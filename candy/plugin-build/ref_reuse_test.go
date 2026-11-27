package build

import (
	"fmt"
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

// TestMintAutoRef — minting a NEW artifact must never overwrite a tag a different artifact
// already owns: two changed builds inside one CalVer minute produce `HHMM` and `HHMM-2`
// (the churn the reuse predicate exists to stop, in its worst form: a tag silently
// re-pointed at different bytes).
func TestMintAutoRef(t *testing.T) {
	const repo = "ghcr.io/opencharly/arch"
	const base = "2026.331.1207"
	free := func(string) bool { return false }
	const newID = "sha256:new"

	if got, want := mintAutoRef(repo, base, newID, nil, free), repo+":"+base; got != want {
		t.Errorf("a free minute mints the bare CalVer: got %q want %q", got, want)
	}

	taken := []kit.LocalImageInfo{{ID: "sha256:aaa", Names: []string{repo + ":" + base}}}
	if got, want := mintAutoRef(repo, base, newID, taken, free), repo+":"+base+"-2"; got != want {
		t.Errorf("a minute taken by a DIFFERENT artifact bumps: got %q want %q", got, want)
	}

	taken2 := append(taken, kit.LocalImageInfo{ID: "sha256:bbb", Names: []string{repo + ":" + base + "-2"}})
	if got, want := mintAutoRef(repo, base, newID, taken2, free), repo+":"+base+"-3"; got != want {
		t.Errorf("a taken suffix bumps further: got %q want %q", got, want)
	}

	same := []kit.LocalImageInfo{{ID: newID, Names: []string{repo + ":" + base}}}
	if got, want := mintAutoRef(repo, base, newID, same, free), repo+":"+base; got != want {
		t.Errorf("the tag already names THIS artifact: got %q want %q", got, want)
	}

	if got, want := mintAutoRef(repo, base, newID, nil, func(ref string) bool { return ref == repo+":"+base }),
		repo+":"+base+"-2"; got != want {
		t.Errorf("a tag the STORE holds is taken: got %q want %q", got, want)
	}

	// Every numeric slot occupied: fall back to a content suffix rather than overwrite.
	var all []kit.LocalImageInfo
	for n := 1; n <= 99; n++ {
		tag := base
		if n > 1 {
			tag = fmt.Sprintf("%s-%d", base, n)
		}
		all = append(all, kit.LocalImageInfo{ID: fmt.Sprintf("sha256:%02d", n), Names: []string{repo + ":" + tag}})
	}
	if got, want := mintAutoRef(repo, base, newID, all, free), repo+":"+base+"-new"; got != want {
		t.Errorf("an exhausted minute must mint the content suffix instead of overwriting: got %q want %q", got, want)
	}
}

// TestIsAutoTag — the drive's own tag grammar, now including the collision suffix. A caller
// tag (a bed/release/user name) must never be mistaken for one, or a build would replace the
// ref the caller named.
func TestIsAutoTag(t *testing.T) {
	for tag, want := range map[string]bool{
		"2026.331.1207":                     true,
		"2026.331.1207-2":                   true,
		"2026.331.1207-12":                  true,
		"check-openwebui-pod-2026.331.0504": false,
		"probe-1292":                        false,
		"2026.331.1207-x":                   false,
		"2026.331.1207-":                    false,
		"2026.331.120":                      false,
		"release-2026.331.1207":             false,
	} {
		if got := isAutoTag("ghcr.io/opencharly/arch:" + tag); got != want {
			t.Errorf("isAutoTag(%q) = %v, want %v", tag, got, want)
		}
	}
}
