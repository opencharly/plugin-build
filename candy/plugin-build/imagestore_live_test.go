package build

// imagestore_live_test.go — the LIVE proof of the two claims the build drive rests on.
//
// Both tests SKIP VISIBLY when the toolchain is absent (R8) and are never mocked. They live in the
// module for the same reason buildkit_live_test.go does: assembling `charly` requires every pinned
// plugin to compile against the contract, so the changed path is driven directly.
//
//  1. The OCI naming annotation, WITH ITS NEGATIVE CONTROL. A `name=<full-ref>` export imports
//     tagged; the same archive with the annotation stripped imports DANGLING. That is what proves
//     the annotation — not coincidence, and not a tag step — is doing the naming. The build also
//     leaves its image in the shared store under a digest.
//  2. A box-on-box build whose parent is in the charly image store while the parent's registry is
//     UNREACHABLE, with the control that the same child fails without the store binding.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/sdk/buildkitdrive"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/sdk/storekit"
	"github.com/opencharly/spec/spec"
)

// liveStoreDir points a live run at a throwaway cache root. A check must NEVER mutate the
// operator's store, and `CHARLY_CACHE_DIR` is D-STORE-1's ONE relocation lever — the same one a
// disposable bed uses. A caller that already exported it (a bed, or a run keeping its emitted
// evidence) keeps its own.
func liveStoreDir(t *testing.T) {
	t.Helper()
	if os.Getenv("CHARLY_CACHE_DIR") == "" {
		t.Setenv("CHARLY_CACHE_DIR", t.TempDir())
	}
}

// liveTools skips unless the whole toolchain is present.
func liveTools(t *testing.T, tools ...string) {
	t.Helper()
	if testing.Short() {
		t.Skip("live build: needs the BuildKit toolchain and a container runtime")
	}
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed — skipping the live store proof: %v", tool, err)
		}
	}
}

// A named export lands TAGGED; the same archive with `io.containerd.image.name` removed lands
// DANGLING. Both directions matter: without the control, the tagged result could be an artefact of
// the image already existing in the engine's store.
func TestImageStore_AnnotationNamesTheImport_AndItsAbsenceDoesNot(t *testing.T) {
	liveTools(t, "rootlesskit", "buildkitd", "buildctl", "podman")
	liveStoreDir(t)

	ctx := context.Background()
	dir := t.TempDir()
	cfg := driveConfig{RunEngine: "podman", Dir: dir, BuildDir: filepath.Join(dir, ".build")}

	const named = "localhost/w36-named-live:2026.296.1200"
	namedFile := "FROM alpine:3.20\nLABEL w36=named\nCMD [\"/bin/true\"]\n"
	if err := cfg.buildImage(ctx, spec.BuildResolveBox{Name: "w36named", FullTag: named}, namedFile); err != nil {
		t.Fatalf("buildImage: %v", err)
	}
	defer exec.Command("podman", "rmi", "-f", named).Run() //nolint:errcheck

	if !kit.LocalImageExists("podman", named) {
		t.Fatalf("%s does not resolve after the import — the naming annotation did not take", named)
	}

	// The build's image is an ENTRY of the shared store, under a digest. The entry's annotations
	// are read through the STORE's own inventory: `ImageStore.List`/`Lookup` answer a reference,
	// and `Entries` is the key-and-annotation view the sweep reads (D-STORE-7).
	store, err := storekit.OpenImageStore()
	if err != nil {
		t.Fatalf("OpenImageStore: %v", err)
	}
	digest, ok, err := store.Lookup(named)
	if err != nil || !ok || digest == "" {
		t.Fatalf("the charly image store has no entry for %s (ok=%v, digest=%q, err=%v)", named, ok, digest, err)
	}
	entries, err := storekit.OpenStore(storekit.ImageStoreName)
	if err != nil {
		t.Fatalf("open the store inventory: %v", err)
	}
	inventory, err := entries.Entries()
	if err != nil {
		t.Fatalf("read the store entries: %v", err)
	}
	found := false
	for _, e := range inventory {
		if e.Annotations[storekit.AnnotationImageName] != named {
			continue
		}
		found = true
		if e.Key != digest {
			t.Errorf("the entry annotated %s is keyed %s, want the digest %s `Lookup` resolved",
				named, e.Key, digest)
		}
		if e.Annotations[storekit.AnnotationCreated] == "" {
			t.Errorf("the store entry for %s carries no %s", named, storekit.AnnotationCreated)
		}
	}
	if !found {
		t.Fatalf("no store entry is annotated %s=%s", storekit.AnnotationImageName, named)
	}

	// The store the build wrote is DISCOVERABLE by name: `charly clean --cache` sweeps what
	// `NamedStores` lists (a store it cannot see would grow forever — D-STORE-7), and the sweep
	// reads the layout marker, so this is also the check that the build left one.
	names, err := storekit.NamedStores()
	if err != nil {
		t.Fatalf("NamedStores: %v", err)
	}
	listed := false
	for _, name := range names {
		if name == storekit.ImageStoreName {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("NamedStores() = %v does not list %q — the sweep cannot see the store the build wrote",
			names, storekit.ImageStoreName)
	}

	// THE CONTROL: a DIFFERENT image, exported with a name and then stripped of the naming
	// annotation, must land dangling. Its differing LABEL keeps the control from reusing the
	// named image's bytes, so nothing but the annotation can explain the outcome.
	const control = "localhost/w36-control-live:2026.296.1200"
	controlDir := t.TempDir()
	controlFile := filepath.Join(controlDir, "Containerfile")
	if err := os.WriteFile(controlFile, []byte("FROM alpine:3.20\nLABEL w36=control\nCMD [\"/bin/true\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	controlTar := filepath.Join(controlDir, "control.tar")
	daemon, err := buildkitdrive.Ensure(ctx, buildkitdrive.ExecRunner{}, buildkitdrive.DaemonOptions{})
	if err != nil {
		t.Fatalf("preparing the builder: %v", err)
	}
	runner := buildkitdrive.ExecRunner{}
	args := []string{"build", "--frontend", "dockerfile.v0",
		"--addr", daemon.Addr(),
		"--local", "context=" + controlDir,
		"--local", "dockerfile=" + controlDir,
		"--opt", "filename=Containerfile",
		"--output", "type=oci,name=" + control + ",dest=" + controlTar,
	}
	if _, err := runner.Run(ctx, buildkitdrive.BuildctlBinary, args...); err != nil {
		t.Fatalf("building the control image: %v", err)
	}
	if err := rewriteExportIndex(controlTar, func(idx *ociIndex) {
		for i := range idx.Manifests {
			delete(idx.Manifests[i].Annotations, storekit.AnnotationImageName)
			delete(idx.Manifests[i].Annotations, storekit.AnnotationRefName)
		}
	}); err != nil {
		t.Fatalf("stripping the control's naming annotation: %v", err)
	}
	// Assert WHERE the control fails: the load must SUCCEED (the archive is valid) and the
	// reference must fail to RESOLVE. An error from the load would prove nothing about naming.
	cerr := importExport(ctx, runner, "podman", controlTar, control)
	if cerr == nil {
		t.Fatalf("an archive with NO %s annotation imported and reported %s resolvable — the "+
			"annotation would not be what names the image", storekit.AnnotationImageName, control)
	}
	if !strings.Contains(cerr.Error(), "not resolvable") {
		t.Fatalf("the control failed BEFORE the reference check (%v) — the load, not the naming, "+
			"is what failed, so this proves nothing", cerr)
	}
	if kit.LocalImageExists("podman", control) {
		t.Fatalf("the unnamed control archive landed tagged as %s", control)
	}
}

// A parent in the charly image store is supplied to the solve from the STORE while its registry is
// unreachable. The control — a child whose base is absent from the store — fails against the same
// unreachable registry, which is what makes the first build's success attributable to the binding.
func TestImageStore_BoxOnBoxBuildsWithTheRegistryUnreachable(t *testing.T) {
	liveTools(t, "rootlesskit", "buildkitd", "buildctl", "podman")
	liveStoreDir(t)

	ctx := context.Background()
	dir := t.TempDir()
	cfg := driveConfig{RunEngine: "podman", Dir: dir, BuildDir: filepath.Join(dir, ".build")}

	// `.invalid` is reserved by RFC 2606 and never resolves, so any registry access fails at DNS —
	// the "registry unreachable" condition is a property of the reference, not of this host.
	parentRef := "ghcr.invalid/opencharly/w36-live-parent:2026.296.1200"
	parentFile := "FROM alpine:3.20\nLABEL w36=parent\nCMD [\"/bin/true\"]\n"
	if err := cfg.buildImage(ctx, spec.BuildResolveBox{Name: "w36parent", FullTag: parentRef}, parentFile); err != nil {
		t.Fatalf("building the parent: %v", err)
	}
	store, err := storekit.OpenImageStore()
	if err != nil {
		t.Fatalf("OpenImageStore: %v", err)
	}
	if _, ok, err := store.Lookup(parentRef); err != nil || !ok {
		t.Fatalf("the parent is not in the charly image store (ok=%v, err=%v)", ok, err)
	}

	// THE CONTROL: a base that is NOT in the store must fail — proving the child's success below
	// comes from the store binding and not from some other resolution path.
	missingRef := "ghcr.invalid/opencharly/w36-live-absent:2026.296.1200"
	missingFile := "ARG BASE_IMAGE=" + missingRef + "\nFROM ${BASE_IMAGE}\nLABEL w36=missing-control\n"
	if err := cfg.buildImage(ctx, spec.BuildResolveBox{Name: "w36missing", FullTag: "localhost/w36-missing:2026.296.1200"}, missingFile); err == nil {
		t.Fatalf("a child whose base is absent from the store and unreachable in the registry BUILT — "+
			"the box-on-box result below would prove nothing (base %s)", missingRef)
	}

	childRef := "localhost/w36-live-child:2026.296.1200"
	childFile := "ARG BASE_IMAGE=" + parentRef + "\nFROM ${BASE_IMAGE}\nLABEL w36=child\nCMD [\"/bin/true\"]\n"
	if err := cfg.buildImage(ctx, spec.BuildResolveBox{Name: "w36child", FullTag: childRef}, childFile); err != nil {
		t.Fatalf("the box-on-box build failed with the parent in the store: %v", err)
	}
	defer exec.Command("podman", "rmi", "-f", childRef).Run()  //nolint:errcheck
	defer exec.Command("podman", "rmi", "-f", parentRef).Run() //nolint:errcheck

	if !kit.LocalImageExists("podman", childRef) {
		t.Fatalf("the child %s does not resolve after the import", childRef)
	}
}
