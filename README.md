# yeet-cache-action

**Stop building images you've already built.** Yeet the cached one at your release tags instead.

Same Go service, same CI. Different mental model.

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
        with: { sparse-checkout: "cmd\ninternal\ngo.mod\ngo.sum", sparse-checkout-cone-mode: false }

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

## How it works

A node20 action with a bundled Go helper (`yeet-pack`, ~7MB). On every run: compute a 12-char hash from your declared paths + extra, ask the registry if `<image>:src-<hash>` exists. Hit → retag. Miss → emit the tag and let you build. Post hook attests fresh builds via `@actions/attest`. Verify on hit uses `gh attestation verify` (gh CLI is already on the runner).

No Docker daemon. No Dockerfile required. No crane install. See [SPEC.md](./SPEC.md) for the `:src-<hex>` tag convention.

## v1

Composite YAML + cosign instead of JS + GitHub attestation. Still maintained at `@v1` — slightly smaller download, roughly the same cache-hit speed. Use it if you prefer cosign or want a transparent bash-only action you can fork and own.

## License

MIT. Yeet responsibly.
