# AGENTS.md — plugin-build

Standalone plugin repo for the build drive (`build:box` / `build:generate` /
`build:ensure`). The plugin is a Go module at `candy/plugin-build/` (module path
`github.com/opencharly/plugin-build/candy/plugin-build`); the root `charly.yml`
only declares `discover: candy` so the repo is a project and its candy is
scanned.

Canonical files:

- `candy/plugin-build/charly.yml` — the `plugin-build:` candy entity (`plugin:`
  block, `plan:` check).
- `candy/plugin-build/` — the Go source: `plugin.go`, `drive.go`, `resolve*.go`,
  `render.go`, `podman.go`, `ensure.go`, `host_prep.go`, `bootstrap.go`,
  `schema/build.cue`, `cmd/serve/main.go`.
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.
- `README.md` — user overview only; never agent guidance.

## Load these skills first (R0)

- `/charly-build:build` — the `charly box build` surface the drive backs. Load
  before changing the build-order loop, lock, push, or merge gate.
- `/charly-build:generate` — the `charly box generate` surface (`build:generate`).
- `/charly-image:image` — the box composition the drive resolves and builds.
- `/charly-internals:plugin` — the plugin authoring reference: the `plugin:`
  block, the unified Provider model, placement. Load before touching the
  provider or schema.
- `/charly-internals:git-workflow` — before any git/PR action.

## Build / validate / test

- `go build ./...` in `candy/plugin-build/` — compile the plugin module.
- `go test ./...` in `candy/plugin-build/` — the plugin's Go tests.
- `charly box validate` at the repo root — the structural check (the candy +
  `plugin:` block, CUE schema).
- The merge gate is the **org-wide** `charly/pr-validator` (required check
  `validate / validate`, defined in `opencharly/.github`); this repo has **no**
  per-repo candy gate.
- The changed path is exercised by every box build in the ecosystem (every
  `charly box build` / `charly box generate` / image-ensure fallback).

## Modify this repo

- Edit the `plugin-build:` candy entity, the Go source, and `schema/build.cue`
  **together** — the schema documents the seam.
- The heavy resolve runs **plugin-side**; reach the host only over the
  `buildengine-*` `HostBuild` leg family for what an SDK-only candy cannot do.
- The layer merge goes to `verb:oci` via `InvokeProvider` — never re-implement a
  merge.

## Landing

- PR-only. Every change lands through a pull request; the org-required
  `charly/pr-validator` validates the diff and body and arms native auto-merge on
  PASS. Direct pushes to `main` are blocked.
- History lives in `CHANGELOG/` (written by `tag-on-merge` at merge time); the PR
  body IS the changelog.
- The authoritative rulebook is the umbrella `AGENTS.md` in
  `opencharly/opencharly` and `charly/AGENTS.md` in the charly repo. Do not
  restate its rules here.
