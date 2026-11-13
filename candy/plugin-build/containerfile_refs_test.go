package build

import (
	"reflect"
	"testing"
)

// The local-stage binding set: the ARG base first, then every explicit FROM, deduplicated,
// with ARG-driven and scratch forms excluded.
//
// This is the set the buildkit solve binds from the charly image store. Binding only the
// base (the previous behaviour) left a builder stage to the registry, where a check-run tag
// does not exist — the failure this test guards.
const multiStageContainerfile = `
ARG BASE_IMAGE=ghcr.io/opencharly/cachyos-layer-ssh-client:t
FROM ghcr.io/opencharly/arch-builder:t AS python-pixi-build
USER 1000
RUN pixi install

FROM ${BASE_IMAGE}
COPY --from=python-pixi-build /home/user/.pixi /home/user/.pixi

FROM scratch AS empty
`

func TestContainerfileStageRefsOrderAndDedupe(t *testing.T) {
	got := containerfileStageRefs(multiStageContainerfile)
	want := []string{
		"ghcr.io/opencharly/cachyos-layer-ssh-client:t", // ARG BASE_IMAGE
		"ghcr.io/opencharly/arch-builder:t",             // builder stage
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stage refs = %v, want %v", got, want)
	}
}

func TestContainerfileFromRefsSkipsArgsAndScratch(t *testing.T) {
	got := containerfileFromRefs(multiStageContainerfile)
	want := []string{"ghcr.io/opencharly/arch-builder:t"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("from refs = %v, want %v", got, want)
	}
}

// A duplicate FROM must bind once: buildkit rejects a repeated context opt.
func TestContainerfileStageRefsDedupeAcrossForms(t *testing.T) {
	src := "ARG BASE_IMAGE=ghcr.io/x/base:t\nFROM ghcr.io/x/base:t\nFROM ghcr.io/x/base:t AS again\n"
	got := containerfileStageRefs(src)
	want := []string{"ghcr.io/x/base:t"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stage refs = %v, want %v", got, want)
	}
}

// A Containerfile with no explicit FROM ref and no ARG must yield nothing, so no empty
// context opt is ever emitted.
// Dockerfile-legal forms the naive "FROM " prefix match missed: a TAB separator and a
// --platform flag before the reference.
func TestContainerfileFromRefsHandlesFlagsAndTabs(t *testing.T) {
	src := "FROM\tghcr.io/x/tab:t AS a\nFROM --platform=linux/amd64 ghcr.io/x/flag:t AS b\n"
	got := containerfileFromRefs(src)
	want := []string{"ghcr.io/x/tab:t", "ghcr.io/x/flag:t"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("from refs = %v, want %v", got, want)
	}
}

func TestContainerfileStageRefsEmpty(t *testing.T) {
	if got := containerfileStageRefs("FROM scratch\n"); len(got) != 0 {
		t.Fatalf("want no refs, got %v", got)
	}
}
