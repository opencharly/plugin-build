package build

import (
	"bytes"
	"strings"
	"testing"
)

// TestParseStoreReclaimable covers the `podman system df --format json` payload parsing: the
// Images entry's raw reclaimable/total bytes, a payload with no Images entry, and a non-JSON
// payload (all fail-soft paths in warnIfStoreBloated).
func TestParseStoreReclaimable(t *testing.T) {
	payload := []byte(`[
  {"Type":"Images","Total":2597,"Active":259,"RawSize":111909732884,"RawReclaimable":105746969002,"TotalCount":2597,"Size":"111.9GB","Reclaimable":"105.7GB (94%)"},
  {"Type":"Containers","Total":1,"Active":1,"RawSize":2162338,"RawReclaimable":0,"TotalCount":1,"Size":"2.162MB","Reclaimable":"0B (0%)"}
]`)
	reclaimable, total, err := parseStoreReclaimable(payload)
	if err != nil {
		t.Fatalf("parseStoreReclaimable: %v", err)
	}
	if reclaimable != 105746969002 {
		t.Errorf("reclaimable = %d, want 105746969002", reclaimable)
	}
	if total != 111909732884 {
		t.Errorf("total = %d, want 111909732884", total)
	}
}

func TestParseStoreReclaimableNoImages(t *testing.T) {
	payload := []byte(`[{"Type":"Containers","RawSize":1,"RawReclaimable":0}]`)
	if _, _, err := parseStoreReclaimable(payload); err == nil {
		t.Fatal("expected error for payload with no Images entry")
	}
}

func TestParseStoreReclaimableBadJSON(t *testing.T) {
	if _, _, err := parseStoreReclaimable([]byte("not json")); err == nil {
		t.Fatal("expected error for non-JSON payload")
	}
}

// TestWarnIfStoreBloatedFromPayload pins the threshold: a store with reclaimable bytes above
// storeBloatReclaimableThreshold warns and names `charly clean --deep`; a store below the
// threshold stays silent; a non-podman engine never probes.
func TestWarnIfStoreBloatedFromPayload(t *testing.T) {
	bloated := []byte(`[{"Type":"Images","RawSize":111909732884,"RawReclaimable":105746969002}]`)
	var buf bytes.Buffer
	warnIfStoreBloatedFromPayload(bloated, &buf)
	if !strings.Contains(buf.String(), "charly clean --deep") {
		t.Errorf("bloated store: want a warning naming `charly clean --deep`, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "#173") {
		t.Errorf("bloated store: want the issue reference, got %q", buf.String())
	}

	lean := []byte(`[{"Type":"Images","RawSize":1073741824,"RawReclaimable":107374182}]`) // 1 GiB total, 100 MiB reclaimable
	buf.Reset()
	warnIfStoreBloatedFromPayload(lean, &buf)
	if buf.Len() != 0 {
		t.Errorf("lean store: want no warning, got %q", buf.String())
	}
}

// TestWarnIfStoreBloatedEngineGate pins that the probe only runs for the podman engine (docker
// builds never exec podman system df).
func TestWarnIfStoreBloatedEngineGate(t *testing.T) {
	var buf bytes.Buffer
	// engine != "podman" must return without probing; the payload is irrelevant.
	warnIfStoreBloated("docker", &buf)
	if buf.Len() != 0 {
		t.Errorf("docker engine: want no warning, got %q", buf.String())
	}
}
