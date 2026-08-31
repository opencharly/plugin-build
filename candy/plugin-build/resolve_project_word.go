package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/buildkit"
	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/spec/spec"
)

// resolve_project_word.go — the `build:project` word (#55 step3 unit 3b): the PLUGIN-SIDE
// replacement for charly core's now-deleted resolved-project host seam
// (charly/resolved_project_host.go's hostBuildResolvedProject/buildResolvedProjectFromDir). The
// ~8 external consumers (candy/plugin-box, plugin-fleet ×2, plugin-check, plugin-preempt,
// plugin-installstep, plugin-status, plugin-substrate) that used to reach that now-deleted host
// seam directly now call
// `exec.InvokeProvider(ctx, "build", "project", sdk.OpResolve, reqJSON, nil, ...)` instead — a
// class-generic word on the EXISTING `build` provider (candy/plugin-build already owns this exact
// resolve, box/generate, ensure, pkg), reached over the EXISTING plugin↔plugin InvokeProvider
// peer-dispatch. No new seam invented (F11).
//
// BYTE-FOR-BYTE RELOCATION (cutover-policy "relocation step-sequence PARITY"): this deliberately
// mirrors the DELETED host projector's exact steps — load, vocab, scan, resolve boxes, project the
// envelope — and DELIBERATELY OMITS render-prep (dg.RenderPrepAll), unlike resolveBuildEngine's
// (this package's build:box/build:generate resolve) full pipeline. The current host projector
// never render-prepped ROOT boxes either (only namespaced ones — formerly via the deleted
// host namespaced-box fill's own tempGen.toDeploykit().RenderPrepBox, now via the plugin-side
// namespace walk's deploykit.FillNamespaceBoxViews (resolve_legs.go) — an existing,
// UNTOUCHED asymmetry tracked separately as task #69). Changing that asymmetry here would smuggle
// a behavior change into a pure boundary move — forbidden by the parity requirement.
func resolveProjectEnvelope(ctx context.Context, ex *sdk.Executor, req spec.ResolvedProjectRequest) (spec.ResolvedProject, error) {
	dir := req.Dir
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return spec.ResolvedProject{}, err
		}
		dir = cwd
	}

	// Persistent cache: the project load (LoadUnified over the full import
	// closure) is the dominant cost of `charly status` (measured ~4.4s + GC
	// pressure per load, and the status fan-out loads it once per collector).
	// The project does not change often — only an edit to charly.yml or its
	// imports mutates it — so the first call after the TTL expires re-fetches
	// with user feedback and every subsequent call within the TTL reads the
	// cache. The LIVE container state (podman ps) is never cached.
	cachePath, key := projectCacheKey(dir, req)
	if cachePath != "" {
		if rp, ok := readProjectCache(cachePath, key); ok {
			return *rp, nil
		}
	}
	fmt.Fprintf(os.Stderr, "charly: resolving project (first run — may take a moment)...\n")

	// LocalSuperproject mirrors the deleted charly/resolved_project_host.go's
	// applySelfSuperprojectOverride(dir) call — reproduced here PURELY (os/exec + loaderkit, zero
	// host-only coupling), since a compiled-in plugin shares the host's OS process/environment, so
	// os.Setenv here is visible to the SAME per-call ensureRepoLeg
	// EnsureRepoDownloaded call that reads CHARLY_REPO_OVERRIDE. Used today by
	// candy/plugin-check/checkproject.go.
	if req.LocalSuperproject {
		restore := applySelfSuperprojectOverridePlugin(dir)
		defer restore()
	}

	uf, ok, err := loaderkit.LoadUnifiedViaExecutor(ctx, ex, dir)
	if err != nil {
		return spec.ResolvedProject{}, err
	}
	if !ok || uf == nil {
		// Project-less directory → empty envelope (the SAME empty-project contract the deleted
		// buildResolvedProjectFromDir/loadProjectForResolve honoured).
		return spec.ResolvedProject{}, nil
	}
	cfg := uf.ProjectConfig()

	distroCfg := loaderkit.ProjectDistroConfig(uf, resolveDistroLeg(ctx, ex))
	builderCfg := loaderkit.ProjectBuilderConfig(uf)
	initCfg := loaderkit.ProjectInitConfig(uf, resolveInitLeg(ctx, ex))

	// SCAN: threads req VERBATIM (Dir/IncludeDisabled/ExtraCandyRefs) — unlike resolveBuildEngine's
	// own scan (which hardcodes ExtraCandyRefs: nil, since build/generate never widens the scan),
	// the deleted host projector DID thread ExtraCandyRefs through ScanAllCandyWithConfigOpts, so
	// this must too (candy/plugin-installstep + plugin-fleet both rely on it for add_candy: refs).
	localScanned, err := scanLocalLeg(ctx, ex, uf, dir, distroCfg)
	if err != nil {
		return spec.ResolvedProject{}, err
	}
	layers, err := loaderkit.ScanCandyFromLocal(localScanned, initCfg, scanSeamsLeg(ctx, ex, req, cfg, distroCfg, stderrWarn))
	if err != nil {
		return spec.ResolvedProject{}, err
	}

	// No build-time plugin CONNECT, no pre-build VALIDATE gate — the deleted host projector never
	// ran either (those are resolveBuildEngine-only, for the actual build/generate drive).

	calver := buildkit.ComputeCalVer()

	// preResolvedBoxes=nil: a FRESH per-box ResolveBox loop, no render-prep — byte-identical to the
	// deleted host projector's own fresh resolve (never fed pre-rendered boxes).
	rp, err := projectResolvedProjectLeg(ctx, ex, cfg, layers, uf, distroCfg, builderCfg, initCfg, dir, uf.Version, calver, req.IncludeDisabled, nil, nil)
	if err != nil {
		return spec.ResolvedProject{}, err
	}

	// Primaries: the SAME registry-derived D-fact snapshot the deleted host projector filled via
	// loaderThreaded().Primaries — reached here through the ALREADY-EXISTING "loader-threaded"
	// HostBuild leg via loaderkit.LoaderThreadedViaExecutor (no new seam).
	rp.Primaries = loaderkit.LoaderThreadedViaExecutor(ctx, ex).Primaries

	if cachePath != "" {
		_ = writeProjectCache(cachePath, key, rp)
	}
	return *rp, nil
}

// projectCacheTTL is how long a cached resolved project is trusted before a
// re-fetch. The project changes only on an edit to charly.yml or its imports,
// so a 5-minute TTL makes consecutive status runs fast while still seeing edits
// within a few minutes.
const projectCacheTTL = 5 * time.Minute

// projectCacheEntries bounds the on-disk cache. The key carries the request's scan SCOPE, so one
// project legitimately has several live entries at once — an unwidened `charly status` resolve
// plus one per add_candy: ref a deploy compiles. A handful covers that; the oldest is evicted.
const projectCacheEntries = 16

// projectCacheKey returns the resolved-project cache file + a content key: the charly.yml content
// hash, the project dir, AND every request field that changes what the resolve PRODUCES rather
// than merely what it costs.
//
// The scan-widening fields are part of the key because they change the envelope's CONTENTS.
// ExtraCandyRefs (a deploy's add_candy: refs) is the ONLY way a host-side plugin candy that no
// image closure reaches enters rp.Candies at all — refs_collect.go's own comment records that as a
// fixed regression. Keying without it re-opened the same hole one layer up: a resolve widened for
// one ref was served an envelope cached for a DIFFERENT widening, and the compile then failed with
// `candy "..." not in resolved-project envelope`. Measured on distro-omarchy's
// check-omarchy-desktop-vm, where the plugin-wl pin MASKED it — plugin-wl is also reachable
// through pod-hyprland's image closure, so it sat in the unwidened envelope anyway — while the
// plugin-quickshell pin, reachable ONLY through add_candy, was absent from all 167 cached candies.
//
// LocalSuperproject belongs here for the same reason: it applies a CHARLY_REPO_OVERRIDE around the
// resolve, so it decides whether refs resolve from the local superproject or from the fetch cache.
func projectCacheKey(dir string, req spec.ResolvedProjectRequest) (string, string) {
	cfg, err := spec.DefaultDeployConfigPath()
	if err != nil {
		return "", ""
	}
	cachePath := filepath.Join(filepath.Dir(cfg), "cache", "project.json")

	// Sorted before joining: the same scope requested in a different ORDER is the same scan, and
	// must hit the same entry rather than resolving the whole project a second time.
	extra := append([]string(nil), req.ExtraCandyRefs...)
	sort.Strings(extra)
	boxes := append([]string(nil), req.RequestedBoxes...)
	sort.Strings(boxes)
	scope := fmt.Sprintf("extra=%s|boxes=%s|disabled=%t|super=%t",
		strings.Join(extra, ","), strings.Join(boxes, ","),
		req.IncludeDisabled, req.LocalSuperproject)

	data, err := os.ReadFile(filepath.Join(dir, spec.UnifiedFileName))
	if err != nil {
		return cachePath, dir + "|" + scope
	}
	h := sha256.Sum256(data)
	return cachePath, dir + "|" + hex.EncodeToString(h[:]) + "|" + scope
}

// projectCacheEntry is one cached resolve: the resolved project + its resolution time (RFC3339).
type projectCacheEntry struct {
	Resolved string               `json:"resolved"`
	Project  spec.ResolvedProject `json:"project"`
}

// projectCacheFile is the on-disk cache shape: entries keyed by the content key. A MAP rather than
// the single slot it replaces — the key now carries the request's scan scope, so a deploy that
// compiles several add_candy refs holds several live keys, and a single slot would make each one
// evict the last and re-resolve the entire project every time.
type projectCacheFile struct {
	Entries map[string]projectCacheEntry `json:"entries"`
}

// readProjectCache returns the cached resolved project if fresh for key, else (nil, false). A
// corrupt or absent file — or one still in the pre-map shape — is a cache miss.
func readProjectCache(path, key string) (*spec.ResolvedProject, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var cf projectCacheFile
	if json.Unmarshal(data, &cf) != nil {
		return nil, false
	}
	entry, ok := cf.Entries[key]
	if !ok {
		return nil, false
	}
	resolved, err := time.Parse(time.RFC3339, entry.Resolved)
	if err != nil || time.Since(resolved) > projectCacheTTL {
		return nil, false
	}
	return &entry.Project, true
}

// writeProjectCache persists the resolved project under key, KEEPING the other live entries and
// evicting the oldest once the file exceeds projectCacheEntries (best-effort).
func writeProjectCache(path, key string, rp *spec.ResolvedProject) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	cf := projectCacheFile{Entries: map[string]projectCacheEntry{}}
	if existing, err := os.ReadFile(path); err == nil {
		var prev projectCacheFile
		if json.Unmarshal(existing, &prev) == nil && prev.Entries != nil {
			cf.Entries = prev.Entries
		}
	}
	cf.Entries[key] = projectCacheEntry{
		Resolved: time.Now().UTC().Format(time.RFC3339),
		Project:  *rp,
	}
	evictOldestProjectCacheEntries(cf.Entries, projectCacheEntries)
	data, err := json.Marshal(cf)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// evictOldestProjectCacheEntries trims entries to at most max, dropping the oldest first. An
// unparseable timestamp sorts oldest, so a corrupt entry is evicted before a good one.
func evictOldestProjectCacheEntries(entries map[string]projectCacheEntry, max int) {
	for len(entries) > max {
		oldestKey := ""
		var oldest time.Time
		for k, entry := range entries {
			t, err := time.Parse(time.RFC3339, entry.Resolved)
			if err != nil {
				t = time.Time{}
			}
			if oldestKey == "" || t.Before(oldest) {
				oldestKey, oldest = k, t
			}
		}
		delete(entries, oldestKey)
	}
}

// applySelfSuperprojectOverridePlugin is the plugin-side, pure reproduction of charly core's
// (deleted-site) applySelfSuperprojectOverride/selfSuperprojectOverridePair (charly/refs.go): it
// makes this resolve obey the same local-candy rule as a subsequent bed session by pointing the
// remote-ref resolver at the checkout's OWN superproject working tree, when this project is
// checked out as a git submodule. Zero host-only dependency (os/exec + loaderkit.RootRepoIdentity),
// so it is reproduced here rather than round-tripped through a host leg.
func applySelfSuperprojectOverridePlugin(projectDir string) func() {
	pair := selfSuperprojectOverridePairPlugin(projectDir)
	if pair == "" {
		return func() {}
	}
	old, had := os.LookupEnv(repoOverrideEnv)
	_ = os.Setenv(repoOverrideEnv, mergeRepoOverridesPlugin(old, pair))
	return func() {
		if had {
			_ = os.Setenv(repoOverrideEnv, old)
			return
		}
		_ = os.Unsetenv(repoOverrideEnv)
	}
}

// repoOverrideEnv mirrors charly core's RepoOverrideEnv (charly/refs.go) — a plain literal, no
// host-only logic behind it.
const repoOverrideEnv = "CHARLY_REPO_OVERRIDE"

func selfSuperprojectOverridePairPlugin(projectDir string) string {
	out, err := exec.Command("git", "-C", projectDir, "rev-parse", "--show-superproject-working-tree").Output()
	if err != nil {
		return ""
	}
	superDir := strings.TrimSpace(string(out))
	if superDir == "" {
		return ""
	}
	identity := loaderkit.RootRepoIdentity(superDir)
	if identity == "" {
		return ""
	}
	return identity + "=" + superDir
}

func mergeRepoOverridesPlugin(existing, add string) string {
	existing = strings.TrimSpace(existing)
	add = strings.TrimSpace(add)
	switch {
	case existing == "":
		return add
	case add == "":
		return existing
	default:
		return existing + "," + add
	}
}
