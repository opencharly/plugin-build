package build

// imagestore.go — the CHARLY IMAGE STORE: the engine-neutral artefact of every build.
//
// One OCI image layout at `$XDG_DATA_HOME/charly/images/` — `oci-layout` + ONE `index.json` +
// `blobs/<alg>/<digest>` — holding every image this host has built. It is engine-NEUTRAL on
// purpose: the build's canonical output must not belong to whichever container engine happens to
// be selected, because the next deploy may resolve a different one.
//
// ⚑ THE NAMING ANNOTATION IS THE WHOLE CONTRACT, and it is `io.containerd.image.name`, NOT
// `org.opencontainers.image.ref.name`. Measured on buildkit 0.34.1: a `--output
// type=oci,name=<full-ref>` export writes BOTH — `io.containerd.image.name` = `<repo>:<tag>` and
// `org.opencontainers.image.ref.name` = the TAG ALONE. Every engine measured (podman 6.1.4,
// docker 29.9.2, nerdctl 2.4.1) imports such an archive with the full reference already applied
// and needs NO `tag` step; an archive built WITHOUT `name=` lands untagged/dangling everywhere.
// Store lookups therefore key on `io.containerd.image.name` only — keying on the tag-only
// annotation would match the wrong image (or none) for every namespaced ref.

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencharly/sdk/storekit"
	"github.com/opencharly/spec/lock"
)

const (
	// imageStoreDirName is the layout's path element under the charly data root.
	imageStoreDirName = "images"
	// imageStoreID is the id a solve addresses the store by (`oci-layout://<id>@<digest>`).
	imageStoreID = "charly"
	// imageStoreDirEnv relocates the store for a disposable target, exactly as
	// CHARLY_CACHE_DIR relocates the cache root. A check bed MUST use it: a bed's build must
	// not write the operator's store.
	imageStoreDirEnv = "CHARLY_IMAGE_STORE_DIR"

	// imageNameAnnotation is the annotation that NAMES an image inside an OCI layout index.
	imageNameAnnotation = "io.containerd.image.name"
	// refNameAnnotation carries the TAG ALONE in a buildkit export; it is never the lookup key.
	refNameAnnotation = "org.opencontainers.image.ref.name"
	// createdAnnotation dates an entry, and is the store's recency key.
	createdAnnotation = "org.opencontainers.image.created"

	// imageStoreLockName is the store-index lock, deliberately OUTSIDE the store tree: a lock is
	// transient state with no content address and must never live among content-addressed blobs.
	imageStoreLockName = "image-store.lock"
)

// imageStoreLayoutMarker is the `oci-layout` file's content.
const imageStoreLayoutMarker = `{"imageLayoutVersion":"1.0.0"}`

// storeIndex is an OCI image layout's `index.json`. It is deliberately the full descriptor shape,
// so a rewritten export keeps every field buildkit wrote.
type storeIndex struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType,omitempty"`
	Manifests     []storeManifest `json:"manifests"`
}

// storeManifest is one image in the index. Annotations is where the two naming keys live.
type storeManifest struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Platform    json.RawMessage   `json:"platform,omitempty"`
}

// imageStoreDir resolves the charly image store.
//
// The store lives under the DATA root (`$XDG_DATA_HOME/charly/images`), not the cache root: a
// built image is a durable artefact, and the cache root's `images` path is the RETIRED pre-rename
// blob store that plugin-clean's retirement record still sweeps.
//
// WORKAROUND (opencharly/sdk#458): storekit — the ONE roots resolver — has no data root yet, so
// this file resolves `$XDG_DATA_HOME` itself. When RootData lands, this function becomes
// `storekit.Dir(storekit.RootData)` + `images` and the local resolution goes away. It is not a
// second resolver for an EXISTING root, which is the duplication storekit's doctrine forbids.
func imageStoreDir() (string, error) {
	if dir := os.Getenv(imageStoreDirEnv); dir != "" {
		return dir, nil
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve the charly image store: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "charly", imageStoreDirName), nil
}

// storeIndexPath is the layout's entry point.
func storeIndexPath(dir string) string { return filepath.Join(dir, "index.json") }

// ensureLayout creates the layout directories and the `oci-layout` marker. It is idempotent.
func ensureLayout(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o755); err != nil {
		return fmt.Errorf("prepare the charly image store %s: %w", dir, err)
	}
	marker := filepath.Join(dir, storekit.LayoutFile)
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect the charly image store marker: %w", err)
	}
	return writeFileAtomic(marker, []byte(imageStoreLayoutMarker))
}

// writeFileAtomic publishes data at dest through a unique temp file in dest's own directory.
func writeFileAtomic(dest string, data []byte) error {
	return copyFileAtomic(dest, bytes.NewReader(data))
}

// copyFileAtomic publishes a STREAM at dest through a unique temp file in dest's own directory,
// then renames. Concurrent writers must never share a temp name, and a reader must never see a
// partial index. The stream shape matters: a blob is copied layer by layer, and buffering a whole
// layer in memory is not something an image build can afford.
func copyFileAtomic(dest string, src io.Reader) error {
	f, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }() // no-op once the rename has consumed it
	if _, err := io.Copy(f, src); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, dest)
}

// readStoreIndex reads the layout's index. A missing index is an EMPTY store, never an error.
func readStoreIndex(dir string) (*storeIndex, error) {
	data, err := os.ReadFile(storeIndexPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return &storeIndex{SchemaVersion: 2, MediaType: "application/vnd.oci.image.index.v1+json"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the charly image store index: %w", err)
	}
	var idx storeIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse the charly image store index %s: %w", storeIndexPath(dir), err)
	}
	return &idx, nil
}

// storeLookup returns the MANIFEST digest a reference resolves to inside the store, keyed on
// `io.containerd.image.name` — the full reference — because that is the annotation the engines
// apply and the only one that names a namespaced ref unambiguously.
func storeLookup(ref string) (digest string, ok bool, err error) {
	dir, err := imageStoreDir()
	if err != nil {
		return "", false, err
	}
	idx, err := readStoreIndex(dir)
	if err != nil {
		return "", false, err
	}
	for _, m := range idx.Manifests {
		if m.Annotations[imageNameAnnotation] == ref {
			return m.Digest, true, nil
		}
	}
	return "", false, nil
}

// annotateExport writes the naming/creation annotations into an exported OCI archive's
// `index.json`, IN PLACE (through a temp file + rename).
//
// This is the step that makes the archive self-naming: the engine import that follows applies the
// reference from `io.containerd.image.name`, so the drive no longer needs the load→tag→verify
// dance the export bridge used to require.
func annotateExport(tarPath, ref string, created time.Time) error {
	if strings.TrimSpace(ref) == "" {
		return fmt.Errorf("annotate the OCI export: no reference — the archive would be unaddressable")
	}
	return rewriteExportIndex(tarPath, func(idx *storeIndex) {
		for i := range idx.Manifests {
			if idx.Manifests[i].Annotations == nil {
				idx.Manifests[i].Annotations = map[string]string{}
			}
			idx.Manifests[i].Annotations[imageNameAnnotation] = ref
			idx.Manifests[i].Annotations[refNameAnnotation] = refTag(ref)
			idx.Manifests[i].Annotations[createdAnnotation] = created.UTC().Format(time.RFC3339)
		}
	})
}

// rewriteExportIndex applies mutate to the archive's `index.json` and rewrites the archive.
func rewriteExportIndex(tarPath string, mutate func(*storeIndex)) error {
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
			var idx storeIndex
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

// exportIndex reads an archive's index without rewriting it.
func exportIndex(tarPath string) (*storeIndex, error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
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
		var idx storeIndex
		if uerr := json.Unmarshal(raw, &idx); uerr != nil {
			return nil, uerr
		}
		return &idx, nil
	}
	return nil, fmt.Errorf("the OCI export %s carries no index.json", tarPath)
}

// storeAdd copies an exported archive's blobs into the store and merges its manifest descriptor(s)
// into the store index, replacing any entry that already names the same reference. One descriptor
// is written per manifest in the export (a multi-platform `--push` export contributes one per
// platform); a rebuild replaces the whole previous set for that reference.
//
// The blobs are content-addressed, so an existing blob is the same bytes and is left alone; the
// index is the only mutable file and it is rewritten atomically under a cross-process lock (two
// levels of a build write the store concurrently — see drive.go's errgroup).
func storeAdd(tarPath, ref string) error {
	dir, err := imageStoreDir()
	if err != nil {
		return err
	}
	if err := ensureLayout(dir); err != nil {
		return err
	}
	exported, err := exportIndex(tarPath)
	if err != nil {
		return fmt.Errorf("read the OCI export index: %w", err)
	}
	if len(exported.Manifests) == 0 {
		return fmt.Errorf("the OCI export %s carries no image manifest", tarPath)
	}
	if exported.Manifests[0].Annotations[imageNameAnnotation] == "" {
		// A store entry without the naming annotation is not addressable, which is the whole
		// contract of this file — refuse rather than write a dangling entry.
		return fmt.Errorf("the OCI export %s carries no %s annotation", tarPath, imageNameAnnotation)
	}
	if err := copyBlobsIntoStore(tarPath, dir); err != nil {
		return err
	}

	lockPath, err := storeLockPath()
	if err != nil {
		return err
	}
	release, err := lock.AcquireFileLock(lockPath, true)
	if err != nil {
		return fmt.Errorf("lock the charly image store index: %w", err)
	}
	defer func() { _ = release() }() // the release error carries nothing a caller can act on

	idx, err := readStoreIndex(dir)
	if err != nil {
		return err
	}
	kept := idx.Manifests[:0]
	for _, m := range idx.Manifests {
		if m.Annotations[imageNameAnnotation] == ref {
			continue // superseded by this build
		}
		kept = append(kept, m)
	}
	idx.Manifests = append(kept, exported.Manifests...)
	data, err := json.Marshal(idx)
	if err != nil {
		return fmt.Errorf("render the charly image store index: %w", err)
	}
	if err := writeFileAtomic(storeIndexPath(dir), data); err != nil {
		return fmt.Errorf("write the charly image store index: %w", err)
	}
	return nil
}

// copyBlobsIntoStore copies every `blobs/` entry of the archive into the layout, skipping blobs
// that already exist. Names are validated so a crafted archive cannot escape the store tree.
func copyBlobsIntoStore(tarPath, dir string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	tr := tar.NewReader(f)
	for {
		hdr, rerr := tr.Next()
		if errors.Is(rerr, io.EOF) {
			return nil
		}
		if rerr != nil {
			return fmt.Errorf("read the OCI export %s: %w", tarPath, rerr)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if !strings.HasPrefix(name, "blobs/") || hdr.Typeflag != tar.TypeReg {
			continue
		}
		if strings.Contains(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("the OCI export %s carries an unsafe blob path %q", tarPath, name)
		}
		dest := filepath.Join(dir, filepath.FromSlash(name))
		if _, serr := os.Stat(dest); serr == nil {
			continue
		} else if !errors.Is(serr, os.ErrNotExist) {
			return serr
		}
		if merr := os.MkdirAll(filepath.Dir(dest), 0o755); merr != nil {
			return merr
		}
		// STREAM the blob: a layer can be gigabytes, and the store must not hold one in memory.
		if werr := copyFileAtomic(dest, tr); werr != nil {
			return fmt.Errorf("write blob %s: %w", name, werr)
		}
	}
}

// storeLockPath is the store-index lock, in the state root (never inside the store tree).
func storeLockPath() (string, error) {
	state, err := storekit.Dir(storekit.RootState)
	if err != nil {
		return "", fmt.Errorf("resolve the charly state root for the image store lock: %w", err)
	}
	return filepath.Join(state, "locks", imageStoreLockName), nil
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
