package build

// imagestore.go — what the BUILD owns about the charly image store.
//
// The store itself is not this repo's. Its OCI layout (`oci-layout` + ONE `index.json` +
// `blobs/<alg>/<digest>`), the blob copy, the atomic index publication and its cross-process
// lock all live in `sdk/storekit` (`storekit.OpenImageStore`), so the image resolver and this
// build drive read and write ONE implementation (opencharly/sdk#460). The store is the `registry`
// NAMED store under the cache root, and `storekit.StoreDir` is the one resolver for it: D-STORE-1
// blesses exactly one relocation lever (`CHARLY_CACHE_DIR`), so there is no image-specific
// override here, and D-STORE-3 keeps a store's locks in the runtime root, never inside the tree.
//
// Two things stay, because only a build has them:
//
//  1. WHICH reference an image is being built as, and which of a Containerfile's stage references
//     this host can already resolve from the store — see bindStoreContexts in buildkit.go.
//  2. NAMING THE EXPORT. `storekit.ImageStore.Add` refuses an archive that does not already carry
//     `io.containerd.image.name` == the reference, because the SAME file is what the engine load
//     reads. The kit emits a bare `--output type=oci,dest=<file>` with no `name=`, so naming the
//     archive is this side's half of that contract — and the one thing `Add` does not do for the
//     build's export.

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencharly/sdk/storekit"
)

// ociLayoutID is the id the buildctl argv addresses the store by in a context binding
// (`<ref>=oci-layout://<id>@<digest>`). It is a NAME inside an argv, never a path — the DIRECTORY
// comes from the store (D-STORE-1) — and it is the store's own name, so the argv cannot drift from
// the store it actually bound.
const ociLayoutID = storekit.ImageStoreName

// ociIndex is an OCI image layout's `index.json` as a buildkit export writes it. Deliberately the
// full descriptor shape, so a rewritten export keeps every field buildkit wrote.
type ociIndex struct {
	SchemaVersion int           `json:"schemaVersion"`
	MediaType     string        `json:"mediaType,omitempty"`
	Manifests     []ociManifest `json:"manifests"`
}

// ociManifest is one image in the index. Annotations is where the export's naming keys live.
type ociManifest struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Platform    json.RawMessage   `json:"platform,omitempty"`
}

// annotateExport writes the naming/creation annotations into an exported OCI archive's
// `index.json`, IN PLACE (through a temp file + rename).
//
// This is the step that makes the archive self-naming: the engine import that follows applies the
// reference from `io.containerd.image.name`, so the drive needs no load→tag→verify dance, and
// `storekit.ImageStore.Add` — which ingests the SAME file — refuses one that does not already name
// `ref`. `org.opencontainers.image.ref.name` carries the TAG ALONE, which is the shape a
// `name=<full-ref>` buildkit export writes; it is never the lookup key, and the store strips both
// from its payload and re-applies only the naming one when it hands the image back to an engine.
func annotateExport(tarPath, ref string, created time.Time) error {
	if strings.TrimSpace(ref) == "" {
		return fmt.Errorf("annotate the OCI export: no reference — the archive would be unaddressable")
	}
	return rewriteExportIndex(tarPath, func(idx *ociIndex) {
		for i := range idx.Manifests {
			if idx.Manifests[i].Annotations == nil {
				idx.Manifests[i].Annotations = map[string]string{}
			}
			idx.Manifests[i].Annotations[storekit.AnnotationImageName] = ref
			idx.Manifests[i].Annotations[storekit.AnnotationRefName] = refTag(ref)
			idx.Manifests[i].Annotations[storekit.AnnotationCreated] = created.UTC().Format(time.RFC3339)
		}
	})
}

// rewriteExportIndex applies mutate to the archive's `index.json` and rewrites the archive.
func rewriteExportIndex(tarPath string, mutate func(*ociIndex)) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return fmt.Errorf("open the OCI export %s: %w", tarPath, err)
	}
	defer func() { _ = f.Close() }() // read-only open; the close error carries nothing

	tmp, err := os.CreateTemp(filepath.Dir(tarPath), "."+filepath.Base(tarPath)+".*")
	if err != nil {
		return fmt.Errorf("stage the OCI export %s: %w", tarPath, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename has consumed it

	tr := tar.NewReader(f)
	tw := tar.NewWriter(tmp)
	replaced := false
	for {
		hdr, rerr := tr.Next()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			_ = tw.Close()
			_ = tmp.Close()
			return fmt.Errorf("read the OCI export %s: %w", tarPath, rerr)
		}
		if strings.TrimPrefix(hdr.Name, "./") == "index.json" {
			raw, rerr := io.ReadAll(tr)
			if rerr != nil {
				_ = tw.Close()
				_ = tmp.Close()
				return fmt.Errorf("read index.json from %s: %w", tarPath, rerr)
			}
			var idx ociIndex
			if uerr := json.Unmarshal(raw, &idx); uerr != nil {
				_ = tw.Close()
				_ = tmp.Close()
				return fmt.Errorf("parse index.json from %s: %w", tarPath, uerr)
			}
			mutate(&idx)
			out, merr := json.Marshal(idx)
			if merr != nil {
				_ = tw.Close()
				_ = tmp.Close()
				return fmt.Errorf("render index.json for %s: %w", tarPath, merr)
			}
			hdr.Size = int64(len(out))
			if werr := tw.WriteHeader(hdr); werr != nil {
				_ = tw.Close()
				_ = tmp.Close()
				return fmt.Errorf("rewrite index.json in %s: %w", tarPath, werr)
			}
			if _, werr := tw.Write(out); werr != nil {
				_ = tw.Close()
				_ = tmp.Close()
				return fmt.Errorf("rewrite index.json in %s: %w", tarPath, werr)
			}
			replaced = true
			continue
		}
		if werr := tw.WriteHeader(hdr); werr != nil {
			_ = tw.Close()
			_ = tmp.Close()
			return fmt.Errorf("copy %s in %s: %w", hdr.Name, tarPath, werr)
		}
		if _, werr := io.Copy(tw, tr); werr != nil {
			_ = tw.Close()
			_ = tmp.Close()
			return fmt.Errorf("copy %s in %s: %w", hdr.Name, tarPath, werr)
		}
	}
	if !replaced {
		_ = tw.Close()
		_ = tmp.Close()
		return fmt.Errorf("the OCI export %s carries no index.json", tarPath)
	}
	if err := tw.Close(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("finish the OCI export %s: %w", tarPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("finish the OCI export %s: %w", tarPath, err)
	}
	return os.Rename(tmpName, tarPath)
}

// readExportIndex reads an archive's index without rewriting it.
func readExportIndex(tarPath string) (*ociIndex, error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only open; the close error carries nothing
	tr := tar.NewReader(f)
	for {
		hdr, rerr := tr.Next()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
		if strings.TrimPrefix(hdr.Name, "./") != "index.json" {
			continue
		}
		raw, rerr := io.ReadAll(tr)
		if rerr != nil {
			return nil, rerr
		}
		var idx ociIndex
		if uerr := json.Unmarshal(raw, &idx); uerr != nil {
			return nil, uerr
		}
		return &idx, nil
	}
	return nil, fmt.Errorf("the OCI export %s carries no index.json", tarPath)
}

// refTag returns the tag of a reference: everything after the last colon that follows the last
// slash. A digest-pinned ref has no tag and yields "".
func refTag(ref string) string {
	if strings.ContainsRune(ref, '@') {
		return "" // a digest-pinned reference carries no tag
	}
	slash := strings.LastIndexByte(ref, '/')
	colon := strings.LastIndexByte(ref, ':')
	if colon <= slash || colon == len(ref)-1 {
		return ""
	}
	return ref[colon+1:]
}

// containerfileStageRefs is the ordered, deduplicated set of registry references a
// Containerfile can resolve locally: the ARG BASE_IMAGE (the final stage's base) first, then
// every explicit FROM reference.
func containerfileStageRefs(containerfile string) []string {
	var refs []string
	if base := containerfileBaseRef(containerfile); base != "" {
		refs = append(refs, base)
	}
	for _, ref := range containerfileFromRefs(containerfile) {
		dup := false
		for _, seen := range refs {
			if seen == ref {
				dup = true
				break
			}
		}
		if !dup {
			refs = append(refs, ref)
		}
	}
	return refs
}

// containerfileFromRefs returns every REGISTRY reference a Containerfile names in a FROM
// line, in file order and deduplicated. ARG-driven forms (`FROM ${BASE_IMAGE}`, handled by
// containerfileBaseRef) and `scratch` are skipped: they are not registry lookups.
//
// WHY EVERY STAGE, not just the base: a multi-stage Containerfile names its BUILDER stages by
// registry reference too (`FROM ghcr.io/opencharly/arch-builder:<tag> AS build`), and when that
// builder was itself built locally in this run it lives only in the charly image store. Binding
// only the final stage's base left such a stage to be resolved from the registry, where the
// check-run tag does not exist — measured: `charly check run check-openwebui-pod` failed at
// `[internal] load metadata for ghcr.io/opencharly/arch-builder:<tag>: not found`, on the
// unmodified main tree as well.
func containerfileFromRefs(containerfile string) []string {
	seen := map[string]bool{}
	var refs []string
	for _, line := range strings.Split(containerfile, "\n") {
		// Fields, not a "FROM " prefix match: a Dockerfile allows any whitespace after the
		// instruction (a TAB is legal), and the instruction may carry flags before the
		// reference (`FROM --platform=linux/amd64 <ref> AS stage`).
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		ref := ""
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "--") { // FROM flags, not the image reference
				continue
			}
			ref = f
			break
		}
		// A bare stage alias (`FROM builder AS final`) is not a registry reference; it is
		// kept here and harmlessly dropped by the store lookup, which answers only for images
		// it holds.
		if ref == "" || strings.Contains(ref, "$") || strings.EqualFold(ref, "scratch") {
			continue
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	return refs
}

// containerfileBaseRef extracts the direct base reference from a generated Containerfile. The
// renderer emits `ARG BASE_IMAGE=<ref>` as the first instruction of a non-scratch box (see
// sdk/deploykit/generate.go), and the drive overrides that ARG when the base is in the store.
func containerfileBaseRef(containerfile string) string {
	for _, line := range strings.Split(containerfile, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ARG BASE_IMAGE=")
		if ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
