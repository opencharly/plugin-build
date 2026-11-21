package build

import (
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// ensure_base_resolve_test.go — plugin-build#61: `box build debian` (base `debian:13`) waited forever on
// its own image lock, because the external base was never pulled and was mapped onto the box being
// built. These pin the three rules that close it.

func debianProject(registry string) *spec.Config {
	cfg := &spec.Config{Box: spec.BoxMap{
		"debian":         spec.EncodeBox(spec.BoxConfig{Base: "debian:13"}),
		"debian-builder": spec.EncodeBox(spec.BoxConfig{Base: "debian"}),
	}}
	cfg.Defaults.Registry = registry
	return cfg
}

// An external image that is not a box of this project is pulled AS WRITTEN — before, "" skipped the
// pull entirely. A box key still resolves to the box's registry ref.
func TestResolveImageRef_AnExternalImageIsPulledAsWritten(t *testing.T) {
	cfg := debianProject("ghcr.io/opencharly")
	if got := resolveImageRefPlugin("debian:13", cfg); got != "debian:13" {
		t.Fatalf("resolveImageRefPlugin(debian:13) = %q, want it pulled as written", got)
	}
	if got := resolveImageRefPlugin("debian", cfg); !strings.HasPrefix(got, "ghcr.io/opencharly/debian") {
		t.Fatalf("resolveImageRefPlugin(debian) = %q, want the box's own registry ref", got)
	}
}

// The build fallback maps a ref onto a box only when the ref NAMES the box: its bare key or its own
// published repository. An external image that merely shares the leaf is not that box.
func TestBuildableShortName_OnlyARefThatNamesTheBox(t *testing.T) {
	cfg := debianProject("ghcr.io/opencharly")
	cases := map[string]string{
		"debian":                             "debian", // the bare box key
		"ghcr.io/opencharly/debian:2026.1.1": "debian", // the box's own published ref
		"ghcr.io/opencharly/debian":          "debian",
		"debian:13":                          "", // Docker Hub's library/debian, NOT the box
		"docker.io/library/debian:13":        "",
		"quay.io/someone/debian:13":          "",
		"localhost:5000/debian":              "",
	}
	for ref, want := range cases {
		if got := buildableShortNamePlugin(ref, cfg); got != want {
			t.Errorf("buildableShortNamePlugin(%q) = %q, want %q", ref, got, want)
		}
	}
}

// A box can never be its own base — the explicit invariant, which holds even for a registry-less
// project where `debian:13` and the box `debian` share a repository.
func TestSelfBase_ABoxCannotBeItsOwnBase(t *testing.T) {
	cfg := debianProject("")
	fallback := buildableShortNamePlugin("debian:13", cfg)
	if fallback != "debian" {
		t.Fatalf("registry-less fixture: fallback = %q, want the leaf match the guard exists for", fallback)
	}
	err := selfBaseError("debian:13", fallback, "debian")
	if err == nil || !strings.Contains(err.Error(), "cannot be its own base") {
		t.Fatalf("selfBaseError = %v, want the self-base refusal", err)
	}
	if err := selfBaseError("debian", "debian", "debian-builder"); err != nil {
		t.Fatalf("a box based on a DIFFERENT box was refused: %v", err)
	}
	if err := selfBaseError("debian:13", "debian", ""); err != nil {
		t.Fatalf("the build:ensure word (no self) was refused: %v", err)
	}
}
