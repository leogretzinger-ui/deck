# Image variants and derived images

One Dockerfile builds two image variants. Deck publishes no images: you
build the one you want from this checkout with Docker Compose. Both are the
same Deck — same server binary, same Noise tools, same runtime identity,
entrypoint, and healthcheck — and differ only in how many agent harnesses
they carry.

| Variant | Build target | Harnesses |
| --- | --- | --- |
| full (the default) | `runtime` | every supported harness |
| slim | `slim` | Claude Code and Codex |

Compose tags whichever variant it builds `deck:${NOISEDECK_IMAGE_TAG}`
(`deck:local` when the tag is unset).

Both carry Python and Node, the core shell and development utilities
(`git`, `gh`, `curl`, `jq`, `ripgrep`, `openssh-client`, `tmux`, `unzip`), the
Noise tools (`noise`, `noise-tasks`, `noise-visual-diff`, `noise-sdlc`), and
the browser integration — which is client-side only, because Chromium runs
as a separate sidecar service and no browser bytes are copied into either
image.

The full image additionally carries `build-essential` for native Python and
Node extensions. The slim image does not; it is a console, not a workbench.

Which harnesses an image contains is discovered, not declared: every
provider executable resolves from `PATH` at startup, so a harness the image
does not install is not registered and cannot be launched. No build flag and
no capability list separates the variants.

Neither image contains Go. Go is how Deck is *built*, in a disposable build
stage, not something a Deck user needs at runtime. The same reasoning
applies to Rust, Java, .NET, and project-specific SDKs.

```sh
# The full image, which is what a plain build produces.
docker compose --env-file deploy/compose.env up --build -d

# The slim image.
docker compose -f compose.yaml -f compose.slim.yaml \
  --env-file deploy/compose.env up --build -d
```

Stop the stack before switching between image targets.

## Project toolchains are derived images

A container cannot gain a layer at runtime, so an optional toolchain is not
something you can attach to a running Deck. There are three ways to
get one, and only one of them is reproducible and auditable:

1. **A derived image** — built on top of a Deck image from this checkout,
   and pinned like any other image. Preferred, and documented below.
2. Installing into a persistent workspace volume, which drifts and is not
   captured anywhere.
3. Mounting a toolchain from the host, which ties the container to one
   machine's contents.

A derived image inherits the entrypoint, healthcheck, and runtime identity,
so it drops into the same Compose service as the image it extends. Take root
to install and give it back, exactly as the Dockerfile's own overlay stages
do.

### Go

[`deploy/derived/go.Dockerfile`](../deploy/derived/go.Dockerfile) copies Go
from the same digest-pinned, multi-architecture image that builds Deck. The
toolchain is root-owned image content, `GOTOOLCHAIN=local` prevents implicit
toolchain downloads, and the build proves a runtime-user login shell can run a
real cgo link. It defaults to the full image because slim has no C compiler and
cannot build Deck's SQLite-backed packages.

Build and run it with the checked-in overlay:

```sh
NOISEDECK_IMAGE_TAG=go-local \
  docker compose -f compose.yaml -f compose.go.yaml \
    --env-file deploy/compose.env up --build -d
```

The overlay builds the full image from this checkout as the base, in the same
BuildKit graph, so no intermediate tag is needed.
`NOISEDECK_DERIVED_BASE_CONTEXT` names a different base context, for example
`docker-image://deck:mybase` for an image you built and tagged yourself. When
the base is another capability built from this checkout, use the second
checked-in overlay instead:

```sh
NOISEDECK_IMAGE_TAG=mempalace-go \
  docker compose \
    -f compose.yaml \
    -f compose.mempalace.yaml \
    -f compose.go.yaml \
    -f compose.go-mempalace.yaml \
    --env-file deploy/compose.env build app
```

`compose.go-mempalace.yaml` exposes the MemPalace target as a build-only
service and binds it to Go's named `deck-base` context. Compose therefore
builds both capabilities in one BuildKit graph; no intermediate image tag has
to cross builder boundaries. The production wrapper uses this graph and then
certifies both capabilities in the running container.

### Rust

Written and checked in: `deploy/derived/rust.Dockerfile` adds a
digest-pinned Rust toolchain and Tauri's published Debian prerequisites.

```sh
# Build the full image first; Compose tags it deck:local.
docker compose --env-file deploy/compose.env build app
docker build -f deploy/derived/rust.Dockerfile -t deck:rust .
```

`--build-arg DECK_IMAGE=<tag>` selects a different base image.

Build it from the full image rather than slim: a Rust build links against
the native-extension support only the full image carries.

### MemPalace on slim

MemPalace is built in this repository as the `mempalace` target, which
extends the **full** image. There is no slim + MemPalace target. A slim
deployment that wants durable memory uses a derived image, the same answer as
for Go and Rust: build the slim image, then take the MemPalace stage's
instructions from this repository's Dockerfile with that image as the base.
Ask for a slim + MemPalace target if the need turns out to be common; the
reason there is not one is that nobody has needed it yet, not that it
cannot exist.
