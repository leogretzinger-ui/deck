# deck:rust — a derived image adding a Rust toolchain and Tauri's Linux
# prerequisites on top of a Deck image built from this checkout.
#
# This is a derived image rather than a stage in Deck's own Dockerfile because
# a running container cannot be given a layer: the choices are a derived
# image, something installed into a persistent workspace, or a host mount. A
# derived image is the one of those three that stays reproducible and
# auditable, which is why optional toolchains live here.
#
#   docker compose --env-file deploy/compose.env build app   # tags deck:local
#   docker build -f deploy/derived/rust.Dockerfile -t deck:rust .
#
# Build it FROM the full image, not slim: slim carries neither the compiler
# nor the native-extension support a Rust build links against.
ARG DECK_IMAGE=deck:local

# The toolchain is copied from a digest-pinned image rather than installed by
# piping rustup over the network, so it is content with a known provenance and
# a rebuild resolves to the same bytes.
FROM rust:1.98.0-bookworm@sha256:82150a52ec202c1b14d7817e14516c392bb7f5cfebd88f1ed531cb37ebd39922 AS rust-toolchain

FROM ${DECK_IMAGE}

ARG NOISEDECK_RUNTIME_UID=1026
ARG NOISEDECK_RUNTIME_GID=100
ARG RUST_VERSION=1.98.0

USER root
# Tauri's own published Debian prerequisites. A missing one fails at a
# project's build rather than here, so the list is kept whole rather than
# trimmed to what a particular project happened to need.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
      libwebkit2gtk-4.1-dev \
      build-essential \
      curl \
      wget \
      file \
      libxdo-dev \
      libssl-dev \
      libayatana-appindicator3-dev \
      librsvg2-dev \
    && rm -rf /var/lib/apt/lists/*

COPY --from=rust-toolchain /usr/local/rustup /usr/local/rustup
COPY --from=rust-toolchain /usr/local/cargo /usr/local/cargo
ENV RUSTUP_HOME=/usr/local/rustup \
    CARGO_HOME=/usr/local/cargo \
    PATH=/usr/local/cargo/bin:${PATH}
COPY --chmod=0444 deploy/derived/rust-profile.sh /etc/profile.d/noisedeck-rust.sh

# The upstream rust image runs `chmod -R a+w` over both trees so that any
# user can rustup-update in place. Copying it preserves those modes, which
# would leave the compiler writable by every session in this image - so the
# modes are normalised here rather than assumed from the ownership of the
# cache directories created below.
#
# Cargo still writes into CARGO_HOME when it builds, so the session user owns
# exactly the registry and git caches - and the two lock files cargo keeps at
# the CARGO_HOME root. Those are pre-created rather than making the root
# directory writable: write permission on a directory governs creating and
# removing its entries, so a writable CARGO_HOME would let a session replace
# bin/ wholesale and defeat the point. Cargo opens these locks, it does not
# need to create them, and without them it warns that the home is read-only
# and proceeds WITHOUT file locking - which lets parallel cargo processes
# mutate the shared caches unsynchronised.
RUN chown -R root:root /usr/local/rustup /usr/local/cargo \
    && chmod -R go-w /usr/local/rustup /usr/local/cargo \
    && install -d -m 0755 -o "${NOISEDECK_RUNTIME_UID}" -g "${NOISEDECK_RUNTIME_GID}" \
      /usr/local/cargo/registry /usr/local/cargo/git \
    && install -m 0644 -o "${NOISEDECK_RUNTIME_UID}" -g "${NOISEDECK_RUNTIME_GID}" \
      /dev/null /usr/local/cargo/.package-cache \
    && install -m 0644 -o "${NOISEDECK_RUNTIME_UID}" -g "${NOISEDECK_RUNTIME_GID}" \
      /dev/null /usr/local/cargo/.package-cache-mutate \
    && test "$(rustc --version | awk '{print $2}')" = "${RUST_VERSION}" \
    && test "$(cargo --version | awk '{print $2}')" = "${RUST_VERSION}" \
    && command -v cc \
    && pkg-config --exists webkit2gtk-4.1

USER ${NOISEDECK_RUNTIME_UID}:${NOISEDECK_RUNTIME_GID}
# Reaching the toolchain through a login shell is the contract that matters:
# Debian's /etc/profile replaces the image PATH for non-root login shells.
RUN test "$(/bin/bash -lc 'command -v cargo')" = "/usr/local/cargo/bin/cargo" \
    && test "$(/bin/bash -lc 'rustc --version' | awk '{print $2}')" = "${RUST_VERSION}" \
    && test -w /usr/local/cargo/registry \
    && test -w /usr/local/cargo/git \
    && test -w /usr/local/cargo/.package-cache \
    && test -w /usr/local/cargo/.package-cache-mutate \
    && test ! -w /usr/local/cargo \
    && test ! -w /usr/local/cargo/bin/cargo \
    && test ! -w /usr/local/cargo/bin \
    && test ! -w /usr/local/rustup \
    && rustc_real="$(readlink -f "$(rustup which rustc)")" \
    && test -x "$rustc_real" \
    && case "$rustc_real" in /usr/local/rustup/toolchains/*) ;; \
         *) echo "rustc did not resolve into the toolchain tree: $rustc_real" >&2; exit 1 ;; esac \
    && test ! -w "$rustc_real" \
    && test ! -w "$(dirname "$rustc_real")" \
    && cargo_probe="$(mktemp -d)" \
    && mkdir -p "$cargo_probe/src" \
    && printf '[package]\nname = "probe"\nversion = "0.0.0"\nedition = "2021"\n' > "$cargo_probe/Cargo.toml" \
    && printf 'fn main() {}\n' > "$cargo_probe/src/main.rs" \
    && cargo build --offline --quiet --manifest-path "$cargo_probe/Cargo.toml" 2>"$cargo_probe/err" \
    && ! grep -qi 'read-only' "$cargo_probe/err" \
    && rm -rf "$cargo_probe"
