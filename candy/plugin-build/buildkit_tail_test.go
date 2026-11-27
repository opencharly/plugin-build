package build

import (
	"context"

	"errors"
	"github.com/opencharly/sdk/buildkitdrive"
	"strings"
	"testing"
)

// TestTailLines is the coverage for opencharly/charly#1255: a failed build must carry the
// captured command output in its error instead of only `exit status 1`. tailLines is what keeps
// that bounded; if it truncates from the wrong end, the failing STEP (the last lines) is lost and
// the fix proves nothing.
func TestTailLines(t *testing.T) {
	cases := []struct {
		name, out string
		n         int
		want      string
	}{
		{"empty", "", 10, ""},
		{"short", "#1 a\n#2 b\n", 10, "#1 a\n#2 b"},
		{"exact", "a\nb", 2, "a\nb"},
		{"tail", "a\nb\nc\nd", 2, "c\nd"},
		{"tail-keeps-the-error-line", "#13 RUN x\n#13 ERROR: pacman failed\n", 1, "#13 ERROR: pacman failed"},
	}
	for _, c := range cases {
		if got := tailLines(c.out, c.n); got != c.want {
			t.Fatalf("%s: tailLines(%q,%d) = %q, want %q", c.name, c.out, c.n, got, c.want)
		}
	}
	if got := tailLines("only", 1); strings.HasSuffix(got, "\n") {
		t.Fatalf("trailing newline leaked into the error: %q", got)
	}
}

// TestBuildFailure_CarriesTheCapturedOutput is the test the review asked for: it pins the WIRING
// (buildFailure is what the call site uses), so deleting the output from the error fails here even
// if tailLines itself stays green.
func TestBuildFailure_CarriesTheCapturedOutput(t *testing.T) {
	err := buildFailure("comfyui-cdi", errors.New("exit status 1"), "#12 [stage-4 5/12] RUN pacman -Syu\n#12 ERROR: process did not complete: exit code: 1\n")
	if !strings.Contains(err.Error(), "#12 ERROR: process did not complete") {
		t.Fatalf("the failed step's own output is not in the error: %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("the underlying error is not in the message: %v", err)
	}
	// empty output must not leave a dangling newline (the review's non-blocking note)
	plain := buildFailure("x", errors.New("boom"), "")
	if strings.HasSuffix(plain.Error(), "\n") {
		t.Fatalf("empty output left a trailing newline: %q", plain.Error())
	}
}

// failingBuildctlRunner fails ONLY the buildctl command, returning captured output — the seam the
// review asked for. Everything else succeeds, so buildImage reaches the line that must pass `out`.
type failingBuildctlRunner struct{ out string }

func (f failingBuildctlRunner) Run(_ context.Context, name string, _ ...string) (string, error) {
	if name == buildkitdrive.BuildctlBinary {
		return f.out, errors.New("exit status 1")
	}
	return "", nil
}

// TestRunBuildctl_PutsTheCapturedOutputInTheError exercises the CALL SITE that used to discard the
// output: buildImage's build call now goes through runBuildctl, and reverting that to
// `if _, err := runner.Run(...)` makes this test fail while buildFailure/tailLines stay green.
func TestRunBuildctl_PutsTheCapturedOutputInTheError(t *testing.T) {
	prev := buildRunner
	buildRunner = failingBuildctlRunner{out: "#12 [stage-4 5/12] RUN pacman -Syu\\n#12 ERROR: pacman failed\\n"}
	defer func() { buildRunner = prev }()

	err := (driveConfig{}).runBuildctl(context.Background(), "comfyui-cdi", []string{"build"})
	if err == nil {
		t.Fatal("runBuildctl returned nil although buildctl failed")
	}
	if !strings.Contains(err.Error(), "#12 ERROR: pacman failed") {
		t.Fatalf("the call site dropped the captured output: %v", err)
	}
}
