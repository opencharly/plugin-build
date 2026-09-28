# plugin-build

The build drive for OpenCharly — it owns the podman build engine behind
`charly box build`, `charly box generate`, and the ensure-image orchestration.

The plugin OWNS the podman build drive (the build-order loop, the per-image
build lock, the push, and the merge gate) for `build:box` and `build:generate`,
plus the ensure-image orchestration for `build:ensure`. It runs the heavy
loader/render resolve **plugin-side itself**, reaching the host only for what a
SDK-only candy structurally cannot do (the bootstrap-delicate local scan, the
git fetch, the build-time plugin connect, the host-fs prep) over a small
`buildengine-*` `HostBuild` leg family. The layer merge crosses to `verb:oci`
via `InvokeProvider`. The plugin then runs podman directly — building each image
(Containerfile piped over stdin), gating the inline merge on the box's
`MergeAuto`, and pushing after merge.

It is **compiled into charly** (listed in the embedded `compiled_plugins:`):
`charly box build` / `charly box generate` / every `dispatchBuildEnsure` caller
dispatch it in-process. It also serves out-of-process via `cmd/serve` for
module-shape parity — one provider, two placements.

## What it provides

| Capability | Surface |
|---|---|
| `build:box` | the `charly box build` drive — build-order loop, per-image lock, push, merge gate |
| `build:generate` | the `charly box generate` engine — renders the `.build/` Containerfile tree |
| `build:ensure` | ensure-image — pull an image, falling back to a local/remote-cached build |

## How to use it

The drive is invoked through the `charly box` commands — no candy composition is
needed:

```bash
charly box build my-box
charly box generate my-box
charly box pull my-image
```

## Layout

- `candy/plugin-build/` — the plugin module: `plugin.go` (provider + meta),
  `drive.go`, `resolve*.go`, `render.go`, `podman.go`, `ensure.go`,
  `host_prep.go`, `bootstrap.go`, `schema/build.cue`, `cmd/serve/main.go`.
- `candy/plugin-build/charly.yml` — the `plugin-build:` candy entity (`plugin:`
  block, `plan:` check).
- `charly.yml` — the root project manifest (`discover: candy`).
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.

## Related

- Owning skills: `/charly-build:build` (the `charly box build` surface) and
  `/charly-build:generate` (the `charly box generate` surface).
- `/charly-image:image` — the box composition the drive builds.
- `/charly-internals:plugin` — the plugin/provider model.
- [`opencharly/charly`](https://github.com/opencharly/charly) — the charly CLI.
