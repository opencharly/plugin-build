package build

// base_image_test.go — coverage for the base-image ensure at the build seam (charly#949): the drive
// reads the base ref out of the Containerfile it just wrote, and only ever asks the ensure contract
// about a base that could actually need it.

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestBaseImageRef reads the exact string BuildKit will resolve out of the authored artifact — the
// reason no new field has to carry the base across the resolve/build boundary.
func TestBaseImageRef(t *testing.T) {
	cf := "ARG BASE_IMAGE=ghcr.io/opencharly/arch:2026.288.0854\n\nFROM ${BASE_IMAGE}\n\n"
	if got, want := baseImageRef(cf), "ghcr.io/opencharly/arch:2026.288.0854"; got != want {
		t.Errorf("baseImageRef = %q, want %q", got, want)
	}
	if got := baseImageRef("FROM scratch\n"); got != "" {
		t.Errorf("a Containerfile with no base ARG must read as empty, got %q", got)
	}
}

// TestEnsureBaseImage_NoOpForScratchAndEmpty pins the two bases that are nobody's business to
// ensure: no ARG at all, and `scratch`. Neither may reach the engine or the executor — so this
// runs against a zero driveConfig, which is also the proof that the no-op path cannot panic.
func TestEnsureBaseImage_NoOpForScratchAndEmpty(t *testing.T) {
	var c driveConfig // no RunEngine, no Ex
	for _, cf := range []string{"", "FROM scratch\n", "ARG BASE_IMAGE=scratch\n\nFROM ${BASE_IMAGE}\n"} {
		if err := c.ensureBaseImage(context.Background(), cf); err != nil {
			t.Errorf("ensureBaseImage(%q) = %v, want nil (nothing to ensure)", cf, err)
		}
	}
}

// TestEnsureBaseImage_RefusesWithoutAnExecutor pins the THIRD arm — a base that is not local, on a
// drive that holds no executor. `build:box` always constructs one (`Ex: ex`), so this is the guard
// that a drive built without it FAILS LOUDLY instead of silently leaving the base unfetched and
// letting BuildKit die with the registry's own auth error — the exact failure charly#949 is about.
// It must NAME the ref, say it is not present, and say why it cannot be resolved here.
func TestEnsureBaseImage_RefusesWithoutAnExecutor(t *testing.T) {
	const ref = "ghcr.io/opencharly/does-not-exist:2026.288.0854"
	cf := "ARG BASE_IMAGE=" + ref + "\n\nFROM ${BASE_IMAGE}\n"
	c := driveConfig{RunEngine: "podman"} // no Ex — the arm under test

	err := c.ensureBaseImage(context.Background(), cf)
	if err == nil {
		t.Fatalf("ensureBaseImage(%q) on a drive with no executor = nil, want a refusal naming the base", ref)
	}
	msg := err.Error()
	for _, want := range []string{ref, "not present locally", "no executor"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q must contain %q — it names the artifact, the state and the reason", msg, want)
		}
	}
}

// TestEnsureBaseImage_PresentBaseIsANoOp is the arm a warm host takes on EVERY build: a base the run
// engine already holds must return before the executor is consulted — so it runs with a nil Ex, and
// a lookup that reached for the executor would fail this test rather than pass it.
func TestEnsureBaseImage_PresentBaseIsANoOp(t *testing.T) {
	// Take a reference the RUN ENGINE really holds, rather than assuming one: the arm is "the base is
	// already local", and inventing a ref would test the refusal arm instead (a check that cannot
	// fail the way it claims proves nothing).
	out, err := exec.Command("podman", "images", "--format", "{{.Repository}}:{{.Tag}}").Output()
	if err != nil {
		t.Skipf("podman cannot list the run engine's store here — skipping the warm-store arm: %v", err)
	}
	ref := ""
	for _, line := range strings.Split(string(out), "\n") {
		if r := strings.TrimSpace(line); r != "" && !strings.Contains(r, "<none>") {
			ref = r
			break
		}
	}
	if ref == "" {
		t.Skip("the run engine's store holds no tagged image — skipping the warm-store arm")
	}
	cf := "ARG BASE_IMAGE=" + ref + "\n\nFROM ${BASE_IMAGE}\n"
	c := driveConfig{RunEngine: "podman"} // no Ex: a lookup that reached the executor would fail here
	if err := c.ensureBaseImage(context.Background(), cf); err != nil {
		t.Errorf("ensureBaseImage(%q) with the base already local = %v, want nil (present is a no-op)", ref, err)
	}
}
