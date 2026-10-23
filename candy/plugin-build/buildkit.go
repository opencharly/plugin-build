package build

// buildkit.go — the BUILD DRIVE. It was `podman.go`, and the name went with the mechanism:
// this file no longer drives an engine at all.
//
// ⚑ One builder: BuildKit. The Containerfile is built by the buildkitdrive kit (daemon ensure +
// `buildctl` argv). The export is NAMED with `io.containerd.image.name=<FullTag>`, written to the
// CHARLY IMAGE STORE (one engine-neutral OCI layout at $XDG_DATA_HOME/charly/images — see
// imagestore.go), and then materialised into the run engine, because every later read of it — its
// capability labels, its deploy, its status, its inline merge, `charly clean` — resolves it
// through that engine. The import needs NO tag step: the annotation names the image (measured on
// podman/docker/nerdctl), and the import verifies the reference resolves.
//
// A parent box that is present in the charly image store is bound to the daemon as an OCI layout
// named context, so a box-on-box build never reaches the registry for it. Each built image is ALSO
// pushed to the LOCAL CHARLY OCI REGISTRY under its FullTag, and the daemon mirrors
// `ghcr.io/opencharly` to that registry (DaemonOptions.RegistryMirror), so any base the store does
// not hold resolves internally instead of against the upstream registry.
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
	"time"

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

	// A parent box built LOCALLY is supplied from the charly image store, not the registry: the
	// daemon is handed the store as an OCI layout and the base reference is bound to the parent's
	// manifest digest as a NAMED CONTEXT, which is the one form the dockerfile frontend resolves
	// from buildkit's `--oci-layout` store (measured — `FROM oci-layout://` itself is not a valid
	// dockerfile reference). The registry stays the fallback: when the base is absent from the
	// store, nothing is bound and the daemon resolves the reference normally.
	baseRef := containerfileBaseRef(containerfile)
	baseDigest := ""
	if baseRef != "" {
		storeDir, serr := imageStoreDir()
		if serr != nil {
			return serr
		}
		digest, found, serr := storeLookup(baseRef)
		if serr != nil {
			return serr
		}
		if found {
			baseDigest = digest
			build.OCILayouts = []buildkitdrive.OCILayout{{ID: imageStoreID, Dir: storeDir}}
			fmt.Fprintf(os.Stderr, "--- %s: base %s from the charly image store ---\n", box.Name, baseRef)
		}
	}

	args, err := build.Args(dest)
	if err != nil {
		return err
	}
	if baseDigest != "" {
		args = append(args, "--opt", "context:"+baseRef+"=oci-layout://"+imageStoreID+"@"+baseDigest)
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

	// Name the archive, then make the image durable in the charly image store, then materialise it
	// into the run engine.
	//
	// ⚑ There is NO tag step. The export is annotated with `io.containerd.image.name=<FullTag>`
	// BEFORE the import, and every engine measured applies that annotation on load — so the
	// reference resolves because of the annotation, not because a second command re-applied it.
	// The import VERIFIES the reference resolves, so a stripped annotation cannot pass for a
	// successful build.
	created := time.Now().UTC()
	if err := annotateExport(dest, box.FullTag, created); err != nil {
		return fmt.Errorf("naming the %s export: %w", box.Name, err)
	}
	if _, err := storeAdd(dest, box.FullTag); err != nil {
		return fmt.Errorf("storing %s in the charly image store: %w", box.Name, err)
	}
	if err := importExport(ctx, runner, c.RunEngine, dest, box.FullTag); err != nil {
		return fmt.Errorf("importing %s into the %s store: %w", box.Name, c.RunEngine, err)
	}

	// The export is a full copy of the image, and keeping one per build on a persistent root is
	// storage nobody reclaims (GC for that root is not yet proven), so it goes once the bridge has
	// succeeded — and a failure to remove it is REPORTED, never swallowed.
	if err := os.Remove(dest); err != nil {
		return fmt.Errorf("removing the export %s: %w", dest, err)
	}
	return nil
}

// importExport materialises an ANNOTATED OCI archive into the run engine and proves the reference
// resolves.
//
// The engine binary and the existence probe both come from the capability table, never from a
// literal in this file: every engine measured takes `<binary> load -i <archive>` and exposes a
// table-provided probe (`image exists` / `image inspect`), so the drive names no engine. No `tag`
// step follows the load — the archive's `io.containerd.image.name` annotation is what names the
// image, and the probe below is what proves it did.
func importExport(ctx context.Context, r buildkitdrive.Runner, runEngine, tarPath, ref string) error {
	engine := container.EngineBinary(runEngine)
	if _, err := r.Run(ctx, engine, "load", "-i", tarPath); err != nil {
		return err
	}
	capability, ok := container.EngineCapabilityFor(runEngine)
	if !ok || len(capability.ImageExistsArgv) == 0 {
		return fmt.Errorf("the engine capability table carries no image-exists probe for %q", runEngine)
	}
	probe := append(append([]string{}, capability.ImageExistsArgv...), ref)
	if _, err := r.Run(ctx, engine, probe...); err != nil {
		return fmt.Errorf("the loaded image is not resolvable as %s — the archive's %s annotation is what names it: %w", ref, imageNameAnnotation, err)
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
