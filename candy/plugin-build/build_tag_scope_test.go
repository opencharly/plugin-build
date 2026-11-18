package build

import (
	"strings"
	"testing"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// build_tag_scope_test.go — the regression guard for the two TAG ROLES of one build resolve.
//
// A caller's `--tag` names the ARTIFACT a build produces; it is not a rename of every node the
// resolve happens to touch. So a borrowed builder — a vocabulary node owned by the distro repo
// that declares it (DESIGN.md D-KIND-2) — and any box the build does not produce keeps the CalVer
// its owning repo gave it. Pre-fix, every box in the resolve carried the caller's tag, so
// `charly box build comfyui-cdi --tag check-comfyui-pod-<calver>` made the drive resolve
// `ghcr.io/opencharly/arch-builder:check-comfyui-pod-<calver>` — a ref no registry serves — instead
// of the `arch-builder:<its own CalVer>` already in the store.
//
// The named target's tag is load-bearing the other way: `charly check run` builds the bed image
// with the run's tag and verifies the artifact BY that tag (opencharly/plugin-check#115), so a fix
// that simply drops the override would break the bed's freshness proof instead.

const (
	tagScopeBorrowed = "2026.322.1943"
	tagScopeTarget   = "check-comfyui-pod-2026.322.1943"
)

// tagScopeFixture is the smallest project that exercises BOTH roles at once:
//
//	arch           the distro base
//	arch-builder   the builder every pac-format image borrows (a BORROWED node)
//	app            the box the request NAMES to build, on arch, borrowing arch-builder
//	sibling        a second consumer of arch → the pair makes an auto-intermediate on app's chain
//	alpha / beta   a sibling group NOTHING in this build names → its intermediate is borrowed too
func tagScopeFixture() *spec.Config {
	const archBase = "quay.io/archlinux/archlinux:base-20260719.0.558306"
	return &spec.Config{
		Defaults: spec.BoxConfig{Registry: "ghcr.io/opencharly"},
		Box: boxMapOfFixture(map[string]spec.BoxConfig{
			"arch": {
				Base: archBase, Build: []string{"pac"}, Distro: []string{"arch"},
				Builder: spec.BuilderMap{"pixi": "arch-builder"},
			},
			"arch-builder": {
				Base: archBase, Build: []string{"pac"}, Distro: []string{"arch"},
				Produce: []string{"pixi"},
			},
			"app": {
				Base: "arch", Build: []string{"pac"}, Distro: []string{"arch"},
				Builder: spec.BuilderMap{"pixi": "arch-builder"},
				Candy:   []string{"a", "b"},
			},
			"sibling": {
				Base: "arch", Build: []string{"pac"}, Distro: []string{"arch"},
				Builder: spec.BuilderMap{"pixi": "arch-builder"},
				Candy:   []string{"a", "c"},
			},
			"alpha": {
				Base: "quay.io/fedora/fedora:43", Build: []string{"rpm"}, Distro: []string{"fedora"},
				Candy: []string{"a", "b"},
			},
			"beta": {
				Base: "quay.io/fedora/fedora:43", Build: []string{"rpm"}, Distro: []string{"fedora"},
				Candy: []string{"a", "c"},
			},
		}),
	}
}

// tagScopeLayers is the candy set the fixture composes. `b` carries a package section, which is
// what makes its consumers genuinely need the builder — so `arch-builder` lands in the build order
// and the assertion below is about the tag SPLIT, not about the builder merely being absent.
func tagScopeLayers() map[string]spec.CandyReader {
	mk := func(name string, top []string) spec.CandyReader {
		return deploykit.NewSpecCandyModel(
			spec.CandyModel{Name: name, TopPackages: top},
			spec.CandyView{Name: name},
		)
	}
	return map[string]spec.CandyReader{
		"a": mk("a", nil),
		"b": mk("b", []string{"jq"}),
		"c": mk("c", nil),
	}
}

// TestBuildTagScope_BorrowedNodesKeepTheirOwnersTag is the regression guard: with a non-empty
// override, the boxes the build PRODUCES carry it and the borrowed ones do not.
func TestBuildTagScope_BorrowedNodesKeepTheirOwnersTag(t *testing.T) {
	resolved, order, err := resolveBoxViewsWithTagScope(
		tagScopeFixture(), tagScopeLayers(),
		[]string{"app"}, false, "",
		tagScopeBorrowed, tagScopeTarget,
		&spec.DistroConfig{}, &spec.BuilderConfig{},
	)
	if err != nil {
		t.Fatalf("resolveBoxViewsWithTagScope: %v", err)
	}

	// The target the request names — the artifact the bed proves freshness by.
	assertBoxTag(t, resolved, "app", tagScopeTarget)
	// Its OWN chain: the parent base and the auto-intermediate the pair with `sibling` created.
	assertBoxTag(t, resolved, "arch", tagScopeTarget)
	assertBoxTag(t, resolved, "arch-a", tagScopeTarget)

	// BORROWED: the builder's name and tag belong to the repository that owns it (D-KIND-2),
	// even though THIS build rebuilds it — renaming it is what made the drive pull a ref no
	// registry serves.
	assertBoxTag(t, resolved, "arch-builder", tagScopeBorrowed)

	// BORROWED: a sibling group this build never names — not this build's output.
	assertBoxTag(t, resolved, "fedora-a", tagScopeBorrowed)
	assertBoxTag(t, resolved, "sibling", tagScopeBorrowed)

	// The order is the filtered build set, so the builder really is produced here.
	want := map[string]bool{"app": true, "arch": true, "arch-a": true, "arch-builder": true}
	for _, name := range order {
		if !want[name] {
			t.Errorf("filtered order carries %q, which this build does not produce (order: %v)", name, order)
		}
		delete(want, name)
	}
	for name := range want {
		t.Errorf("filtered order is missing %q (order: %v)", name, order)
	}
}

// TestBuildTagScope_FullBuildStillHonoursTheOverride keeps the CLI contract: with no named target
// a build produces every box, so `--tag` still stamps them — but a builder node is never renamed.
func TestBuildTagScope_FullBuildStillHonoursTheOverride(t *testing.T) {
	resolved, _, err := resolveBoxViewsWithTagScope(
		tagScopeFixture(), tagScopeLayers(),
		nil, false, "",
		tagScopeBorrowed, tagScopeTarget,
		&spec.DistroConfig{}, &spec.BuilderConfig{},
	)
	if err != nil {
		t.Fatalf("resolveBoxViewsWithTagScope: %v", err)
	}
	assertBoxTag(t, resolved, "app", tagScopeTarget)
	assertBoxTag(t, resolved, "sibling", tagScopeTarget)
	assertBoxTag(t, resolved, "arch-builder", tagScopeBorrowed)
}

// TestBuildTagScope_NoOverrideLeavesEveryNodeOnTheSharedTag is the bare-build arm: with no
// `--tag` the two roles collapse onto the ONE CalVer, so the resolve is unchanged.
func TestBuildTagScope_NoOverrideLeavesEveryNodeOnTheSharedTag(t *testing.T) {
	resolved, _, err := resolveBoxViewsWithTagScope(
		tagScopeFixture(), tagScopeLayers(),
		[]string{"app"}, false, "",
		tagScopeBorrowed, tagScopeBorrowed,
		&spec.DistroConfig{}, &spec.BuilderConfig{},
	)
	if err != nil {
		t.Fatalf("resolveBoxViewsWithTagScope: %v", err)
	}
	for _, name := range []string{"app", "arch", "arch-a", "arch-builder"} {
		assertBoxTag(t, resolved, name, tagScopeBorrowed)
	}
}

// assertBoxTag pins BOTH tag-bearing fields: FullTag is what the emitted Containerfile's
// FROM/COPY names, so a fix that moved Tag without FullTag (or the reverse) would still fail live.
func assertBoxTag(t *testing.T, resolved map[string]*deploykit.ResolvedBox, name, want string) {
	t.Helper()
	img := resolved[name]
	if img == nil {
		t.Fatalf("box %q absent from the resolved set", name)
	}
	if img.Tag != want {
		t.Errorf("%s.Tag = %q, want %q", name, img.Tag, want)
	}
	if !strings.HasSuffix(img.FullTag, ":"+want) {
		t.Errorf("%s.FullTag = %q, want it to end with %q", name, img.FullTag, ":"+want)
	}
}
