package build

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLocalRegistryRef_MapsTheFullTagOntoTheMirror — the push half must land the built image
// under the SAME path the daemon resolves through the mirror, or the next stage's FROM misses.
func TestLocalRegistryRef_MapsTheFullTagOntoTheMirror(t *testing.T) {
	cases := []struct {
		name    string
		mirror  string
		fullTag string
		want    string
	}{
		{
			name:    "the ghcr host is replaced, the path kept",
			mirror:  "127.0.0.1:5000",
			fullTag: "ghcr.io/opencharly/arch:2026.292.1",
			want:    "127.0.0.1:5000/opencharly/arch:2026.292.1",
		},
		{
			name:    "a deeper path keeps every segment",
			mirror:  "localhost:5000",
			fullTag: "ghcr.io/opencharly/sub/name:tag",
			want:    "localhost:5000/opencharly/sub/name:tag",
		},
		{
			name:    "no slash leaves the ref as the path",
			mirror:  "localhost:5000",
			fullTag: "plain:tag",
			want:    "localhost:5000/plain:tag",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := localRegistryRef(tc.mirror, tc.fullTag)
			if got != tc.want {
				t.Errorf("localRegistryRef(%q, %q) = %q, want %q", tc.mirror, tc.fullTag, got, tc.want)
			}
			// The mirror path must be preserved verbatim, or the daemon's resolve cannot
			// match the pushed reference.
			if !strings.HasPrefix(got, tc.mirror+"/") {
				t.Errorf("the mapped ref must stay under the mirror %q: %q", tc.mirror, got)
			}
		})
	}
}

// TestRegistryAnswers_ReadsTheV2Endpoint — the readiness probe is the only thing standing
// between a started registry and the first build that needs it, so both accepted statuses and
// the failure path are pinned here.
func TestRegistryAnswers_ReadsTheV2Endpoint(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v2/" {
				t.Errorf("the probe must hit /v2/, got %q", r.URL.Path)
			}
			w.WriteHeader(status)
		}))
		addr := strings.TrimPrefix(srv.URL, "http://")
		if !registryAnswers(addr) {
			t.Errorf("status %d at /v2/ must count as answering", status)
		}
		srv.Close()
	}

	// A server that answers something else is NOT a registry.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	if registryAnswers(strings.TrimPrefix(srv.URL, "http://")) {
		t.Error("a 404 at /v2/ must not count as a registry")
	}
	srv.Close()

	// A closed listener is not a registry either.
	if registryAnswers("127.0.0.1:1") {
		t.Error("a closed port must not count as a registry")
	}
}

// TestEnsureLocalRegistry_NoMirrorIsANoOp — an unset mirror must not start anything: every
// existing deployment keeps its exact previous behaviour.
func TestEnsureLocalRegistry_NoMirrorIsANoOp(t *testing.T) {
	cfg := driveConfig{RunEngine: "podman", RegistryMirror: ""}
	if err := cfg.ensureLocalRegistry(context.Background()); err != nil {
		t.Fatalf("an empty mirror must be a no-op, got: %v", err)
	}
}

// TestEnsureLocalRegistry_RejectsANonHostPortMirror — a malformed address fails loudly rather
// than starting a registry the daemon could never reach.
func TestEnsureLocalRegistry_RejectsANonHostPortMirror(t *testing.T) {
	cfg := driveConfig{RunEngine: "podman", RegistryMirror: "not-a-hostport"}
	if err := cfg.ensureLocalRegistry(context.Background()); err == nil {
		t.Fatal("a mirror without host:port must be rejected")
	} else if !strings.Contains(err.Error(), "is not host:port") {
		t.Errorf("the error must name the contract, got: %v", err)
	}
}
