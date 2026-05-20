# yeet-cache-action

**Skip CI image builds when source hasn't changed.** Hashes your build inputs, asks your registry *"do you already have an image for this hash?"*, and on a hit retags the cached image in ~1.5 seconds of actual work. Works in front of any image builder. Signs new cache entries with GitHub-native attestation and verifies them on cache hit to prevent registry-tag spoofing.

Ships as a Node 20 GitHub Action with a bundled Go helper (`yeet-pack`) that builds OCI images in memory — no Docker daemon, no Dockerfile, no `crane append + crane mutate` round-trips.

See [SPEC.md](./SPEC.md) for the `:src-<hex>` tag convention.

## Comparison times

Measured on `ubuntu-latest` against a Go service with realistic deps (chi, pgx, prometheus, otel):

| Workflow | Build (cache miss) | No-op push (cache hit) |
|---|---|---|
| `docker buildx` + GHA cache | 66s | 66s (re-validates layers) |
| `ko publish` | 20s | 20s (no whole-build cache) |
| **`yeet-cache-action@v2`** | **22s** | **~16s wall, ~1.5s inner work** |

The 16s wall on cache hit is essentially the GitHub-hosted runner platform floor (job startup + action download + step transitions). The action itself does sub-2 seconds of real work — hashing, checking, retagging.

## Usage

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
      GO_VERSION: "1.22"
      LDFLAGS: "-s -w"
      # Pin base by digest. Bump intentionally to roll the cache + pick up upstream updates.
      BASE_IMAGE: "gcr.io/distroless/static@sha256:963fa6c5..."
    steps:
      - name: Compute source hash via GitHub API
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
          EXTRA="go=${{ env.GO_VERSION }} base=${{ env.BASE_IMAGE }} ldflags=${{ env.LDFLAGS }}"
          HASH=$(printf '%sextra:%s\n' "$HASHES" "$EXTRA" | sha256sum | cut -c1-12)
          echo "hash=${HASH}" >> "$GITHUB_OUTPUT"

      - uses: alfredtm/yeet-cache-action@v2
        id: cache
        with:
          hash: ${{ steps.hash.outputs.hash }}
          image: ${{ env.IMAGE }}
          registry-password: ${{ secrets.GITHUB_TOKEN }}
          sign: 'true'
          verify-on-hit: 'false'    # trust-on-first-use; saves ~3-4s per no-op push
          tags: ${{ github.sha }},latest

      # ---- CACHE MISS PATH (nothing below runs on a hit) ----

      - uses: actions/checkout@v4
        if: steps.cache.outputs.hit == 'false'
        with:
          fetch-depth: 1
          sparse-checkout: |
            cmd
            internal
            go.mod
            go.sum
          sparse-checkout-cone-mode: false

      - uses: actions/setup-go@v5
        if: steps.cache.outputs.hit == 'false'
        with: { go-version: "${{ env.GO_VERSION }}", cache-dependency-path: go.sum }

      - if: steps.cache.outputs.hit == 'false'
        run: |
          CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
            go build -ldflags="${{ env.LDFLAGS }}" -trimpath -o app ./cmd/server

          # One round-trip: append layer + set entrypoint + push to all tags
          yeet-pack pack \
            --binary app \
            --binary-path-in-image /app/server \
            --entrypoint /app/server \
            --base "${{ env.BASE_IMAGE }}" \
            --tag "${{ steps.cache.outputs.src-tag }}" \
            --also-tag "${{ github.sha }},latest"
```

That's the whole workflow. No separate sign step — the action's `post:` hook auto-attests built images via `@actions/attest`. No `crane append + crane mutate` round-trips — `yeet-pack pack` does it in memory.

### What about non-Go builds?

`yeet-pack pack` is Go-specific (it expects a single static binary). For other build tools, replace just that one command with whatever you use — `crane append + mutate`, `ko publish`, `docker buildx build`, `kaniko`, etc. The cache check, signing, and retag logic in the action stays the same regardless. Just push your built image to `${{ steps.cache.outputs.src-tag }}` so the next run hits.

## Inputs

| Input | Required | Description |
|---|---|---|
| `image` | ✓ | OCI image repository **without tag** (e.g. `ghcr.io/owner/repo`). |
| `registry-password` | ✓ | Registry token. Usually `secrets.GITHUB_TOKEN`. |
| `paths` | * | Space-separated paths to hash via `git rev-parse HEAD:<path>`. Requires checkout. |
| `hash` | * | Pre-computed source hash. Use this to **skip checkout on cache hit**. |
| `extra` |  | Free-form string mixed into the hash. Include build flags, toolchain version, base image digest. Ignored when `hash` is provided (caller bakes extra into the hash). |
| `sign` |  | `'true'` to auto-attest built images on cache miss (post hook) and verify on cache hit. Requires `permissions: { id-token: write, attestations: write }`. |
| `verify-on-hit` |  | Default `'true'` (strict). Set to `'false'` to skip attestation verification on cache hit (trust-on-first-use) — saves 3-4s per no-op push. |
| `tags` |  | Comma-separated tags to apply on hit (e.g. `${{ github.sha }},latest`). |
| `verify-identity` |  | Regex for the workflow identity. Defaults to any workflow in `$GITHUB_REPOSITORY`. |

*Either `paths` or `hash` is required, not both.*

Outputs: `hit`, `src-hash`, `src-tag`, `cached-tag`. Full list in [`action.yml`](./action.yml).

## Gotchas

1. **`paths` requires checkout. `hash` doesn't.** Use `hash` (computed via `gh api`) when you want to skip the working tree clone on cache hits. The example above does this — `actions/checkout@v4` only runs on cache miss.

2. **`extra` matters.** Without it the cache is unsound: two builds with different `-ldflags` produce different binaries but hit the same cache key. Always include toolchain version, build flags, and base image digest. When using the `hash` input, **bake `extra` into your hash computation upstream** (see the `EXTRA=` line in the example) — the action's `extra` input is ignored when `hash` is provided.

3. **Pin your base image by digest, not tag.** `gcr.io/distroless/static:nonroot` is mutable — Google updates it. Pin to `gcr.io/distroless/static@sha256:...` and include the digest in your hash. Bump intentionally to roll the cache.

4. **Caller pushes the built image to `src-tag` on cache miss.** The action computes `src-tag` but doesn't build/push — that's the build step's job. With `yeet-pack pack --tag "${{ steps.cache.outputs.src-tag }}"`, this is one line. Forget it, and the next run won't hit.

5. **`sign: 'true'` requires both `id-token: write` and `attestations: write`.** Without them, GitHub-native attestation fails. Easy to forget; the failure message is unhelpful.

6. **Cache hits across signing-backend changes will fail loud.** v2 verifies GitHub attestations; v1 used cosign. If you migrate from v1 to v2, any existing `:src-<hash>` tags that were cosign-signed will fail verification under v2. Bump `extra` once (`extra: migration=v2`) to invalidate the old cache and start fresh.

7. **`verify-on-hit: 'false'` trades security for speed.** The post-hook attestation still fires on cache miss (so downstream consumers can verify), but the action itself doesn't re-verify on hit. Acceptable when you trust your registry's write-access controls; not acceptable if you're worried about supply-chain attacks on the registry itself.

8. **Determinism is on you.** The action assumes identical inputs produce identical outputs. For Go: `-trimpath` + `-ldflags='-s -w'`. For other toolchains: set `SOURCE_DATE_EPOCH`. If your build embeds random IDs, the cache still hits — but the cached image may differ from a fresh build.

## Architecture

`yeet-cache-action@v2` is a Node 20 GitHub Action with:

- `dist/main.js` — main entry. Hashes inputs (via API or `yeet-pack hash`), checks the registry, verifies attestation on hit (if enabled), retags via parallel `crane tag` calls.
- `dist/post.js` — post hook. On cache miss, calls `@actions/attest`'s `attestProvenance()` to generate a SLSA v1.0 provenance attestation for the built image.
- `dist/yeet-pack-linux-amd64` — bundled 7MB Go binary that copies itself to `/usr/local/bin/yeet-pack` so caller workflows can use it for `yeet-pack pack` (in-memory image build) and `yeet-pack hash` (content-address computation). Uses [go-containerregistry](https://github.com/google/go-containerregistry) as a library.

Verification on cache hit uses `gh attestation verify oci://...` — the gh CLI is pre-installed on GitHub-hosted runners, so no separate cosign install is needed.

## v1 (composite, cosign-based)

The v1 line is still maintained at `@v1` for users who want the smaller composite action (no JS, no Go binary download) and don't mind using cosign for signing. v1.3 has roughly:

- Cache miss: ~28-30s (slower; cosign Rekor upload dominates unless `tlog-upload: 'false'`)
- Cache hit: ~12s (faster; smaller action download, cosign verify is leaner than GH attestation)

If you have lots of no-op pushes and don't need GitHub-native attestation, v1 is a reasonable choice. If you care about miss-path speed and want GitHub-native supply-chain integration, use v2.

## License

MIT.
