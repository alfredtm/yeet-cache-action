# `src-<hex>` Tag Specification

**Status**: Draft v0.1 · **Author**: Alfred Tovsen Mo · **Date**: 2026-05-20

This document specifies a conventional OCI image tag scheme for content-addressed build caches. It is intended to enable interoperability between independently-developed build tools (e.g. `crane`, `ko`, `kaniko`, `docker buildx`, custom builders) and cache tools (e.g. `yeet-cache-action`, future implementations) so that a build cache populated by one tool can be consumed by another.

## Summary

> A container image tagged `<image>:src-<hex>` SHOULD be a build whose canonical input hash is `<hex>`. Consumers can rely on the presence of such a tag to skip rebuilds when their own computed input hash matches.

## Tag format

A tag MUST match the regex:

```
^src-[0-9a-f]{8,64}$
```

- The prefix is the literal string `src-`.
- The hex portion is lowercase ASCII hex.
- 12 characters is RECOMMENDED as a reasonable balance between collision resistance and tag readability.
- Implementations MAY use a longer hex value if they desire stronger collision resistance.

## Hash computation

The hex value is the leading hex digits of a SHA-256 of the build's canonical input set. Implementations are free to choose what goes into the input set, but the following are RECOMMENDED, in this order, separated by newlines:

1. **Source content addresses.** For Git-managed projects, the tree hash returned by `git rev-parse HEAD:<path>` for each declared source path, in the order the paths were declared.
2. **A user-supplied "extra" string.** Free-form text capturing additional inputs that affect build output but are not files: language toolchain version, build flags, target OS/arch, base image digest, environment variables that affect compilation.

```
sha256(
  git_tree_hash(path_1) "\n"
  git_tree_hash(path_2) "\n"
  ...
  "extra:" extra_string "\n"
)[:N]
```

where `N` is the chosen hex prefix length.

### Why git tree hashes?

Git already content-addresses every tree and blob in the repository (using SHA-1, which is sufficient for collision resistance in this context). Reading a tree hash with `git rev-parse HEAD:<path>` is O(milliseconds) and 100% deterministic — no need to re-walk and rehash files. Falling back to file content hashing is permitted for non-Git projects.

### Why include "extra"?

Two builds with identical source files but different compiler flags, language versions, or base images produce different output. The hash must include those to be a sound cache key. Implementations MUST allow callers to mix in such metadata.

## Semantics

When a `src-<hex>` tag points to a manifest in the registry, it asserts:

> *"There exists a build of this hash, and the resulting image is what this tag points to."*

It does NOT assert:

- That the image was built by a trusted party. **Verify provenance separately** (see § Provenance).
- That the image is currently the recommended release. The tag is a cache marker, not a release pointer.
- That the input hash is globally unique. Different projects with different conventions will produce different hashes for the same logical source.

## Consumer behavior

A cache-consuming tool SHOULD:

1. Compute its own input hash using the same algorithm and inputs.
2. Issue a manifest existence check (e.g. `HEAD /v2/<repo>/manifests/src-<hex>`) against the registry.
3. On a hit, OPTIONALLY verify provenance (see below) and then either:
   - Retag the cached image to release tags (e.g. `<sha>`, `latest`), or
   - Skip its build pipeline entirely.
4. On a miss, run the full build, push the result, and tag it `src-<hex>` so that future consumers may hit.

## Provenance

Cache poisoning is a real concern: anyone with write access to a registry can publish a manifest at any tag. A `src-<hex>` tag alone is not authoritative.

**Implementations SHOULD support signed cache entries** using sigstore/cosign keyless signing. A cache producer signs `<image>:src-<hex>` with cosign; a cache consumer verifies the signature against an expected workflow identity before retagging.

The reference implementation (`yeet-cache-action/sign@v1`) uses keyless cosign with the GitHub Actions OIDC issuer (`token.actions.githubusercontent.com`) and verifies against the workflow identity regex `^https://github.com/<owner>/<repo>/.github/workflows/.+$` by default.

## Determinism caveats

This scheme assumes that, given identical inputs, the build produces identical (or substantially equivalent) output. To approximate this in Go:

- `-trimpath` strips local filesystem paths from the binary
- `-ldflags='-s -w'` strips DWARF symbols and timestamps
- `SOURCE_DATE_EPOCH` set to a stable value (e.g. the commit timestamp)
- `CGO_ENABLED=0` for fully static binaries

Other languages have analogous knobs. Producers MUST declare any sources of non-determinism in their extra string or accept that cache hits may serve outputs that differ from a fresh build.

## Reference implementation

[`alfredtm/yeet-cache-action`](https://github.com/alfredtm/yeet-cache-action) is the reference implementation. It is intentionally small (~200 lines of composite-action YAML + shell) so that other tools can adopt the scheme without taking a dependency on this implementation.

## Compatibility

This scheme uses standard OCI tags only. No registry, image format, or runtime extensions are required. Tags coexist freely with arbitrary other tags (release tags, build tags, signatures).

## Open questions

- Should the spec recommend a fixed hex length (e.g. 12) to make implementations interoperable, or leave it implementation-defined?
- Should the spec recommend a fixed canonicalization of the "extra" string (e.g. sorted `key=value;key=value`) for cross-implementation cache sharing?
- Should provenance verification become MUST rather than SHOULD in a future revision?

Feedback welcome — open an issue at https://github.com/alfredtm/yeet-cache-action.
