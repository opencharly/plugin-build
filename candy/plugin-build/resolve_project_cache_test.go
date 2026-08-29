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

func TestProjectCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "charly.yml")
	t.Setenv("CHARLY_DEPLOY_CONFIG", cfg)
	path, key := projectCacheKey(dir)
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
	path, key := projectCacheKey(dir)
	if err := writeProjectCache(path, key, &spec.ResolvedProject{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	// Backdate the entry beyond the TTL.
	data, _ := os.ReadFile(path)
	var cf projectCacheFile
	_ = json.Unmarshal(data, &cf)
	cf.Resolved = time.Now().Add(-2 * projectCacheTTL).UTC().Format(time.RFC3339)
	out, _ := json.Marshal(cf)
	_ = os.WriteFile(path, out, 0o644)
	if _, ok := readProjectCache(path, key); ok {
		t.Fatal("readProjectCache: stale entry should miss")
	}
}
