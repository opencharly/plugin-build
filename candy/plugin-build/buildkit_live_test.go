package build

// buildkit_live_test.go — the LIVE proof of the changed path: a real BuildKit daemon, a real
// `buildctl`, and a real image bridged into the run engine's store.
//
// ⚑ Why this lives in the module rather than as an assembled `charly box build`: the plugin is
// COMPILED INTO charly, so an assembled build couples this proof to every other pinned plugin's
// release — the assembled build answers "does the whole product assemble", while this proof
// answers "does THIS drive work". So the drive is driven directly here: `driveConfig.buildImage`
// is the whole changed path — daemon ensure, the buildctl argv, the OCI export, and the bridge
// (annotate → store → load → verify) — and it is invoked exactly as `runBoxBuild` invokes it.
//
// It SKIPS VISIBLY when the toolchain is absent (R8), and is never mocked: where the tools exist
// it runs a real build.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/sdk/buildkitdrive"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/container"
	"github.com/opencharly/spec/spec"
)

func TestBuildImage_LiveBuildsAndBridgesIntoTheRunEngine(t *testing.T) {
	if testing.Short() {
		t.Skip("live build: needs the BuildKit toolchain and a container runtime")
	}
	for _, tool := range []string{"rootlesskit", "buildkitd", "buildctl", "podman"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed — skipping the live build drive proof: %v", tool, err)
		}
	}

	// A live run writes the CHARLY IMAGE STORE, so point it (and the index lock's state root) at
	// temp dirs: a check must never mutate the operator's store.
	liveStoreDir(t)

	const (
		name = "leg4live"
		tag  = "localhost/leg4-live:2026.287.1200"
	)
	dir := t.TempDir()
	// A LABEL is part of the proof, not decoration: it is what the read path below reads back
	// through the RUN engine, which is the whole reason the bridge exists.
	cfile := "FROM alpine:3.20\n" +
		"LABEL ai.opencharly.liveprobe=leg4\n" +
		"CMD [\"/bin/true\"]\n"

	cfg := driveConfig{
		RunEngine: "podman",
		Dir:       dir,
		BuildDir:  filepath.Join(dir, ".build"),
	}
	box := spec.BuildResolveBox{Name: name, FullTag: tag}

	defer exec.Command("podman", "rmi", "-f", tag).Run() //nolint:errcheck

	if err := cfg.buildImage(context.Background(), box, cfile); err != nil {
		t.Fatalf("buildImage: %v", err)
	}

	// ⚑ THE BRIDGE'S NAMED DUTY, asserted where it matters: the reference must RESOLVE in the RUN
	// engine's store. A load returns success while landing an image the engine cannot resolve by
	// reference, so this is the assertion that fails if the export's NAMING ANNOTATION is ever
	// dropped — the failure mode imagestore_live_test.go proves from the other side, where an
	// archive with the annotation stripped imports DANGLING.
	if !kit.LocalImageExists("podman", tag) {
		t.Fatalf("the built image is not resolvable as %s in the run engine's store — the naming annotation did not take", tag)
	}

	// And the READ PATH the bridge exists for: labels, read through the run engine, which shares
	// no store with the builder.
	labels, err := container.InspectLabels("podman", tag)
	if err != nil {
		t.Fatalf("reading labels back through the run engine: %v", err)
	}
	if got := labels["ai.opencharly.liveprobe"]; got != "leg4" {
		t.Errorf("label ai.opencharly.liveprobe: got %q want %q (all: %v)", got, "leg4", labels)
	}

	// The export archive must not be left behind: one per build on a persistent root is storage
	// nobody reclaims.
	stage, err := buildkitdrive.StageDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); err == nil {
		entries, _ := os.ReadDir(stage)
		for _, e := range entries {
			if strings.Contains(e.Name(), "leg4-live") {
				t.Errorf("the export archive %s survived the build", e.Name())
			}
		}
	}
}
