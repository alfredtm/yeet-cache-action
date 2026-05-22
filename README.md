# yeet-cache-action

**Stop building images you've already built.** Yeet the cached one at your release tags instead.

Measured on `ubuntu-latest` against a Go service with chi + pgx + otel + prometheus deps:

| | docker buildx | ko publish | **yeet-cache-action@v2** |
|---|---|---|---|
| Build (cache miss) | 66s | 20s | **22s** |
| No-op push (cache hit) | 66s | 20s | **12s** ← ~1.4s actual work |

The trick: use your OCI registry as a content-addressed cache. If you've built this exact source before, the image is already there. Don't rebuild — *yeet the manifest at your release tags.* In ~1 second.

## Yeet it

```yaml
name: build
on: push

permissions:
  contents: read
  packages: write
  id-token: write
  attestations: write

jobs:
  build:
    runs-on: ubuntu-latest
    env:
      IMAGE: ghcr.io/${{ github.repository }}
      BASE_IMAGE: gcr.io/distroless/static@sha256:963fa6c5...

    steps:
      - uses: alfredtm/yeet-cache-action@v2
        id: cache
        with:
          paths: cmd internal go.mod go.sum
          via-api: 'true'
          extra: go=1.22 base=${{ env.BASE_IMAGE }}
          image: ${{ env.IMAGE }}
          registry-password: ${{ secrets.GITHUB_TOKEN }}
          sign: 'true'
          verify-on-hit: 'false'
          tags: ${{ github.sha }},latest

      # Nothing below runs on cache hit — the action already retagged.

      - if: steps.cache.outputs.hit == 'false'
        uses: actions/checkout@v4
        with:
          sparse-checkout: |
            cmd
            internal
            go.mod
            go.sum
          sparse-checkout-cone-mode: false

      - if: steps.cache.outputs.hit == 'false'
        uses: actions/setup-go@v5
        with: { go-version: '1.22', cache-dependency-path: go.sum }

      - if: steps.cache.outputs.hit == 'false'
        run: |
          CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -trimpath -o app ./cmd/server
          yeet-pack pack \
            --binary app --entrypoint /app/server \
            --base "${{ env.BASE_IMAGE }}" \
            --tag "${{ steps.cache.outputs.src-tag }}" \
            --also-tag "${{ github.sha }},latest"
```

That's the yeet. Cache check, attestation, retag — all handled. `yeet-pack` is bundled with the action and installed on PATH automatically.

**Not building Go?** Swap the last step for `ko publish`, `docker buildx`, `kaniko`, whatever. Just yeet your image at `${{ steps.cache.outputs.src-tag }}` so the next run hits.

## Inputs

| | required | what |
|---|---|---|
| `image` | ✓ | OCI repo without tag (e.g. `ghcr.io/owner/repo`) |
| `registry-password` | ✓ | usually `secrets.GITHUB_TOKEN` |
| `paths` | * | space-separated paths to hash (`cmd internal go.mod go.sum`) |
| `hash` | * | pre-computed hash, escape hatch if you don't want the action computing it |
| `via-api` | | `'true'` to hash via the GitHub git API → no checkout needed on cache hit |
| `extra` | | mix build flags / Go version / base digest into the hash. **Always set this.** |
| `sign` | | `'true'` → auto-attest on miss, verify on hit (GitHub native, via `@actions/attest`) |
| `verify-on-hit` | | default `'true'`; set `'false'` for trust-on-first-use (~3s faster hits) |
| `tags` | | comma-separated, what to retag the cached image as on hit |

\* one of `paths` or `hash` is required.

Outputs: `hit`, `src-hash`, `src-tag`, `cached-tag`. See [`action.yml`](./action.yml) for the full list.

## Gotchas

- **Pin your base image by digest, not tag.** Mutable tags poison the cache silently. Put the digest in `extra`.
- **`extra` matters.** Different `-ldflags` = different binary = should be a different cache key. Mix toolchain version, build flags, base digest. The action doesn't infer.
- **`sign: 'true'` needs both `id-token: write` AND `attestations: write` permissions.** The failure message if you forget is unhelpful.
- **Cache miss requires the caller to push the built image at `steps.cache.outputs.src-tag`.** `yeet-pack pack --tag ...` is one line. Forget it and the next run misses again.
- **Migrating from v1 (cosign) to v2 (attestation)?** Bump `extra` once (`extra: migration=v2`) to yeet the old cosign-signed cache entries into the void. They'd fail verification under v2 anyway.
- **Determinism is on you.** `-trimpath` + `-ldflags='-s -w'` for Go. `SOURCE_DATE_EPOCH` for everything else.

## What ships with the action

Two artifacts, both committed under `dist/` and downloaded together when you `uses: alfredtm/yeet-cache-action@v2`:

| File | What it is | Role |
|---|---|---|
| `dist/main.js` (~1MB) | Node 20 JavaScript, compiled from TypeScript | Runs at the start of the step. Parses inputs, computes the hash, checks the registry, retags on hit. |
| `dist/post.js` (~1MB) | Same — runs *after* your other steps finish | On cache miss, signs the newly-built image via `@actions/attest`. |
| `dist/yeet-pack-linux-amd64` (~7MB) | A Go binary using [go-containerregistry](https://github.com/google/go-containerregistry) | Does the actual heavy lifting: hash, registry HEAD check, in-memory image construction, parallel retag. The JS copies it to `/usr/local/bin/yeet-pack` on every run so your own workflow steps can call it too (that's the `yeet-pack pack` line in the example). |

**Why the split?** GitHub Actions can only execute JavaScript, a Docker container, or composite YAML — it can't run a Go binary as the action's entry point. So the JS is the conductor (~200 lines of orchestration: parse inputs, decide hit vs miss, call `gh attestation verify`), and the Go binary is the instrument that actually talks to the OCI registry. The JS shells out to the Go binary; the user's own workflow steps can too.

## How it works

On every run: compute a 12-char hash from your declared `paths` + `extra`, ask the registry if `<image>:src-<hash>` exists.

- **Hit** → optionally verify the attestation → retag the cached image to your release tags → done in ~1.5s of actual work.
- **Miss** → emit `src-tag` and let your build steps push to it. The post hook signs the new image via `@actions/attest` so the next run can verify it.

Verification on hit uses `gh attestation verify` (the `gh` CLI is pre-installed on GitHub-hosted runners). No Docker daemon. No Dockerfile required. No crane install.

See [SPEC.md](./SPEC.md) for the `:src-<hex>` tag convention.

## v1

Composite YAML + cosign instead of JS + GitHub attestation. Still maintained at `@v1` — slightly smaller download, roughly the same cache-hit speed. Use it if you prefer cosign or want a transparent bash-only action you can fork and own.

## License

MIT. Yeet responsibly.
