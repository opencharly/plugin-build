package build

// buildkit.go — the BUILD DRIVE. It was `podman.go`, and the name went with the mechanism:
// this file no longer drives an engine at all.
//
// ⚑ One builder: BuildKit. The Containerfile is built by the buildkitdrive kit (daemon ensure +
// `buildctl` argv), and the built image is STORED IN THE CONFIGURED CONTAINER BACKEND via the
// SDK's LoadExport, because every later read of it — its capability labels, its deploy, its
// status, its inline merge, `charly clean` — resolves it through that backend. Each built image
// is ALSO pushed to the LOCAL CHARLY OCI REGISTRY under its FullTag, and the daemon mirrors
// `ghcr.io/opencharly` to that registry (DaemonOptions.RegistryMirror), so the next stage's
// `FROM <FullTag>` resolves an internal base locally instead of against the upstream registry.
// The engine-named drivers this replaces ({podman|docker} build --layers, docker buildx --push,
// podman build --manifest, the cache-arg matrix and the push retry ladder) are DELETED, which is
// what closes plugin-build#29 by deletion: every cell of its engine×flag matrix belonged to one
// of them.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencharly/sdk/buildkitdrive"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/container"
	"github.com/opencharly/spec/spec"
)

// imageTags computes the tags for an image. charly is CalVer-only — it never emits `:latest`.
// Every built image carries exactly its CalVer tag (box.FullTag, e.g.
// `ghcr.io/opencharly/fedora:2026.114.1042`).
func imageTags(box spec.BuildResolveBox) []string {
	return []string{box.FullTag}
}

// buildImage builds one image through the BuildKit drive and bridges it into the run engine's
// store.
//
// The per-image build lock still serializes concurrent builds of THIS image across charly
// processes (so a shared intermediate built by many parallel beds is built COLD once — the others
// block here, then re-use BuildKit's own cache), while DISTINCT images take distinct locks and
// build in parallel. The wait is spec/lock's bounded-but-REPORTED one: a peer building the same
// image is a SLOW holder, not a stuck one, so the second builder QUEUES and reports the holder.
func (c driveConfig) buildImage(ctx context.Context, box spec.BuildResolveBox, containerfile string) error {
	release, err := kit.AcquireImageBuildLock(box.FullTag)
	if err != nil {
		return imageBuildLockError(box, err)
	}
	defer func() { _ = release() }()

	// BuildKit reads the Containerfile and the context from the FILESYSTEM (the engine took the
	// Containerfile on stdin). This writes the canonical generated path — the render step wrote
	// the same bytes there — so the file the build reads is the file the render produced.
	cfile := filepath.Join(c.BuildDir, box.Name, "Containerfile")
	if err := os.MkdirAll(filepath.Dir(cfile), 0o755); err != nil {
		return fmt.Errorf("preparing the build context for %s: %w", box.Name, err)
	}
	if err := os.WriteFile(cfile, []byte(containerfile), 0o644); err != nil {
		return fmt.Errorf("writing the Containerfile for %s: %w", box.Name, err)
	}

	// The daemon's one setting this drive resolves is the renamed INNER concurrency
	// (`stage_parallelism`, formerly `podman build --jobs N`): under BuildKit it is the daemon's
	// `[worker.oci] max-parallelism`, applied when the daemon starts.
	daemon, err := buildkitdrive.Ensure(ctx, buildkitdrive.ExecRunner{}, buildkitdrive.DaemonOptions{
		MaxParallelism: c.StageParallelism,
		RegistryMirror: c.RegistryMirror,
	})
	if err != nil {
		return fmt.Errorf("preparing the builder for %s: %w", box.Name, err)
	}

	stageDir, err := buildkitdrive.StageDir()
	if err != nil {
		return err
	}
	dest := buildkitdrive.ExportFile(stageDir, box.FullTag)

	build := buildkitdrive.Build{
		Containerfile: cfile,
		Context:       c.Dir,
		Ref:           box.FullTag,
		Addr:          daemon.Addr(),
		Platforms:     buildPlatforms(c.Platform, box.Platforms),
		NoCache:       c.NoCache,
	}
	args, err := build.Args(dest)
	if err != nil {
		return err
	}
	if c.Push {
		args = append(args, pushOutput(box.FullTag)...)
	}
	// The built image goes to the local charly OCI registry under the SAME tag the next
	// stage's FROM names, so an internal base resolves locally through the daemon mirror
	// instead of querying the upstream registry.
	localRef := ""
	if c.RegistryMirror != "" {
		localRef = localRegistryRef(c.RegistryMirror, box.FullTag)
		args = append(args, pushOutput(localRef)...)
	}
	fmt.Fprintf(os.Stderr, "\n--- Building %s ---\n", box.Name)

	runner := buildkitdrive.ExecRunner{}
	if _, err := runner.Run(ctx, buildkitdrive.BuildctlBinary, args...); err != nil {
		return fmt.Errorf("building %s: %w", box.Name, err)
	}

	// The bridge: load, tag, and VERIFY the reference resolves. The tag re-application is a named
	// duty of the kit's LoadExport, not a caveat — a load returns success while landing an
	// OCI-exported image UNTAGGED, so without it the CalVer ref would resolve to nothing and every
	// downstream verb would break.
	if err := buildkitdrive.LoadExport(ctx, runner, container.EngineBinary(c.RunEngine), dest, box.FullTag); err != nil {
		return fmt.Errorf("bridging %s into the %s store: %w", box.Name, c.RunEngine, err)
	}

	// The export is a full copy of the image, and keeping one per build on a persistent root is
	// storage nobody reclaims (GC for that root is not yet proven), so it goes once the bridge has
	// succeeded — and a failure to remove it is REPORTED, never swallowed.
	if err := os.Remove(dest); err != nil {
		return fmt.Errorf("removing the export %s: %w", dest, err)
	}
	return nil
}

// localRegistryRef maps a built image onto the local charly OCI registry under the mirror
// path the daemon resolves (ghcr.io/opencharly/<name>:<tag> -> <mirror>/opencharly/<name>:<tag>).
func localRegistryRef(mirror, fullTag string) string {
	path := fullTag
	if i := strings.IndexByte(path, '/'); i >= 0 {
		path = path[i+1:]
	}
	return mirror + "/" + path
}

// pushOutput returns the registry exporter a --push build adds.
//
// ⚑ The LOCAL export is not composed here: the kit's Args owns it, because the OCI export is what
// makes the result reachable by the run engine at all (that is the kit's bridge contract). This is
// only the second destination — BuildKit's image exporter, which is the ONE push path, replacing
// both `docker buildx --push` and `podman manifest push`, and the only one that can push the
// multi-platform image this build produced. Which registries a build writes is the DRIVE's policy;
// the build itself stays with the kind-blind kit.
func pushOutput(ref string) []string {
	return []string{"--output", "type=image,name=" + ref + ",push=true"}
}

// imageBuildLockError renders the per-image build-lock failure. The primitive names the HOLDER
// (pid + command, resolved from the kernel); this wrap adds what the primitive cannot know — WHICH
// box, the LOCK KEY (the tag-stripped ref, so the contended artifact is identifiable), and what to
// do about it. It stays wrapped with %w so callers can still errors.Is/As the primitive's error.
func imageBuildLockError(box spec.BuildResolveBox, err error) error {
	return fmt.Errorf("acquiring the per-image build lock for %s (%s): %w — another charly process is building this image; wait for it to finish and re-run (the second build then re-uses the builder's cache)", box.Name, box.FullTag, err)
}

// buildPlatforms narrows a local solve to ONE platform.
//
// box.Platforms comes from cfg.Defaults.Platforms / img.Platforms. When a project declares no platforms the base
// image's multi-arch manifest list expands into one build PER architecture — so a local build of a box based on
// quay.io/fedora/fedora:43 builds both linux/amd64 and linux/arm64 for an image only ever run on this host. On a
// host without arm64 emulation that cannot complete at all; with emulation registered it is several times the
// work, and the second build stream is what pushes the process into the OOM killer.
//
// driveConfig.Platform is the drive's host-resolved single target ("empty on a push build"), so honouring it here
// makes a local build single-platform while leaving a push build multi-arch exactly as before — no default is
// changed for anyone who declares platforms or pushes.
func buildPlatforms(single string, declared []string) []string {
	if single != "" {
		return []string{single}
	}
	return declared
}
