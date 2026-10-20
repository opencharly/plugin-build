package build

import "testing"

// TestBuildPlatforms is the coverage that fails without this change: before it, the solve always used the
// declared list verbatim, so a local build of a box declaring no platforms inherited the base image's whole
// multi-arch manifest list. Measured live: charly was OOM-killed at 5.9 GiB anon-rss building both
// linux/amd64 and linux/arm64 for an image only ever run on the host.
func TestBuildPlatforms(t *testing.T) {
	declared := []string{"linux/amd64", "linux/arm64"}

	// A local build: the drive resolved one host platform. The solve must narrow to it.
	got := buildPlatforms("linux/amd64", declared)
	if len(got) != 1 || got[0] != "linux/amd64" {
		t.Fatalf("local build must narrow to the host platform, got %v", got)
	}

	// A push build: driveConfig.Platform is empty by contract, so the declared/multi-arch list is preserved.
	got = buildPlatforms("", declared)
	if len(got) != len(declared) {
		t.Fatalf("push build must keep the declared platforms, got %v", got)
	}
	for i := range declared {
		if got[i] != declared[i] {
			t.Fatalf("push build must keep the declared platforms in order, got %v", got)
		}
	}

	// A local build with no declared platforms is the case that used to inherit every architecture.
	if got = buildPlatforms("linux/arm64", nil); len(got) != 1 || got[0] != "linux/arm64" {
		t.Fatalf("must narrow even with no declared platforms, got %v", got)
	}
}
