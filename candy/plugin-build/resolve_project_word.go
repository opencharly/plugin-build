package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/buildkit"
	"github.com/opencharly/sdk/kit"
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

// projectCacheEntries bounds the on-disk cache. The key carries the request's scan SCOPE, so one
// project legitimately has several live entries at once — an unwidened `charly status` resolve
// plus one per add_candy: ref a deploy compiles. A handful covers that; the oldest is evicted.
const projectCacheEntries = 16

// projectCacheKey returns the resolved-project cache file + a content key over EVERY input the
// resolve consumes: (1) the top-level charly.yml bytes, (2) the CONTENT of every discovered
// manifest (the discover: roots), (3) the request's scan-widening scope, and (4) the project dir.
//
// (2) is the RCA 2026.257 fix. The discover: roots hold the entities the resolve folds in — for
// eval-omarchy that is pr-beds/**, where the eval lane RENDERS a per-PR bed DURING its run. The
// former key hashed only charly.yml, so once the first lane cached a skeleton envelope (before the
// beds existed), every later lane was served that stale envelope for the 5-minute TTL and its bed
// entity was absent from Deploy[entity] -> `charly check run: no entity "check-omarchy-pr-<N>-vm"`.
// (Reproduced: with pr-beds/ moved away, resolve+status cached 0 pr-beds; restoring 32 bed files
// left the cache still reporting 0 — the charly.yml-only key never saw the tree.) Hashing the
// discovered manifests' bytes means a rendered bed changes the key -> an immediate miss -> the
// fresh resolve sees it. There is no TTL dependency left in the key's correctness.
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

	h := sha256.New()
	if !projectManifestsDigest(h, dir) {
		// The manifest set could not be fully enumerated — the tree cannot be
		// content-addressed, so do NOT cache (an unconditional miss: never read a
		// possibly-stale entry, never write a digest that omits a manifest).
		return "", ""
	}
	return cachePath, dir + "|" + hex.EncodeToString(h.Sum(nil)) + "|" + scope
}

// projectManifestsDigest folds the CONTENT of every charly.yml manifest under the project root
// into the hash (RCA 2026.257). The former key hashed ONLY the top-level file, so a manifest that
// appears or changes DURING a run was invisible to it — the eval lane renders a per-PR bed into
// pr-beds/ mid-run, the first resolve cached a skeleton envelope, and every later resolve was
// served it ("charly check run: no entity").
//
// INTERPRETATION-FREE: it does NOT parse the `discover:` directive or model the resolve's input
// set. It hashes the raw bytes of every manifest file in the tree, using the ONE canonical skip
// rule the discover walk itself uses (kit.DiscoverSkipDir — the same predicate FindEntityDirs
// applies), so it never applies a rule the resolve does not. It is a superset of the resolve's
// LOCAL input (the declared discover roots) plus the top-level file; imported remote repos are
// covered by the top-level file's import pins. Cost is low (measured ~11ms over eval-omarchy's 41
// manifests / 319 KB, against the ~11s resolve it guards).
//
// FAIL-CLOSED: returns false if the tree could not be fully enumerated (a walk/ReadFile error on
// any entry). The caller then treats the project as un-cacheable and resolves fresh, so a manifest
// can never be silently dropped from the digest to produce a stale HIT. The walk uses only
// kit.DiscoverSkipDir — no candy-local path rule.
func projectManifestsDigest(h hash.Hash, dir string) bool {
	ok := true
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Cannot enumerate this entry/subtree: mark un-cacheable and skip it so
			// the walk completes, but the digest will not be used.
			ok = false
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != dir && kit.DiscoverSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != spec.UnifiedFileName {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			ok = false
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			// a manifest the walk located but could not read: un-cacheable, never a
			// digest that looks like the file was absent.
			ok = false
			return nil
		}
		_, _ = h.Write([]byte(rel))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(b)
		_, _ = h.Write([]byte{0})
		return nil
	})
	return ok
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

// readProjectCache returns the cached resolved project for key, else (nil, false). A corrupt or
// absent file — or one still in the pre-map shape — is a cache miss. There is NO time validity:
// the key is a CONTENT address over every resolve input (charly.yml + the discovered manifests +
// the scan scope), so a changed input is a new key -> an immediate miss, and an unchanged input is
// served however old the entry is (the Docker content-address model). The Resolved stamp is kept
// for reclamation ordering only.
//
// FAIL-CLOSED: an empty path or key (the un-enumerable-tree signal from projectCacheKey) is a
// guaranteed miss, so a non-content-addressable tree can never serve a possibly-stale entry.
func readProjectCache(path, key string) (*spec.ResolvedProject, bool) {
	if path == "" || key == "" {
		return nil, false
	}
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
	return &entry.Project, true
}

// writeProjectCache persists the resolved project under key, KEEPING the other live entries and
// evicting the oldest once the file exceeds projectCacheEntries (best-effort).
//
// FAIL-CLOSED: an empty path or key writes NOTHING (the un-enumerable-tree signal — never create a
// cache entry a later read could serve without a content address).
func writeProjectCache(path, key string, rp *spec.ResolvedProject) error {
	if path == "" || key == "" {
		return nil
	}
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
