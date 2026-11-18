package build

// imagestore_test.go — what the BUILD owns about the charly image store, pinned at its boundary.
//
// The store's own contract (the layout, the index merge, the blob copy, the lock) is storekit's
// and is tested there (opencharly/sdk#460). What is tested HERE is the build's half of that
// contract: the export is NAMED (and the naming key is `io.containerd.image.name`, not the
// tag-only `org.opencontainers.image.ref.name`), the build's export is INGESTIBLE by the shared
// store, and the tree that lands is the `registry` named store under the cache root and nothing
// else. The engine-import half of the proof is live in imagestore_live_test.go.

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/opencharly/sdk/storekit"
)

// cacheRoot points the build's store at a throwaway cache root. `CHARLY_CACHE_DIR` is D-STORE-1's
// ONE relocation lever, and it is the same one a disposable bed uses — so there is no second
// override for this test to reach for.
func cacheRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CHARLY_CACHE_DIR", root)
	return root
}

// writeExportFixture writes a minimal but VALID OCI layout archive as buildkit emits one:
// oci-layout, index.json, and the manifest/config/layer blobs the index and manifest point at.
func writeExportFixture(t *testing.T, label string) (path, manifestDigest string) {
	t.Helper()
	configBlob := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	layerBlob := []byte("layer-" + label)
	configDigest := digestOf(configBlob)
	layerDigest := digestOf(layerBlob)
	manifestBlob, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    configDigest,
			"size":      len(configBlob),
		},
		"layers": []map[string]any{{
			"mediaType": "application/vnd.oci.image.layer.v1.tar",
			"digest":    layerDigest,
			"size":      len(layerBlob),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest = digestOf(manifestBlob)
	indexBlob, err := json.Marshal(ociIndex{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.index.v1+json",
		Manifests: []ociManifest{{
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Digest:    manifestDigest,
			Size:      int64(len(manifestBlob)),
			Annotations: map[string]string{
				"org.opencontainers.image.ref.name": "fixture-tag",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	path = filepath.Join(t.TempDir(), "export.tar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for _, e := range []struct {
		name string
		data []byte
	}{
		{"oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)},
		{"index.json", indexBlob},
		{"blobs/sha256/" + hexOf(manifestDigest), manifestBlob},
		{"blobs/sha256/" + hexOf(configDigest), configBlob},
		{"blobs/sha256/" + hexOf(layerDigest), layerBlob},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path, manifestDigest
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hexOf(digest string) string {
	const prefix = "sha256:"
	if len(digest) > len(prefix) && digest[:len(prefix)] == prefix {
		return digest[len(prefix):]
	}
	return digest
}

// The naming contract. `name=<full-ref>` is what a buildkit export writes, and the drive
// reproduces the same three annotations for the archives it builds; a lookup that read the
// TAG-ONLY annotation would be wrong for every namespaced ref, so that annotation must be the tag
// alone.
func TestAnnotateExportNamesWithTheFullReferenceAndDatesIt(t *testing.T) {
	path, _ := writeExportFixture(t, "named")
	ref := "ghcr.io/opencharly/fedora:2026.296.1200"
	created := time.Date(2026, 10, 23, 10, 49, 59, 0, time.UTC)
	if err := annotateExport(path, ref, created); err != nil {
		t.Fatalf("annotateExport: %v", err)
	}
	idx, err := readExportIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	got := idx.Manifests[0].Annotations
	if got[storekit.AnnotationImageName] != ref {
		t.Errorf("%s = %q, want %q", storekit.AnnotationImageName, got[storekit.AnnotationImageName], ref)
	}
	if got[storekit.AnnotationRefName] != "2026.296.1200" {
		t.Errorf("%s = %q, want the TAG ALONE (this is the annotation a naive reader keys on)",
			storekit.AnnotationRefName, got[storekit.AnnotationRefName])
	}
	if got[storekit.AnnotationCreated] != "2026-10-23T10:49:59Z" {
		t.Errorf("%s = %q, want the RFC3339 build instant", storekit.AnnotationCreated, got[storekit.AnnotationCreated])
	}
	// The rewrite must not lose what it did not touch.
	if idx.Manifests[0].Digest == "" || idx.Manifests[0].Size == 0 {
		t.Errorf("annotateExport lost the descriptor: %+v", idx.Manifests[0])
	}
}

// THE RELOCATION PROOF, on the build's side of the seam: the export the drive names is exactly
// what `storekit.ImageStore.Add` accepts, and what lands is the `registry` named store under the
// cache root — keyed by a DIGEST, not by the reference, and carrying the layout and NOTHING else
// (D-STORE-3: no `.lock`, no sidecar; the store's locks live in the runtime root).
func TestBuildExportLandsInTheRegistryStoreKeyedByDigest(t *testing.T) {
	root := cacheRoot(t)
	ref := "ghcr.io/opencharly/fedora:2026.296.1200"

	path, manifestDigest := writeExportFixture(t, "ingest")
	if err := annotateExport(path, ref, time.Date(2026, 10, 23, 10, 49, 59, 0, time.UTC)); err != nil {
		t.Fatalf("annotateExport: %v", err)
	}

	store, err := storekit.OpenImageStore()
	if err != nil {
		t.Fatalf("OpenImageStore: %v", err)
	}
	wantDir := filepath.Join(root, storekit.ImageStoreName)
	if got := store.Dir(); got != wantDir {
		t.Errorf("the build wrote store dir %q, want %q (the named store under the cache root)", got, wantDir)
	}
	if err := store.Add(path, ref); err != nil {
		t.Fatalf("the store refused the build's own export: %v", err)
	}

	// The reference resolves to a DIGEST, and that digest is the entry's key.
	digest, ok, err := store.Lookup(ref)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !ok || digest == "" {
		t.Fatalf("the store holds no entry for %s (ok=%v, digest=%q)", ref, ok, digest)
	}
	if _, err := os.Stat(filepath.Join(wantDir, "blobs", "sha256", hexOf(digest))); err != nil {
		t.Errorf("the entry's digest %s is not a blob in the store: %v", digest, err)
	}

	// The ENTRY is a digest-keyed entry of the store: `index.json` holds one descriptor for it,
	// whose cache-key annotation IS a digest, and the entry manifest it points at carries that key,
	// the reference, and the build time (the resolution record, outside the payload's digest).
	raw, err := os.ReadFile(filepath.Join(wantDir, "index.json"))
	if err != nil {
		t.Fatalf("read the store index: %v", err)
	}
	var idx struct {
		SchemaVersion int `json:"schemaVersion"`
		Manifests     []struct {
			MediaType   string            `json:"mediaType"`
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatalf("parse the store index: %v", err)
	}
	if len(idx.Manifests) != 1 {
		t.Fatalf("the store index holds %d descriptors for one build, want 1", len(idx.Manifests))
	}
	desc := idx.Manifests[0]
	if desc.MediaType != storekit.MediaTypeStoreEntry {
		t.Errorf("the descriptor's media type = %q, want a store entry (%q)", desc.MediaType, storekit.MediaTypeStoreEntry)
	}
	if got := desc.Annotations[storekit.AnnotationCacheKey]; got != digest {
		t.Errorf("the descriptor's %s = %q, want the entry's digest key %q",
			storekit.AnnotationCacheKey, got, digest)
	}
	if !strings.HasPrefix(desc.Annotations[storekit.AnnotationCacheKey], "sha256:") {
		t.Errorf("the entry is keyed %q, want a content digest", desc.Annotations[storekit.AnnotationCacheKey])
	}
	entryRaw, err := os.ReadFile(filepath.Join(wantDir, "blobs", "sha256", hexOf(desc.Digest)))
	if err != nil {
		t.Fatalf("read the entry manifest %s: %v", desc.Digest, err)
	}
	var entry storekit.Entry
	if err := json.Unmarshal(entryRaw, &entry); err != nil {
		t.Fatalf("parse the entry manifest: %v", err)
	}
	if entry.Key != digest {
		t.Errorf("the entry's key = %q, want the digest %q the reference resolved to", entry.Key, digest)
	}
	if entry.Annotations[storekit.AnnotationImageName] != ref {
		t.Errorf("the entry names %q, want %q", entry.Annotations[storekit.AnnotationImageName], ref)
	}
	if entry.Annotations[storekit.AnnotationCreated] != "2026-10-23T10:49:59Z" {
		t.Errorf("the entry's %s = %q, want the export's build instant",
			storekit.AnnotationCreated, entry.Annotations[storekit.AnnotationCreated])
	}

	// The entry's payload is the artifact index the export carried, so the chain reaches the image
	// manifest buildkit actually built — and the payload holds NO name and NO timestamp (D-STORE-6).
	artifact, err := os.ReadFile(filepath.Join(wantDir, "blobs", "sha256", hexOf(entry.Payload)))
	if err != nil {
		t.Fatalf("read the stored artifact index %s: %v", entry.Payload, err)
	}
	var stored ociIndex
	if err := json.Unmarshal(artifact, &stored); err != nil {
		t.Fatalf("parse the stored artifact index: %v", err)
	}
	if len(stored.Manifests) != 1 || stored.Manifests[0].Digest != manifestDigest {
		t.Errorf("the stored artifact index points at %+v, want the built manifest %s", stored.Manifests, manifestDigest)
	}
	if _, named := stored.Manifests[0].Annotations[storekit.AnnotationImageName]; named {
		t.Errorf("the stored payload still carries %s — a payload holds no name (D-STORE-6)",
			storekit.AnnotationImageName)
	}

	// The store is DISCOVERABLE — `charly clean --cache` sweeps what `NamedStores` lists, so a
	// store it cannot see would grow forever (D-STORE-7).
	names, err := storekit.NamedStores()
	if err != nil {
		t.Fatalf("NamedStores: %v", err)
	}
	if len(names) != 1 || names[0] != storekit.ImageStoreName {
		t.Errorf("NamedStores() = %v, want [%q]", names, storekit.ImageStoreName)
	}

	// D-STORE-3: the tree is `oci-layout` + `index.json` + `blobs/` and NOTHING else.
	var tree []string
	err = filepath.WalkDir(wantDir, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(wantDir, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == "oci-layout" || rel == "index.json" || strings.HasPrefix(rel, "blobs/") {
			return nil
		}
		tree = append(tree, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk the store tree: %v", err)
	}
	if len(tree) != 0 {
		sort.Strings(tree)
		t.Errorf("the store tree carries files that are NOT the layout (D-STORE-3 forbids a lock or any sidecar): %v", tree)
	}
}

func TestContainerfileBaseRefReadsTheGeneratedArgument(t *testing.T) {
	cf := "ARG BASE_IMAGE=ghcr.io/opencharly/fedora:2026.296.1200\n\nFROM ${BASE_IMAGE}\n"
	if got := containerfileBaseRef(cf); got != "ghcr.io/opencharly/fedora:2026.296.1200" {
		t.Errorf("containerfileBaseRef = %q", got)
	}
	if got := containerfileBaseRef("FROM scratch\n"); got != "" {
		t.Errorf("a scratch box has no base argument, got %q", got)
	}
}

func TestRefTag(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/opencharly/fedora:2026.296.1200": "2026.296.1200",
		"localhost:5000/opencharly/x:1":           "1",
		"ghcr.io/opencharly/fedora":               "",
		"ghcr.io/opencharly/fedora@sha256:abc":    "",
	}
	for ref, want := range cases {
		if got := refTag(ref); got != want {
			t.Errorf("refTag(%q) = %q, want %q", ref, got, want)
		}
	}
}
