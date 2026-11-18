package build

import (
	"fmt"
	"strings"

	"github.com/opencharly/sdk/buildkit"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// build_tag_scope.go — the two TAG ROLES of one build RESOLVE.
//
// A resolve stamps a tag on every ResolvedBox it produces, and that tag is NOT one value: the
// caller's `--tag` names the ARTIFACT this build produces (the bed's freshness proof depends on
// it), while a box the build only BORROWS keeps the tag its owning repository gave it — a
// builder's name and tag belong to the repo that declares it (DESIGN.md D-KIND-2), so a caller's
// `--tag` has no business renaming another repository's vocabulary node.
//
// The borrowed tag is one value for the whole resolve: buildkit.ComputeCalVer is clock-derived, so
// computing it per box could straddle a minute boundary and split the set across two tags. The
// caller computes it ONCE and hands it here as borrowedTag.

// resolveBoxViewsWithTagScope runs the box resolve, the auto-intermediates, and the two tag roles
// over them, returning the resolved set and the build order: the FULL order for a full build, the
// filtered target order for a named selection. This is the ONE place the roles are applied.
//
// borrowedTag is the CalVer a box's owning repo would give it; targetTag is the caller's `--tag`
// (equal to borrowedTag when the caller gave none, which collapses the two roles onto the one tag
// a bare build has always used). Everything is resolved at borrowedTag first, then the boxes this
// build PRODUCES are stamped with targetTag: resolving at the borrowed tag and promoting the
// output set is what keeps the borrowed nodes untouched by construction rather than by a second
// guess at what each one is.
func resolveBoxViewsWithTagScope(cfg *spec.Config, layers map[string]spec.CandyReader, requested []string, includeDisabled bool, dir, borrowedTag, targetTag string, distroCfg *spec.DistroConfig, builderCfg *spec.BuilderConfig) (map[string]*buildkit.ResolvedBox, []string, error) {
	ropts := boxBuildkitOpts(requested, includeDisabled, distroCfg, builderCfg)
	resolved, err := buildkit.ResolveAllBox(cfg, borrowedTag, dir, ropts)
	if err != nil {
		return nil, nil, err
	}
	// auto-intermediates (pure sdk). An intermediate is a SYNTHETIC cache node the resolve invents,
	// not a node any repo owns, so it starts on the shared CalVer like every other non-output box;
	// the ones on a target's chain are promoted below with the chain they serve.
	resolved, err = deploykit.ComputeIntermediates(resolved, layers, intermediateDefaults(cfg), borrowedTag)
	if err != nil {
		return nil, nil, fmt.Errorf("computing intermediates: %w", err)
	}
	order, err := deploykit.ResolveBoxOrder(resolved, layers)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving box order: %w", err)
	}
	// produced is the set this build PRODUCES: the named targets and their transitive dependency
	// chain, or every resolved box for a full build (the CLI's "" → all enabled).
	produced := make([]string, 0, len(resolved))
	for name := range resolved {
		produced = append(produced, name)
	}
	if len(requested) > 0 {
		order, err = deploykit.FilterBox(order, requested, resolved)
		if err != nil {
			return nil, nil, fmt.Errorf("scoping generation to requested boxes: %w", err)
		}
		produced = order
	}
	stampBuildOutput(resolved, produced, targetTag)
	return resolved, order, nil
}

// stampBuildOutput applies the caller's `--tag` to exactly the boxes this build PRODUCES. A
// borrowed BUILDER is excluded even when the build does rebuild it: its tag belongs to the
// repository that owns it (D-KIND-2), and the drive then resolves the builder by the CalVer it
// was built under — which is also the ref the local store actually holds.
func stampBuildOutput(resolved map[string]*buildkit.ResolvedBox, produced []string, tag string) {
	if tag == "" {
		return
	}
	builders := builderNodeNames(resolved)
	for _, name := range produced {
		if builders[name] {
			continue
		}
		retagBox(resolved[name], tag)
	}
}

// builderNodeNames is the set of boxes this resolve uses as BUILDER images: every box named by some
// box's effective `builder:` map (format → image) or as a `from: builder:` box's
// BootstrapBuilderImage. Producer images declare `produce:`, but a consumer's reference is what
// makes a box a builder for THIS resolve, and it is the reference the emitted Containerfile's
// builder stage resolves by FullTag — so the reference set is the one that must keep its own tag.
func builderNodeNames(resolved map[string]*buildkit.ResolvedBox) map[string]bool {
	names := make(map[string]bool)
	for _, img := range resolved {
		if img == nil {
			continue
		}
		for _, builder := range img.Builder.AllBuilder() {
			if builder != "" {
				names[builder] = true
			}
		}
		if img.BootstrapBuilderImage != "" {
			names[img.BootstrapBuilderImage] = true
		}
	}
	return names
}

// retagBox stamps img with tag, leaving Base/Registry/... alone. FullTag is the canonical
// `<registry>/<name>:<tag>` both resolver producers emit, so it is rewritten by substituting its
// tag SUFFIX rather than re-deriving that format here (R2), and it moves with Tag because it is
// FullTag — not Tag — that the emitted Containerfile's FROM/COPY resolves.
func retagBox(img *buildkit.ResolvedBox, tag string) {
	if img == nil || img.Tag == tag {
		return
	}
	if img.FullTag != "" && img.Tag != "" {
		img.FullTag = strings.TrimSuffix(img.FullTag, ":"+img.Tag) + ":" + tag
	}
	img.Tag = tag
}
