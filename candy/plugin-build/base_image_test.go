package build

// base_image_test.go — coverage for the base-image ensure at the build seam (charly#949): the drive
// reads the base ref out of the Containerfile it just wrote, and only ever asks the ensure contract
// about a base that could actually need it.

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/opencharly/sdk"
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

// TestEnsureBaseImage_RoutesAMissingBaseThroughTheEnsureContract is the arm round 1 asked for: a
// base that is NOT local, on a drive that DOES hold an executor, must route through the ensure
// contract with the ref, the project dir and the run engine the build will actually use — and a
// failure from it must name the artifact, say it could not be fetched, and say no local build was
// possible. The seam is swapped, so the DECISION is what is asserted; the contract's own behaviour
// is `ensure.go`'s, proven by its own tests and by the live BuildKit run below.
func TestEnsureBaseImage_RoutesAMissingBaseThroughTheEnsureContract(t *testing.T) {
	const ref = "ghcr.io/opencharly/not-in-any-store:2026.288.0854"
	orig := ensureBase
	defer func() { ensureBase = orig }()

	var got struct {
		called bool
		image  string
		dir    string
		engine string
		ex     *sdk.Executor
	}
	ensureBase = func(ctx context.Context, ex *sdk.Executor, image, dir, runEngine string) error {
		got.called, got.image, got.dir, got.engine, got.ex = true, image, dir, runEngine, ex
		return errors.New("no recipe in this project, and the registry refused")
	}

	ex := &sdk.Executor{}
	c := driveConfig{RunEngine: "podman", Dir: "/project/dir", Ex: ex}
	cf := "ARG BASE_IMAGE=" + ref + "\n\nFROM ${BASE_IMAGE}\n"

	err := c.ensureBaseImage(context.Background(), cf)
	if !got.called {
		t.Fatalf("a missing base with an executor present must reach the ensure contract; it did not")
	}
	if got.image != ref {
		t.Errorf("ensure contract got image %q, want the Containerfile's base %q", got.image, ref)
	}
	if got.dir != "/project/dir" {
		t.Errorf("ensure contract got dir %q, want the drive's project dir %q", got.dir, "/project/dir")
	}
	if got.engine != "podman" {
		t.Errorf("ensure contract got run engine %q, want the drive's %q", got.engine, "podman")
	}
	if got.ex != ex {
		t.Errorf("ensure contract got a different executor than the drive holds")
	}
	if err == nil {
		t.Fatalf("a failed ensure must fail the build; got nil")
	}
	msg := err.Error()
	for _, want := range []string{ref, "could not be fetched", "no local build was possible"} {
		if !strings.Contains(msg, want) {
			t.Errorf("failure %q must contain %q — it must name the artifact, the fetch, and the missing fallback", msg, want)
		}
	}
}

// TestEnsureBaseSeamIsWired guards the init() assignment: a nil seam would make every cold build
// panic instead of ensuring its base, so the production wiring is asserted rather than assumed.
func TestEnsureBaseSeamIsWired(t *testing.T) {
	if ensureBase == nil {
		t.Fatal("ensureBase seam is nil — the init() assignment is the only thing wiring the ensure contract")
	}
}
