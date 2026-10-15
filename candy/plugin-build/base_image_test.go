package build

// base_image_test.go — coverage for the base-image ensure at the build seam (charly#949): the drive
// reads the base ref out of the Containerfile it just wrote, and only ever asks the ensure contract
// about a base that could actually need it.

import (
	"context"
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
