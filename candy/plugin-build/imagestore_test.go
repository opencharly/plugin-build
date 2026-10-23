package build

// imagestore_test.go — the charly image store's contract, pinned at its boundary.
//
// The two claims this file can fail without a daemon or an engine are the ones the drive depends
// on: an export can be NAMED (and the naming key is `io.containerd.image.name`, not the tag-only
// `org.opencontainers.image.ref.name`), and the store merges one entry per reference while copying
// the archive's blobs. The engine-import half of the proof is live in imagestore_live_test.go.

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// storeTempDir points the store and its lock at temp dirs for one test. The state root is
// relocated too: the index lock deliberately lives there, never inside the store tree.
func storeTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(imageStoreDirEnv, dir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return dir
}

// writeExportFixture writes a minimal but VALID OCI layout archive: oci-layout, index.json, and the
// manifest/config/layer blobs the index and manifest point at.
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
	indexBlob, err := json.Marshal(storeIndex{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.index.v1+json",
		Manifests: []storeManifest{{
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
		{"oci-layout", []byte(imageStoreLayoutMarker)},
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

func readTarEntry(t *testing.T, tarPath, name string) []byte {
	t.Helper()
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	tr := tar.NewReader(f)
	for {
		hdr, rerr := tr.Next()
		if rerr == io.EOF {
			t.Fatalf("%s has no %s", tarPath, name)
		}
		if rerr != nil {
			t.Fatal(rerr)
		}
		if hdr.Name == name {
			data, rerr := io.ReadAll(tr)
			if rerr != nil {
				t.Fatal(rerr)
			}
			return data
		}
	}
}

// The naming contract. `name=<full-ref>` is what buildkit writes, and the drive reproduces the
// same three annotations for the archives it builds; a lookup that read the TAG-ONLY annotation
// would be wrong for every namespaced ref, so that annotation must be the tag alone.
func TestAnnotateExportNamesWithTheFullReferenceAndDatesIt(t *testing.T) {
	path, _ := writeExportFixture(t, "named")
	ref := "ghcr.io/opencharly/fedora:2026.296.1200"
	created := time.Date(2026, 10, 23, 10, 49, 59, 0, time.UTC)
	if err := annotateExport(path, ref, created); err != nil {
		t.Fatalf("annotateExport: %v", err)
	}
	idx, err := exportIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	got := idx.Manifests[0].Annotations
	if got[imageNameAnnotation] != ref {
		t.Errorf("%s = %q, want %q", imageNameAnnotation, got[imageNameAnnotation], ref)
	}
	if got[refNameAnnotation] != "2026.296.1200" {
		t.Errorf("%s = %q, want the TAG ALONE (this is the annotation a naive reader keys on)",
			refNameAnnotation, got[refNameAnnotation])
	}
	if got[createdAnnotation] != "2026-10-23T10:49:59Z" {
		t.Errorf("%s = %q, want the RFC3339 build instant", createdAnnotation, got[createdAnnotation])
	}
	// The rewrite must not lose what it did not touch.
	if idx.Manifests[0].Digest == "" || idx.Manifests[0].Size == 0 {
		t.Errorf("annotateExport lost the descriptor: %+v", idx.Manifests[0])
	}
}

// The store is the build's durable artefact: adding copies every blob, one entry per reference,
// and a rebuild of the same reference REPLACES its entry rather than accumulating.
func TestStoreAddCopiesBlobsAndKeepsOneEntryPerReference(t *testing.T) {
	dir := storeTempDir(t)
	ref := "ghcr.io/opencharly/fedora:2026.296.1200"

	first, firstDigest := writeExportFixture(t, "first")
	if err := annotateExport(first, ref, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := storeAdd(first, ref); err != nil {
		t.Fatalf("storeAdd first: %v", err)
	}
	if digest, ok, err := storeLookup(ref); err != nil || !ok || digest != firstDigest {
		t.Fatalf("storeLookup first = (%q, %v, %v), want (%q, true, nil)", digest, ok, err, firstDigest)
	}

	second, secondDigest := writeExportFixture(t, "second")
	if err := annotateExport(second, ref, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := storeAdd(second, ref); err != nil {
		t.Fatalf("storeAdd second: %v", err)
	}
	idx, err := readStoreIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Manifests) != 1 {
		t.Fatalf("the store holds %d entries for one reference, want 1: %+v", len(idx.Manifests), idx.Manifests)
	}
	if idx.Manifests[0].Digest != secondDigest {
		t.Errorf("entry digest = %s, want the newest build %s", idx.Manifests[0].Digest, secondDigest)
	}
	// Every blob the newest archive names must be present; the layer differs between fixtures, so
	// a copy that skipped blobs would leave the store unable to serve the image.
	manifest := readTarEntry(t, second, "blobs/sha256/"+hexOf(secondDigest))
	var m struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{m.Config.Digest, m.Layers[0].Digest} {
		if _, err := os.Stat(filepath.Join(dir, "blobs", "sha256", hexOf(d))); err != nil {
			t.Errorf("blob %s was not copied into the store: %v", d, err)
		}
	}
}

// THE INSTRUMENT CHECK (STRICT_POLICY instrument #6): the store keys on the annotation the engines
// apply, and on nothing else. An entry carrying the tag-only annotation must NOT answer a lookup
// for the full reference — otherwise a store full of tag-only entries would look correct while
// every namespaced ref resolved to the wrong image (or to nothing).
func TestStoreLookupIgnoresTheTagOnlyAnnotation(t *testing.T) {
	dir := storeTempDir(t)
	ref := "ghcr.io/opencharly/fedora:2026.296.1200"
	idx := storeIndex{
		SchemaVersion: 2,
		Manifests: []storeManifest{{
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Digest:    digestOf([]byte("tag-only")),
			Size:      8,
			Annotations: map[string]string{
				refNameAnnotation: "2026.296.1200",
			},
		}},
	}
	data, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureLayout(dir); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(storeIndexPath(dir), data); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := storeLookup(ref); err != nil || ok {
		t.Fatalf("storeLookup found a tag-only entry for %s (ok=%v, err=%v); the lookup must key on %s",
			ref, ok, err, imageNameAnnotation)
	}
}

// A store entry without the naming annotation is unaddressable, so storeAdd must refuse it rather
// than write one — the negative control for the contract above, in the write direction.
func TestStoreAddRefusesAnUnnamedExport(t *testing.T) {
	storeTempDir(t)
	path, _ := writeExportFixture(t, "unnamed")
	if err := storeAdd(path, "ghcr.io/opencharly/fedora:2026.296.1200"); err == nil {
		t.Fatal("storeAdd accepted an export with no io.containerd.image.name annotation")
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
