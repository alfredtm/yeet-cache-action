# yeet-pack

A small Go CLI for fast in-memory OCI image operations. Pulls a base image, appends a layer with your binary, sets the entrypoint, and pushes — all in one round-trip. ~7MB stripped binary, statically linked, no Docker daemon required.

Built as the helper binary for [`alfredtm/yeet-cache-action`](https://github.com/alfredtm/yeet-cache-action), but works standalone.

## Why

`crane append + crane mutate + crane tag × N` takes ~7-8s of sequential network round-trips. `yeet-pack pack` builds the OCI image in memory and pushes once — closer to 3s.

## Install

Build from source:

```bash
go install github.com/alfredtm/yeet-cache-action/yeet-pack/cmd/yeet-pack@latest
```

Or grab the bundled binary from the [yeet-cache-action releases](https://github.com/alfredtm/yeet-cache-action/releases) (it's the `yeet-pack-linux-amd64` artifact inside the action's `dist/`).

## Commands

### `pack` — build + push an OCI image in one round-trip

```bash
yeet-pack pack \
  --binary ./app \
  --binary-path-in-image /app/server \
  --entrypoint /app/server \
  --base gcr.io/distroless/static@sha256:963fa6c5... \
  --tag ghcr.io/owner/repo:src-abc123 \
  --also-tag latest,v1.0.0
```

Pulls the base, layers your binary on top, sets the entrypoint, pushes to all tags (`--also-tag` runs in parallel after the primary push). Outputs `{"digest":"sha256:...","tag":"...","size":12345}`.

### `hash` — content-address from git tree SHAs

```bash
yeet-pack hash --paths cmd,internal,go.mod,go.sum --extra "go=1.22 base=..."
# → 12-char hex
```

Runs `git rev-parse HEAD:<path>` for each path, joins with newlines, appends `extra:<value>\n`, SHA-256, truncates to 12 chars. Deterministic.

### `check` — does this image exist?

```bash
yeet-pack check --image ghcr.io/owner/repo:src-abc123
# prints "hit" exit 0, or "miss" exit 1
```

Single `HEAD /v2/<repo>/manifests/<tag>` request.

### `tag` — parallel retag

```bash
yeet-pack tag --src ghcr.io/owner/repo:src-abc123 --tags latest,v1.0.0
```

Resolves the source manifest once, then `PUT`s it at each destination tag in parallel via goroutines.

## Authentication

Reads `~/.docker/config.json` via go-containerregistry's default keychain. Run `docker login` or `crane auth login` first.

## License

MIT.
