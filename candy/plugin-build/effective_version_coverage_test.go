package build

import (
	"testing"

	"github.com/opencharly/sdk/buildkit"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// TestResolvePipeline_LeavesEffectiveVersionUnset is the post-schema-versioning coverage for the
// removal of `deploykit.ComputeEffectiveVersions` (sdk#313). The resolve pipeline `resolveBuildEngine`
// drives — `buildkit.ResolveAllBox` → `deploykit.ComputeIntermediates` → `deploykit.GlobalCandyOrder`
// — MUST leave `spec.ResolvedBox.EffectiveVersion` unset on every box, because the field's only
// writer was the deleted `ComputeEffectiveVersions` and the `ai.opencharly.version` OCI label is no
// longer emitted anywhere. This test FAILS if any step in that chain starts populating the field
// again (e.g. a re-introduced effective-version computation), which is exactly the behaviour the
// removal changed. It deliberately does NOT use `fullResolvedBoxFixture` / the byte-stable golden:
// those hardcode `EffectiveVersion` for the wire-projection completeness assertion and so cannot say
// anything about what the resolve path does.
func TestResolvePipeline_LeavesEffectiveVersionUnset(t *testing.T) {
	// A minimal, candy-free box so the resolve chain runs standalone: ComputeIntermediates /
	// GlobalCandyOrder need a layer for every referenced candy, and this test is about the
	// version field, not candy resolution.
	cfg := &spec.Config{
		Defaults: spec.BoxConfig{
			Base:      "quay.io/fedora/fedora:43",
			Platforms: []string{"linux/amd64"},
			Tag:       "auto",
			Registry:  "ghcr.io/test",
			Build:     []string{"rpm"},
		},
		Box: boxMapOfFixture(map[string]spec.BoxConfig{
			"demo": {Base: "quay.io/fedora/fedora:43"},
		}),
	}
	opts := resolveOptsFixture(spec.ResolveOpts{})

	resolved, err := buildkit.ResolveAllBox(cfg, "test", "", opts)
	if err != nil {
		t.Fatalf("ResolveAllBox: %v", err)
	}
	// Exactly the calls resolveBuildEngine still makes after the removal, in order.
	resolved, err = deploykit.ComputeIntermediates(resolved, map[string]deploykit.CandyModel{}, intermediateDefaults(cfg), "test")
	if err != nil {
		t.Fatalf("ComputeIntermediates: %v", err)
	}
	if _, err := deploykit.GlobalCandyOrder(resolved, map[string]deploykit.CandyModel{}); err != nil {
		t.Fatalf("GlobalCandyOrder: %v", err)
	}

	if len(resolved) == 0 {
		t.Fatal("resolve produced no boxes — fixture regression, test would be vacuous")
	}
	for name, box := range resolved {
		if box.EffectiveVersion != "" {
			t.Errorf("resolved[%q].EffectiveVersion = %q, want empty: the schema-versioning removal deleted the field's sole writer (deploykit.ComputeEffectiveVersions) and the ai.opencharly.version label is never emitted", name, box.EffectiveVersion)
		}
	}
}
