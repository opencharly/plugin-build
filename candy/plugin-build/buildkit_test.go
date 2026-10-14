package build

import (
	"strings"
	"testing"

	"github.com/opencharly/sdk/buildkitdrive"
	"github.com/opencharly/spec/spec"
)

// TestPushOutput — the drive's one compositional piece, pinned in both directions.
//
// A --push build must reach the registry (BuildKit's image exporter, the single push path that
// replaces buildx and `podman manifest push`) AND keep the local export, because the local load is
// what keeps the inline merge and every label read working. The kit owns the local export, so this
// asserts the drive adds exactly the registry destination — and that it does NOT duplicate the OCI
// export the kit already emitted (a duplicate `--output type=oci` would be a silent second write of
// the same archive, and it is the mistake this test exists to make impossible).
func TestPushOutput(t *testing.T) {
	got := pushOutput("ghcr.io/opencharly/fedora:2026.287.1200")
	if len(got) != 2 || got[0] != "--output" {
		t.Fatalf("pushOutput must be one --output pair, got %q", got)
	}
	if want := "type=image,name=ghcr.io/opencharly/fedora:2026.287.1200,push=true"; got[1] != want {
		t.Errorf("push exporter: got %q want %q", got[1], want)
	}
	if strings.Contains(got[1], "type=oci") {
		t.Errorf("the registry exporter must not carry the local export: %q", got[1])
	}

	// And the composed argv carries the OCI export EXACTLY once, whatever the push flag says.
	b := BuildForTest()
	args, err := b.Args("/stage/out.tar")
	if err != nil {
		t.Fatalf("Args: %v", err)
	}
	for _, push := range []bool{false, true} {
		composed := append([]string{}, args...)
		if push {
			composed = append(composed, pushOutput(b.Ref)...)
		}
		if n := strings.Count(strings.Join(composed, " "), "type=oci,dest="); n != 1 {
			t.Errorf("push=%v: the OCI export must appear exactly once, found %d in %q", push, n, composed)
		}
		if n := strings.Count(strings.Join(composed, " "), "push=true"); n != map[bool]int{false: 0, true: 1}[push] {
			t.Errorf("push=%v: push=true count is %d", push, n)
		}
	}
}

// BuildForTest is a minimal valid build for the argv assertions above.
func BuildForTest() buildkitdrive.Build {
	return buildkitdrive.Build{
		Containerfile: "/ctx/Containerfile",
		Context:       "/ctx",
		Ref:           "localhost/box:2026.287.1200",
	}
}

// imageTags is CalVer-only: exactly the box's full tag, never :latest.
func TestImageTags_CalVerOnly(t *testing.T) {
	got := imageTags(spec.BuildResolveBox{FullTag: "ghcr.io/opencharly/arch:2026.287.1200"})
	if len(got) != 1 || got[0] != "ghcr.io/opencharly/arch:2026.287.1200" {
		t.Fatalf("imageTags must be exactly the CalVer tag, got %q", got)
	}
	for _, tag := range got {
		if strings.HasSuffix(tag, ":latest") {
			t.Errorf("charly never emits :latest, got %q", tag)
		}
	}
}
