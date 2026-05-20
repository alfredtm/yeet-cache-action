# yeet-cache-action

**Skip CI image builds when source hasn't changed.** Hashes your build inputs, asks your registry *"do you already have an image for this hash?"*, and on a hit retags the cached image in ~1 second. Works in front of any image builder. Optionally verifies cosign signatures on cache hits to prevent registry-tag spoofing.

See [SPEC.md](./SPEC.md) for the `:src-<hex>` tag convention.

## Comparison times

Measured on `ubuntu-latest` against a Go service with realistic deps (chi, pgx, prometheus, otel):

| Workflow | Build (cache miss) | No-op push (cache hit) |
|---|---|---|
| `docker buildx` + GHA cache | **66s** | 66s (re-validates layers) |
| `ko publish` | **20s** | 20s (no whole-build cache) |
| `crane append` + `yeet-cache@v1.1` (git checkout) | **~50s** | 23s |
| `crane append` + `yeet-cache@v1.2` (API hash, no checkout) | **~50s** | **~13s** |

`yeet-cache` is the only one of these that skips entirely when source is unchanged. With the v1.2 `hash` input you can compute the hash via GitHub's git API before checkout and skip the working tree clone on cache hits — about 10 seconds saved on every no-op push.

## Usage

### Fast path: API-computed hash, no checkout on cache hit

```yaml
name: build
on: push

permissions:
  contents: read
  packages: write
  id-token: write

jobs:
  build:
    runs-on: ubuntu-latest
    env:
      IMAGE: ghcr.io/${{ github.repository }}
      GO_VERSION: "1.22"
      # Pin base by digest. Bump intentionally to roll the cache.
      BASE_IMAGE: "gcr.io/distroless/static@sha256:963fa6c5..."
    steps:
      - name: Compute source hash via API
        id: hash
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          TREE=$(gh api "repos/${GITHUB_REPOSITORY}/git/commits/${GITHUB_SHA}" -q '.tree.sha')
          JSON=$(gh api "repos/${GITHUB_REPOSITORY}/git/trees/${TREE}")
          HASHES=""
          for p in cmd internal go.mod go.sum; do
            SHA=$(echo "$JSON" | jq -r ".tree[] | select(.path == \"$p\") | .sha")
            HASHES="${HASHES}${SHA}"$'\n'
          done
          echo "hash=$(printf '%s' "$HASHES" | sha256sum | cut -c1-12)" >> "$GITHUB_OUTPUT"

      - uses: alfredtm/yeet-cache-action@v1
        id: cache
        with:
          hash: ${{ steps.hash.outputs.hash }}
          extra: go=${{ env.GO_VERSION }} base=${{ env.BASE_IMAGE }} ldflags=-s -w
          image: ${{ env.IMAGE }}
          registry-password: ${{ secrets.GITHUB_TOKEN }}
          sign: 'true'
          tags: ${{ github.sha }},latest

      # CACHE MISS PATH below — none of this runs on a hit.
      - uses: actions/checkout@v4
        if: steps.cache.outputs.hit == 'false'

      - uses: actions/setup-go@v5
        if: steps.cache.outputs.hit == 'false'
        with: { go-version: "${{ env.GO_VERSION }}", cache-dependency-path: go.sum }

      - if: steps.cache.outputs.hit == 'false'
        run: |
          CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
            go build -ldflags='-s -w' -trimpath -o app ./cmd/server
          mkdir -p _image/app && cp app _image/app/server
          tar -cf layer.tar -C _image .
          crane append --base "${{ env.BASE_IMAGE }}" \
            --new_layer layer.tar --new_tag "${{ steps.cache.outputs.src-tag }}"
          crane mutate "${{ steps.cache.outputs.src-tag }}" --entrypoint /app/server
          crane tag "${{ steps.cache.outputs.src-tag }}" ${{ github.sha }} &
          crane tag "${{ steps.cache.outputs.src-tag }}" latest &
          wait

      - uses: alfredtm/yeet-cache-action/sign@v1
        if: steps.cache.outputs.hit == 'false'
        with:
          src-tag: ${{ steps.cache.outputs.src-tag }}
          registry-password: ${{ secrets.GITHUB_TOKEN }}
```

### Simple path: use `paths`, requires checkout

```yaml
- uses: actions/checkout@v4
- uses: alfredtm/yeet-cache-action@v1
  id: cache
  with:
    paths: cmd internal go.mod go.sum
    extra: go=1.22 ldflags=-s -w
    image: ghcr.io/${{ github.repository }}
    registry-password: ${{ secrets.GITHUB_TOKEN }}
    sign: 'true'
    tags: ${{ github.sha }},latest
```

Use `paths` for simplicity, `hash` for speed. They're mutually exclusive.

## Inputs

| Input | Required | Description |
|---|---|---|
| `image` | ✓ | OCI image repository **without tag** (e.g. `ghcr.io/owner/repo`). |
| `registry-password` | ✓ | Registry token. Usually `secrets.GITHUB_TOKEN`. |
| `paths` | * | Space-separated paths to hash via `git rev-parse HEAD:<path>`. Requires checkout. |
| `hash` | * | Pre-computed source hash. Use this to **skip checkout on cache hit**. Combined with `extra` to form the final cache key. |
| `extra` |  | Free-form string mixed into the hash. Include build flags, toolchain version, base image digest. |
| `sign` |  | `'true'` to verify cosign signature on cache hit. |
| `tags` |  | Comma-separated tags to apply on hit (e.g. `${{ github.sha }},latest`). |

*Either `paths` or `hash` is required, not both.*

Outputs: `hit`, `src-hash`, `src-tag`, `cached-tag`. Full list in [`action.yml`](./action.yml).

## Gotchas

1. **`paths` requires checkout. `hash` doesn't.** Use `hash` (computed via `gh api`) when you want to skip the working tree clone on cache hits — saves ~3 seconds per no-op push.

2. **`extra` matters.** Without it the cache is unsound: two builds with different `-ldflags` produce different binaries but get the same cache key. **Always include** toolchain version, build flags, and base image digest.

3. **Pin your base image by digest, not tag.** `gcr.io/distroless/static:nonroot` is mutable — Google updates it. If your `extra` includes `base=gcr.io/distroless/static@sha256:...`, the cache correctly invalidates when the base changes and you bump the digest.

4. **Caller tags the built image with `src-tag` on miss.** The action computes `src-tag` but doesn't push to it — that's the build step's job (`--new_tag "${{ steps.cache.outputs.src-tag }}"`). Forget it and the next run won't hit.

5. **`sign: 'true'` requires `permissions: id-token: write`.** Without it, cosign can't get an OIDC token and signing fails. Easy to forget.

6. **Signed verification rejects unsigned cache entries.** When you first enable `sign: 'true'`, any pre-existing unsigned `:src-<hash>` tags fail verification (correctly). Bump `extra` once (`extra: migration=2026-05`) to invalidate the old cache.

7. **Determinism is on you.** The action assumes identical inputs produce identical outputs. For Go: `-trimpath` + `-ldflags='-s -w'`. For other toolchains: set `SOURCE_DATE_EPOCH`. If your build embeds random IDs, the cache still hits — but the cached image may differ from a fresh build.

## License

MIT.
