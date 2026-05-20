# yeet-cache-action

**Skip CI image builds entirely when nothing has changed.** A GitHub Action that uses your OCI registry as a content-addressed cache — if you've built an image from these exact source files before, it retags the existing image and exits in ~1 second.

```
docker workflow:        66s
ko workflow:            20s
crane workflow (cold):  ~10s
crane workflow + yeet:  1.2s   ← when source is unchanged
```

Works in front of any image builder: `crane`, `ko`, `docker buildx`, `kaniko`, `buildah`.

---

## How it works

Most CI systems cache *layers* or *steps*. `yeet-cache-action` caches the *entire build output* by asking your registry: "do you already have an image built from these exact files?"

1. Hash the configured source paths using `git rev-parse HEAD:<path>` — Git already content-addresses every tree, so this is ~milliseconds and 100% deterministic.
2. Check whether `${image}:src-<hash>` exists in the registry (one HTTP HEAD).
3. **Hit**: retag the cached image to your release tags. Done. The action exits in ~1 second.
4. **Miss**: output `hit=false` and the computed hash. The caller builds the image, pushes it, and tags it `:src-<hash>` so the next run with the same source hits.

Because builds of stateless code are deterministic, the result of the build is fully determined by its inputs. If the inputs haven't changed, the output is already in the registry. Don't rebuild it — *look it up.*

## Usage

### Minimal example (with `crane` for the build itself)

```yaml
name: build
on: push

permissions:
  contents: read
  packages: write

jobs:
  build:
    runs-on: ubuntu-latest
    env:
      IMAGE: ghcr.io/${{ github.repository }}
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 1 }

      - uses: alfredtm/yeet-cache-action@v1
        id: cache
        with:
          paths: cmd internal go.mod go.sum
          image: ${{ env.IMAGE }}
          registry-password: ${{ secrets.GITHUB_TOKEN }}
          tags: ${{ github.sha }},latest

      # Everything below this point runs ONLY on cache miss.
      - uses: actions/setup-go@v5
        if: steps.cache.outputs.hit == 'false'
        with: { go-version: '1.22', cache-dependency-path: go.sum }

      - name: Build and push (only on cache miss)
        if: steps.cache.outputs.hit == 'false'
        run: |
          CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
            go build -ldflags='-s -w' -trimpath -o app ./cmd/server

          mkdir -p _image/app && cp app _image/app/server
          tar -cf layer.tar -C _image .

          # Tag the new image as src-<hash> so the NEXT run hits the cache.
          crane append \
            --base gcr.io/distroless/static:nonroot \
            --new_layer layer.tar \
            --new_tag "${{ steps.cache.outputs.src-tag }}"
          crane mutate "${{ steps.cache.outputs.src-tag }}" --entrypoint /app/server

          # Also tag for release.
          crane tag "${{ steps.cache.outputs.src-tag }}" ${{ github.sha }}
          crane tag "${{ steps.cache.outputs.src-tag }}" latest
```

### With `ko`

```yaml
- uses: alfredtm/yeet-cache-action@v1
  id: cache
  with:
    paths: cmd internal go.mod go.sum
    image: ghcr.io/${{ github.repository }}/server
    registry-password: ${{ secrets.GITHUB_TOKEN }}
    tags: ${{ github.sha }},latest

- uses: actions/setup-go@v5
  if: steps.cache.outputs.hit == 'false'
  with: { go-version: '1.22' }

- uses: ko-build/setup-ko@v0.7
  if: steps.cache.outputs.hit == 'false'

- name: ko publish + tag for cache
  if: steps.cache.outputs.hit == 'false'
  env:
    KO_DOCKER_REPO: ghcr.io/${{ github.repository_owner }}
  run: |
    ko publish --base-import-paths \
      --tags "src-${{ steps.cache.outputs.src-hash }},${{ github.sha }},latest" \
      ./cmd/server
```

### With `docker buildx`

```yaml
- uses: alfredtm/yeet-cache-action@v1
  id: cache
  with:
    paths: . Dockerfile
    image: ghcr.io/${{ github.repository }}
    registry-password: ${{ secrets.GITHUB_TOKEN }}
    tags: ${{ github.sha }},latest

- uses: docker/setup-buildx-action@v3
  if: steps.cache.outputs.hit == 'false'

- uses: docker/login-action@v3
  if: steps.cache.outputs.hit == 'false'
  with:
    registry: ghcr.io
    username: ${{ github.actor }}
    password: ${{ secrets.GITHUB_TOKEN }}

- uses: docker/build-push-action@v6
  if: steps.cache.outputs.hit == 'false'
  with:
    context: .
    push: true
    tags: |
      ${{ steps.cache.outputs.src-tag }}
      ghcr.io/${{ github.repository }}:${{ github.sha }}
      ghcr.io/${{ github.repository }}:latest
```

## Inputs

| Input | Required | Default | Description |
|---|---|---|---|
| `paths` | ✓ | — | Space-separated list of paths to hash. Use the paths whose contents determine the build output (e.g. `cmd internal go.mod go.sum`). Each path must exist in `HEAD`. |
| `image` | ✓ | — | OCI image repository **without tag** (e.g. `ghcr.io/owner/repo`). |
| `registry-password` | ✓ | — | Token for registry login. Usually `${{ secrets.GITHUB_TOKEN }}`. |
| `registry` |  | `ghcr.io` | Registry hostname. |
| `registry-username` |  | `${{ github.actor }}` | Registry username. |
| `tags` |  | — | Comma-separated tags to retag the cached image to on a hit (e.g. `${{ github.sha }},latest`). Skipped on cache miss — caller is responsible for tagging on miss. |
| `crane-version` |  | `latest` | Pinned crane release (e.g. `v0.20.2`) or `latest`. |

## Outputs

| Output | Description |
|---|---|
| `hit` | `"true"` if the cache contained an image for this source hash. |
| `src-hash` | The 12-char content-address of the configured paths. |
| `src-tag` | The full `<image>:src-<hash>` reference. Tag your freshly-built image with this on a cache miss. |
| `cached-tag` | Same as `src-tag`, but only set on cache hit. Useful for `crane copy` / `crane mutate` workflows. |

## Important: populating the cache on misses

On a cache miss, **the caller must tag the freshly-built image with the `src-tag` output**. Otherwise the next run with identical sources will miss too. The simplest pattern: pass `${{ steps.cache.outputs.src-tag }}` as one of the build's output tags, or run `crane tag <new-image> ${{ steps.cache.outputs.src-tag }}` after pushing.

## What `paths` should I hash?

Hash the inputs that determine the build output. For a Go service that's typically:

- `cmd` and `internal` (or wherever your source lives)
- `go.mod` and `go.sum` (dependency manifest)

Do **not** hash:
- The Dockerfile, if your build is Dockerfile-less. (For Dockerfile-based builds, do hash it.)
- The `.github/workflows/` directory. (Workflow changes shouldn't invalidate builds.)
- READMEs, docs, k8s manifests — these don't affect the binary.

Including too much defeats the purpose (you'll cache-miss on doc edits). Including too little is dangerous (you'll cache-hit on stale source).

## Determinism caveat

This pattern assumes builds of unchanged source produce equivalent images. For Go that means using `-trimpath` and `-ldflags='-s -w'`. For other languages, set `SOURCE_DATE_EPOCH` and any other knobs your toolchain needs to produce reproducible output. If your build is non-deterministic (e.g. embeds timestamps or random IDs), this action will still skip — but the cached image may differ from what a fresh build would produce. That's usually fine; if it isn't, don't use this action.

## License

MIT — see [LICENSE](./LICENSE).
