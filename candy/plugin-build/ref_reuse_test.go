package build

import (
	"strings"
	"testing"

	"github.com/opencharly/sdk/kit"
)

// TestChooseBuiltRef pins the option-B decision table (opencharly/charly#1292):
// reuse the NEWEST local ref of the same repository only when it is the same artifact and
// the store holds it; otherwise mint a fresh CalVer; never replace an explicit --tag or a
// push target's argv-baked ref.
func TestChooseBuiltRef(t *testing.T) {
	const repo = "ghcr.io/opencharly/arch-builder"
	const fullTag = repo + ":2026.331.2000"
	locals := []kit.LocalImageInfo{
		{ID: "older", Names: []string{repo + ":2026.331.1900"}, Created: 100},
		{ID: "newer", Names: []string{repo + ":2026.331.1950"}, Created: 200},
		{ID: "other", Names: []string{"ghcr.io/opencharly/other:2026.331.2359"}, Created: 300},
	}
	storeHasAll := func(string) bool { return true }

	t.Run("reuse the newest ref of the repo when it is the same artifact", func(t *testing.T) {
		ref, reused, prev := chooseBuiltRef(fullTag, "sha256:newer", true, false, locals, storeHasAll)
		if !reused || ref != repo+":2026.331.1950" {
			t.Fatalf("want reuse of the newest same-repo ref, got ref=%q reused=%v prev=%+v", ref, reused, prev)
		}
		if prev.Ref != repo+":2026.331.1950" {
			t.Errorf("prev must name the newest same-repo ref: %+v", prev)
		}
	})

	t.Run("mint when the newest same-repo ref is a different artifact", func(t *testing.T) {
		ref, reused, _ := chooseBuiltRef(fullTag, "sha256:older", true, false, locals, storeHasAll)
		if reused {
			t.Fatalf("an older ref with the same digest must NOT be reused while a newer different one is newest")
		}
		if !strings.HasPrefix(ref, repo+":") || ref == repo+":2026.331.1950" {
			t.Errorf("a changed artifact must mint a fresh ref: %q", ref)
		}
	})

	t.Run("mint when the store does not hold the candidate", func(t *testing.T) {
		ref, reused, _ := chooseBuiltRef(fullTag, "sha256:newer", true, false, locals, func(string) bool { return false })
		if reused || !strings.HasPrefix(ref, repo+":") {
			t.Fatalf("a ref the store does not hold cannot be reused (the next stage's binding needs it): ref=%q reused=%v", ref, reused)
		}
	})

	t.Run("a caller tag is never replaced", func(t *testing.T) {
		const tagged = repo + ":probe-1292"
		ref, reused, _ := chooseBuiltRef(tagged, "sha256:newer", false, false, locals, storeHasAll)
		if reused || ref != tagged {
			t.Fatalf("the caller's tag names the artifact: ref=%q reused=%v", ref, reused)
		}
	})

	t.Run("a push target keeps its argv-baked ref", func(t *testing.T) {
		ref, reused, _ := chooseBuiltRef(fullTag, "sha256:newer", true, true, locals, storeHasAll)
		if reused || ref != fullTag {
			t.Fatalf("a push target's ref is in the buildctl argv: ref=%q reused=%v", ref, reused)
		}
	})

	t.Run("no local ref mints", func(t *testing.T) {
		ref, reused, _ := chooseBuiltRef(fullTag, "sha256:newer", true, false, nil, storeHasAll)
		if reused || !strings.HasPrefix(ref, repo+":") {
			t.Fatalf("no local candidate must mint: ref=%q reused=%v", ref, reused)
		}
	})
}

// TestApplyRefSubstitutions: a child's resolve-time refs are rewritten to the refs the drive
// actually published, and an unrelated repo's ref is untouched.
func TestApplyRefSubstitutions(t *testing.T) {
	cf := "ARG BASE_IMAGE=ghcr.io/opencharly/arch:2026.331.1000\n" +
		"FROM ghcr.io/opencharly/arch-builder:2026.331.1000 AS pixi\n" +
		"FROM quay.io/archlinux/archlinux:base-20260719.0.558306\n"
	got := applyRefSubstitutions(cf, map[string]string{
		"ghcr.io/opencharly/arch:2026.331.1000":         "ghcr.io/opencharly/arch:2026.331.0900",
		"ghcr.io/opencharly/arch-builder:2026.331.1000": "ghcr.io/opencharly/arch-builder:2026.331.0900",
	})
	for _, want := range []string{
		"ARG BASE_IMAGE=ghcr.io/opencharly/arch:2026.331.0900",
		"FROM ghcr.io/opencharly/arch-builder:2026.331.0900 AS pixi",
		"FROM quay.io/archlinux/archlinux:base-20260719.0.558306",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("substituted Containerfile must contain %q:\n%s", want, got)
		}
	}
}

// TestReplaceRefToken_DoesNotCorruptATagPrefix: replacing :probe-1 must not touch :probe-12.
func TestReplaceRefToken_DoesNotCorruptATagPrefix(t *testing.T) {
	in := "a ghcr.io/o/x:probe-1 b ghcr.io/o/x:probe-12 c"
	got := replaceRefToken(in, "ghcr.io/o/x:probe-1", "ghcr.io/o/x:reused")
	want := "a ghcr.io/o/x:reused b ghcr.io/o/x:probe-12 c"
	if got != want {
		t.Fatalf("tag-prefix corruption:\n got %q\nwant %q", got, want)
	}
}

func TestRefRepo(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/opencharly/arch:2026.331.1000": "ghcr.io/opencharly/arch",
		"localhost:5000/opencharly/arch:tag":    "localhost:5000/opencharly/arch",
		"ghcr.io/opencharly/arch":               "ghcr.io/opencharly/arch",
	} {
		if got := refRepo(in); got != want {
			t.Errorf("refRepo(%q) = %q, want %q", in, got, want)
		}
	}
}
