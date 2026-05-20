# yeet-cache-action

**Skip CI image builds when source hasn't changed.** Hashes your build inputs, asks your registry *"do you already have an image for this hash?"*, and on a hit retags the cached image in ~1 second. Works in front of any image builder. Optionally verifies cosign signatures on cache hits to prevent registry-tag spoofing.

See [SPEC.md](./SPEC.md) for the `:src-<hex>` tag convention.

## Comparison times

Measured on `ubuntu-latest` against a Go service with realistic deps (chi, pgx, prometheus, otel):

| Workflow | Build (cache miss) | No-op push (cache hit) |
|---|---|---|
| `docker buildx` + GHA cache | **66s** | 66s (re-validates layers) |
| `ko publish` | **20s** | 20s (no whole-build cache) |
| `crane append` + `yeet-cache` | **~15s** | **4.4s actual work, 15s wall** |

`yeet-cache` is the only one of these that skips entirely when source is unchanged. The 15s wall on cache hit is mostly GitHub Actions overhead — the action itself does 4.4s of registry calls.

## Usage

```yaml
name: build
on: push

permissions:
  contents: read
  packages: write
  id-token: write          # required for cosign keyless

jobs:
  build:
    runs-on: ubuntu-latest
    env:
      IMAGE: ghcr.io/${{ github.repository }}
      GO_VERSION: "1.22"
    steps:
      - uses: actions/checkout@v4

      - uses: alfredtm/yeet-cache-action@v1
        id: cache
        with:
          paths: cmd internal go.mod go.sum
          extra: go=${{ env.GO_VERSION }} ldflags=-s -w
          image: ${{ env.IMAGE }}
          registry-password: ${{ secrets.GITHUB_TOKEN }}
          sign: 'true'
          tags: ${{ github.sha }},latest

      # Everything below runs ONLY on cache miss.
      - if: steps.cache.outputs.hit == 'false'
        uses: actions/setup-go@v5
        with: { go-version: "${{ env.GO_VERSION }}", cache-dependency-path: go.sum }

      - if: steps.cache.outputs.hit == 'false'
        run: |
          CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
            go build -ldflags='-s -w' -trimpath -o app ./cmd/server
          mkdir -p _image/app && cp app _image/app/server
          tar -cf layer.tar -C _image .
          crane append --base gcr.io/distroless/static:nonroot \
            --new_layer layer.tar --new_tag "${{ steps.cache.outputs.src-tag }}"
          crane mutate "${{ steps.cache.outputs.src-tag }}" --entrypoint /app/server
          crane tag "${{ steps.cache.outputs.src-tag }}" ${{ github.sha }}
          crane tag "${{ steps.cache.outputs.src-tag }}" latest

      - uses: alfredtm/yeet-cache-action/sign@v1
        if: steps.cache.outputs.hit == 'false'
        with:
          src-tag: ${{ steps.cache.outputs.src-tag }}
          registry-password: ${{ secrets.GITHUB_TOKEN }}
```

Works the same in front of `ko`, `docker buildx`, `kaniko` — replace the build step, keep everything else.

## Inputs

| Input | Required | Description |
|---|---|---|
| `paths` | ✓ | Space-separated paths to hash via `git rev-parse HEAD:<path>`. |
| `image` | ✓ | OCI image repository **without tag** (e.g. `ghcr.io/owner/repo`). |
| `registry-password` | ✓ | Registry token. Usually `secrets.GITHUB_TOKEN`. |
| `extra` |  | Free-form string mixed into the hash. Include build flags, toolchain version, base image digest. |
| `sign` |  | `'true'` to verify cosign signature on cache hit. |
| `tags` |  | Comma-separated tags to apply on hit (e.g. `${{ github.sha }},latest`). |

Outputs: `hit`, `src-hash`, `src-tag`, `cached-tag`. Full list in [`action.yml`](./action.yml).

## Gotchas

1. **`paths` must be tracked by git.** The action hashes via `git rev-parse HEAD:<path>`. Untracked / `.gitignore`'d files are invisible. Commit your files before relying on the hash.

2. **`extra` matters.** Without it the cache is unsound: two builds with different `-ldflags` produce different binaries but hit the same cache key. **Always include** the toolchain version, build flags, and base image digest.

3. **Pin your base image by digest, not tag.** `gcr.io/distroless/static:nonroot` is mutable — Google updates it. If your `extra` references a digest (`base=gcr.io/distroless/static@sha256:...`), the cache correctly invalidates when the base changes. The minimal example above uses the tag for brevity; the real demo at [`alfredtm/yeeted`](https://github.com/alfredtm/yeeted) resolves the digest dynamically.

4. **Caller tags the built image with `src-tag` on miss.** The action computes `src-tag` but doesn't push to it — that's the build step's job (`--new_tag "${{ steps.cache.outputs.src-tag }}"`). Forget it, and the next run with the same source won't hit.

5. **`sign: 'true'` requires `permissions: id-token: write`.** Without it, cosign can't get an OIDC token and fails. Easy to forget.

6. **Signed verification rejects unsigned cache entries.** When you first enable `sign: 'true'`, any pre-existing unsigned `:src-<hash>` tags fail verification (correctly — they're untrusted). Bump `extra` once (e.g. `extra: migration=2026-05`) to invalidate the old cache and start fresh.

7. **Determinism is on you.** The action assumes identical inputs produce identical outputs. For Go: `-trimpath` + `-ldflags='-s -w'`. For other languages: set `SOURCE_DATE_EPOCH`. If your build embeds random IDs or timestamps, the cache still hits but the cached image may differ from a fresh build.

## License

MIT.
