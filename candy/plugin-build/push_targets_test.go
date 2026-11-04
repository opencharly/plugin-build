package build

// push_targets_test.go — the push-policy guard for opencharly/.github#279.
//
// A `--push` build resolves the requested box AND its transitive dependencies. When a
// dependency is an `import:` namespace (`crabbox.pod-crabbox`, the CachyOS `arch` under it), the
// drive BUILDS it because the build needs it — but that box's package belongs to the repo that
// sources it. Pushing it with THIS repo's GITHUB_TOKEN is refused with
// `denied: permission_denied: write_package`, and the whole build fails before the requested
// image is published (measured on pod-crabbox-systemd, run 41895678841).
//
// The push set is the requested box plus this project's own auto-intermediates, never an
// unrequested imported base.

import (
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// pushPolicyReply is the resolved chain shape the failing build produced: the imported CachyOS
// base, the imported lease base, a locally synthesized intermediate, and the requested box.
func pushPolicyReply() spec.BuildResolveReply {
	return spec.BuildResolveReply{
		Boxes: []spec.BuildResolveBox{
			{Name: "crabbox.cachyos.arch.arch", FullTag: "ghcr.io/opencharly/arch:2026.308.1917"},
			{Name: "crabbox.pod-crabbox", FullTag: "ghcr.io/opencharly/pod-crabbox:2026.308.1917"},
			{Name: "pod-crabbox-systemd-base", FullTag: "ghcr.io/opencharly/pod-crabbox-systemd-base:2026.308.1917"},
			{Name: "pod-crabbox-systemd", FullTag: "ghcr.io/opencharly/pod-crabbox-systemd:2026.308.1917"},
		},
		ResolvedProject: &spec.ResolvedProject{
			Boxes: map[string]spec.ResolvedBoxView{
				"crabbox.cachyos.arch.arch": {Name: "crabbox.cachyos.arch.arch"},
				"crabbox.pod-crabbox":       {Name: "crabbox.pod-crabbox"},
				"pod-crabbox-systemd-base":  {Name: "pod-crabbox-systemd-base", Auto: true},
				"pod-crabbox-systemd":       {Name: "pod-crabbox-systemd"},
			},
		},
	}
}

func TestPushTargets_FilteredBuildPublishesRequestedAndOwnIntermediates(t *testing.T) {
	got := pushTargets(spec.BuildRequest{Boxes: []string{"pod-crabbox-systemd"}, Push: true}, pushPolicyReply())

	for _, want := range []string{"pod-crabbox-systemd", "pod-crabbox-systemd-base"} {
		if !got[want] {
			t.Errorf("push targets must include %q, got %v", want, got)
		}
	}
	for _, foreign := range []string{"crabbox.cachyos.arch.arch", "crabbox.pod-crabbox"} {
		if got[foreign] {
			t.Errorf("push targets must NOT include imported base %q — this repo's token cannot write its package, got %v", foreign, got)
		}
	}
}

func TestPushTargets_FullBuildSkipsImportedBases(t *testing.T) {
	got := pushTargets(spec.BuildRequest{Push: true}, pushPolicyReply())

	for _, want := range []string{"pod-crabbox-systemd", "pod-crabbox-systemd-base"} {
		if !got[want] {
			t.Errorf("a full --push build must publish this project's box %q, got %v", want, got)
		}
	}
	for _, foreign := range []string{"crabbox.cachyos.arch.arch", "crabbox.pod-crabbox"} {
		if got[foreign] {
			t.Errorf("a full --push build must NOT publish imported base %q, got %v", foreign, got)
		}
	}
}

func TestPushTargets_ExplicitRequestIsNeverDropped(t *testing.T) {
	// An EXPLICIT request is an instruction: the drive still attempts the push, so a foreign
	// request fails loudly at the registry rather than being silently skipped.
	got := pushTargets(spec.BuildRequest{Boxes: []string{"crabbox.pod-crabbox"}, Push: true}, pushPolicyReply())
	if !got["crabbox.pod-crabbox"] {
		t.Fatalf("an explicitly requested box must remain a push target, got %v", got)
	}
}

func TestPushTargets_NoPushFlagMeansNoTargets(t *testing.T) {
	if got := pushTargets(spec.BuildRequest{Boxes: []string{"pod-crabbox-systemd"}}, pushPolicyReply()); len(got) != 0 {
		t.Fatalf("a build without --push must have no push targets, got %v", got)
	}
}

// TestPushOutputFor_PublishesOnlyTargets pins the exact argv the drive appends for the registry
// exporter. This is the seam buildctl receives: a target gets the one `--output
// type=image,...,push=true` pair; an imported base gets NOTHING; and no --push means NOTHING.
func TestPushOutputFor_PublishesOnlyTargets(t *testing.T) {
	reply := pushPolicyReply()
	c := driveConfig{
		Push:        true,
		PushTargets: pushTargets(spec.BuildRequest{Boxes: []string{"pod-crabbox-systemd"}, Push: true}, reply),
	}

	requested := reply.Boxes[3]
	got := c.pushOutputFor(requested)
	if len(got) != 2 || got[0] != "--output" {
		t.Fatalf("the requested box must get one registry exporter pair, got %q", got)
	}
	want := "type=image,name=" + requested.FullTag + ",push=true"
	if got[1] != want {
		t.Errorf("registry exporter: got %q want %q", got[1], want)
	}

	imported := reply.Boxes[0] // crabbox.cachyos.arch.arch
	if got := c.pushOutputFor(imported); got != nil {
		t.Errorf("an imported base must get NO registry exporter (its package is another repo's), got %q", got)
	}

	noPush := driveConfig{Push: false, PushTargets: map[string]bool{requested.Name: true}}
	if got := noPush.pushOutputFor(requested); got != nil {
		t.Errorf("without --push there must be no registry exporter, got %q", got)
	}
}

// TestPushTargets_DottedIntermediateIsForeign — an auto-intermediate synthesized under an
// imported namespace keeps that namespace prefix; it is built here but has no local package
// owner, so it is not a push target either.
func TestPushTargets_DottedIntermediateIsForeign(t *testing.T) {
	reply := spec.BuildResolveReply{
		Boxes: []spec.BuildResolveBox{{Name: "crabbox.pod-crabbox-base"}},
		ResolvedProject: &spec.ResolvedProject{Boxes: map[string]spec.ResolvedBoxView{
			"crabbox.pod-crabbox-base": {Name: "crabbox.pod-crabbox-base", Auto: true},
		}},
	}
	got := pushTargets(spec.BuildRequest{Boxes: []string{"pod-crabbox-systemd"}, Push: true}, reply)
	for name := range got {
		if strings.ContainsRune(name, '.') {
			t.Fatalf("push targets must not contain a foreign intermediate, got %q", name)
		}
	}
}
