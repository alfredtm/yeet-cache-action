# yeet-cache-action

**A GitHub Action that retags an existing image from your registry instead of rebuilding when nothing relevant has changed.**

## What's the problem?

You push a README change. Or a `.github/workflows/` tweak. Or a `k8s/` manifest update. Your CI rebuilds the entire image anyway — same Go source, same `go.sum`, same base image — and 66 seconds later produces a byte-identical artifact you already had in your registry.

`docker buildx` with GHA cache still revalidates every layer. `ko publish` has no whole-build cache. Most teams accept this as just *"what CI does."*

## What does this do about it?

It uses your OCI registry as a content-addressed cache:

1. Hash your declared inputs — source paths + Go version + build flags + base image digest.
2. Ask the registry whether `<your-image>:src-<hash>` already exists.
3. **Hit** → retag that image to `latest` and `${{ github.sha }}`. Done in ~1.5 seconds.
4. **Miss** → emit the tag for your build step to push to. On success, sign the new image so the next run can verify it.

The novelty isn't the idea (Bazel did this in 2015). It's the packaging — a 30-line workflow snippet instead of *"go learn Bazel."*

## Who's this for?

Teams running CI on every push to a Go service. Especially when most pushes are docs/config/PR-fixup changes that don't touch the binary. The savings compound at any meaningful CI volume.

Not strictly Go-only — the cache check, signing, and retag logic works in front of `ko`, `docker buildx`, `kaniko`, anything. The bundled `yeet-pack pack` helper is the Go-specific bit and is optional.

---

## Numbers

Measured on `ubuntu-latest`, real Go service (chi + pgx + otel + prometheus):

| | `docker buildx` | `ko publish` | **`yeet-cache-action@v2`** |
|---|---|---|---|
| Build (cache miss) | 66s | 20s | **22s** |
| No-op push (cache hit) | 66s | 20s | **12s** ← ~1.4s actual work |

The 12s on cache hit is essentially the GitHub-hosted runner platform floor. The action itself does sub-2-seconds of real work — the rest is runner spin-up + action download.

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

Cache check, signing, retag — all handled by the `uses:` line. The `yeet-pack` binary is bundled with the action and copied to `/usr/local/bin` automatically; your build step uses it for the in-memory image push on cache miss.

### Live example

The numbers above were measured from [**alfredtm/yeeted**](https://github.com/alfredtm/yeeted) — a working Go service shipping with three side-by-side workflows: `build-yeet.yml` (this action), `build-docker.yml` (`docker buildx`), `build-ko.yml` (`ko publish`). Every push runs all three; the [Actions tab](https://github.com/alfredtm/yeeted/actions) is a live benchmark. Fork it as a starting point.

## Inputs

| | required | what |
|---|---|---|
| `image` | ✓ | OCI repo without tag (e.g. `ghcr.io/owner/repo`) |
| `registry-password` | ✓ | usually `secrets.GITHUB_TOKEN` |
| `paths` | * | space-separated paths to hash (`cmd internal go.mod go.sum`) |
| `hash` | * | pre-computed hash if you want to compute it yourself outside the action |
| `via-api` | | `'true'` to hash via the GitHub git API → no checkout needed on cache hit |
| `extra` | | mix build flags / Go version / base digest into the hash. **Always set this.** |
| `sign` | | `'true'` → auto-attest on miss, verify on hit (GitHub native via `@actions/attest`) |
| `verify-on-hit` | | default `'true'`; set `'false'` for trust-on-first-use (~3s faster hits) |
| `tags` | | comma-separated, what to retag the cached image as on hit |

\* one of `paths` or `hash` is required.

Outputs: `hit`, `src-hash`, `src-tag`, `cached-tag`. Full list in [`action.yml`](./action.yml).

## Gotchas

- **Pin your base image by digest, not tag.** Mutable tags poison the cache silently. Put the digest in `extra`.
- **`extra` matters.** Different `-ldflags` = different binary = should be a different cache key. The action doesn't infer.
- **`sign: 'true'` needs both `id-token: write` AND `attestations: write` permissions.** Easy to forget; the error message is unhelpful.
- **Cache miss requires the caller to push at `steps.cache.outputs.src-tag`.** `yeet-pack pack --tag ...` is one line. Forget it and the next run misses again.
- **Migrating from v1 (cosign) to v2 (attestation)?** Bump `extra` once (`extra: migration=v2`) to yeet the old cosign-signed entries into the void.
- **Determinism is on you.** `-trimpath` + `-ldflags='-s -w'` for Go. `SOURCE_DATE_EPOCH` for everything else.

## How it works

A Node 20 JavaScript action with a bundled Go helper (`yeet-pack`, ~7MB). The JS is the conductor (~200 lines); the Go binary is the instrument that actually talks to the OCI registry.

| File in `dist/` | What it is | When it runs |
|---|---|---|
| `main.js` | Cache check, verification on hit, retag | At the start of the action step |
| `post.js` | Sign new image via `@actions/attest` | After your other steps finish |
| `yeet-pack-linux-amd64` | Go CLI using [go-containerregistry](https://github.com/google/go-containerregistry) | Called by `main.js`, copied to `/usr/local/bin/yeet-pack` so your workflow steps can use it too |

Why the split? GitHub Actions can only execute JavaScript, Docker containers, or composite YAML — it can't run a Go binary as the action's entry point. So we have JS for orchestration (parse inputs, call `gh attestation verify`, decide what to do next), Go for the heavy lifting (hash files, build OCI manifests in memory, push, retag in parallel).

No Docker daemon. No Dockerfile required. No crane install. See [SPEC.md](./SPEC.md) for the `:src-<hex>` tag convention.

## v1

v1 was a composite YAML action using cosign for signatures. **No longer maintained** — pinned at v1.3.0 for anyone who still references it. New work happens on v2; use `@v2` for any new project.

## License

MIT. Yeet responsibly.
