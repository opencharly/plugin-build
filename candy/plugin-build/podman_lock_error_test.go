package build

import (
	"errors"
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// TestImageBuildLockError_NamesBoxTagAndHolder pins the contract of the per-image build-lock
// failure: it must carry WHICH box, WHICH lock key (the tag-stripped image ref), and the
// primitive's own holder-naming text, and it must keep that error WRAPPED (errors.Is/As still
// reach it). The queue/report/holder semantics themselves live in spec/lock (opencharly/spec
// #132); this test pins the half plugin-build owns — the composed message an operator actually
// reads.
func TestImageBuildLockError_NamesBoxTagAndHolder(t *testing.T) {
	box := spec.BuildResolveBox{Name: "githubrunner", FullTag: "ghcr.io/opencharly/githubrunner:2026.256.0655"}
	// Verbatim SHAPE of the primitive's timeout error: spec/lock names the holder from the kernel
	// (/proc fdinfo) rather than from any lock-file bytes (see opencharly/spec#132).
	inner := errors.New("flock /home/u/.cache/charly/locks/image-ab5c861f713c0ae3.lock: still held by pid 4242 (/usr/local/bin/charly box build cachyos) after 30m0s — that process is still running; wait for it to finish and retry (the kernel releases the lock when it exits, so there is nothing to clean up)")

	err := imageBuildLockError(box, inner)
	msg := err.Error()
	for _, want := range []string{"githubrunner", box.FullTag, "still held by pid 4242", "wait for it to finish and re-run"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("build-lock error = %q; want it to contain %q", msg, want)
		}
	}
	if !errors.Is(err, inner) {
		t.Fatalf("the primitive's error must stay wrapped (errors.Is must reach it): %v", err)
	}
	t.Logf("composed message: %v", err)
}
