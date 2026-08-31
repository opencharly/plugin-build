package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencharly/spec/spec"
)

// resolve_project_cache_test.go — the persistent resolved-project cache. Each
// test FAILS without its behavior.

// probeReq is the plain unwidened request for a dir: the shape `charly status` resolves with.
func probeReq(dir string) spec.ResolvedProjectRequest {
	return spec.ResolvedProjectRequest{Dir: dir}
}

func TestProjectCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "charly.yml")
	t.Setenv("CHARLY_DEPLOY_CONFIG", cfg)
	path, key := projectCacheKey(dir, probeReq(dir))
	rp := &spec.ResolvedProject{Version: "2026.240.1943"}
	if err := writeProjectCache(path, key, rp); err != nil {
		t.Fatalf("writeProjectCache: %v", err)
	}
	got, ok := readProjectCache(path, key)
	if !ok {
		t.Fatal("readProjectCache: cache miss after write")
	}
	if got.Version != "2026.240.1943" {
		t.Fatalf("readProjectCache: got %+v", got)
	}
	// A different key is a cache miss.
	if _, ok := readProjectCache(path, "other"); ok {
		t.Fatal("readProjectCache: different key should miss")
	}
}

func TestProjectCacheTTLExpiry(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "charly.yml")
	t.Setenv("CHARLY_DEPLOY_CONFIG", cfg)
	path, key := projectCacheKey(dir, probeReq(dir))
	if err := writeProjectCache(path, key, &spec.ResolvedProject{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	// Backdate the entry beyond the TTL.
	data, _ := os.ReadFile(path)
	var cf projectCacheFile
	_ = json.Unmarshal(data, &cf)
	entry := cf.Entries[key]
	entry.Resolved = time.Now().Add(-2 * projectCacheTTL).UTC().Format(time.RFC3339)
	cf.Entries[key] = entry
	out, _ := json.Marshal(cf)
	_ = os.WriteFile(path, out, 0o644)
	if _, ok := readProjectCache(path, key); ok {
		t.Fatal("readProjectCache: stale entry should miss")
	}
}

// writeProbeProject drops a minimal manifest so projectCacheKey hashes real content rather than
// taking its read-error branch.
func writeProbeProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, spec.UnifiedFileName), []byte("version: 2026.240.1943\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestProjectCacheKeySeparatesScanScope is the regression guard for the add_candy envelope defect:
// the key must separate resolves whose scan SCOPE differs, because those produce different
// envelopes. Keyed on charly.yml + dir alone (the shape this replaces), every case below collides
// and a widened resolve is served an envelope that never scanned its ref — which is exactly how
// `candy "@github.com/opencharly/plugin-quickshell/candy/plugin-quickshell" not in
// resolved-project envelope` reached a correct bed.
func TestProjectCacheKeySeparatesScanScope(t *testing.T) {
	dir := writeProbeProject(t)
	qs := "@github.com/opencharly/plugin-quickshell/candy/plugin-quickshell:v2026.243.1516"
	wl := "@github.com/opencharly/plugin-wl/candy/plugin-wl:v2026.243.1212"

	base := spec.ResolvedProjectRequest{Dir: dir}
	_, baseKey := projectCacheKey(dir, base)

	for _, tc := range []struct {
		name string
		req  spec.ResolvedProjectRequest
	}{
		{"one add_candy ref", spec.ResolvedProjectRequest{Dir: dir, ExtraCandyRefs: []string{qs}}},
		{"a different add_candy ref", spec.ResolvedProjectRequest{Dir: dir, ExtraCandyRefs: []string{wl}}},
		{"include disabled", spec.ResolvedProjectRequest{Dir: dir, IncludeDisabled: true}},
		{"requested boxes", spec.ResolvedProjectRequest{Dir: dir, RequestedBoxes: []string{"some-box"}}},
		{"local superproject", spec.ResolvedProjectRequest{Dir: dir, LocalSuperproject: true}},
	} {
		if _, got := projectCacheKey(dir, tc.req); got == baseKey {
			t.Errorf("%s: key collides with the unwidened resolve (%q) — the cache would serve an envelope resolved for a different scan", tc.name, got)
		}
	}

	// Two DIFFERENT widenings must not collide with each other either.
	_, kq := projectCacheKey(dir, spec.ResolvedProjectRequest{Dir: dir, ExtraCandyRefs: []string{qs}})
	_, kw := projectCacheKey(dir, spec.ResolvedProjectRequest{Dir: dir, ExtraCandyRefs: []string{wl}})
	if kq == kw {
		t.Error("two different add_candy refs share a cache key")
	}
}

// TestProjectCacheKeyOrderIndependent: the same scan requested in a different order is the same
// scan, and must reuse the entry instead of re-resolving the whole project.
func TestProjectCacheKeyOrderIndependent(t *testing.T) {
	dir := writeProbeProject(t)
	a := spec.ResolvedProjectRequest{Dir: dir, ExtraCandyRefs: []string{"@a/candy/a:v1", "@b/candy/b:v2"}, RequestedBoxes: []string{"x", "y"}}
	b := spec.ResolvedProjectRequest{Dir: dir, ExtraCandyRefs: []string{"@b/candy/b:v2", "@a/candy/a:v1"}, RequestedBoxes: []string{"y", "x"}}
	_, ka := projectCacheKey(dir, a)
	_, kb := projectCacheKey(dir, b)
	if ka != kb {
		t.Errorf("reordering the same scope changed the key:\n  %q\n  %q", ka, kb)
	}
}

// TestProjectCacheHoldsConcurrentScopes proves the on-disk cache keeps one entry PER scope: a
// deploy compiling several add_candy refs must not have each write evict the last, or every ref
// after the first re-resolves the entire project.
func TestProjectCacheHoldsConcurrentScopes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project.json")
	unwidened := &spec.ResolvedProject{Candies: map[string]spec.CandyView{"plugin-wl": {}}}
	widened := &spec.ResolvedProject{Candies: map[string]spec.CandyView{"plugin-wl": {}, "plugin-quickshell": {}}}

	if err := writeProjectCache(path, "scope-unwidened", unwidened); err != nil {
		t.Fatal(err)
	}
	if err := writeProjectCache(path, "scope-widened", widened); err != nil {
		t.Fatal(err)
	}

	got, ok := readProjectCache(path, "scope-widened")
	if !ok {
		t.Fatal("the widened entry was evicted by the unwidened write")
	}
	if _, has := got.Candies["plugin-quickshell"]; !has {
		t.Error("the widened entry lost its add_candy-only candy")
	}
	if got, ok := readProjectCache(path, "scope-unwidened"); !ok || len(got.Candies) != 1 {
		t.Error("the unwidened entry did not survive the widened write")
	}
	if _, ok := readProjectCache(path, "scope-never-written"); ok {
		t.Error("an unknown scope was served a cached envelope")
	}
}

// TestProjectCacheEvictsOldest keeps the file bounded.
func TestProjectCacheEvictsOldest(t *testing.T) {
	entries := map[string]projectCacheEntry{
		"old":    {Resolved: "2020-01-01T00:00:00Z"},
		"newer":  {Resolved: "2026-01-01T00:00:00Z"},
		"newest": {Resolved: "2026-06-01T00:00:00Z"},
	}
	evictOldestProjectCacheEntries(entries, 2)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	if _, ok := entries["old"]; ok {
		t.Error("evicted the wrong entry — the oldest must go first")
	}
}
