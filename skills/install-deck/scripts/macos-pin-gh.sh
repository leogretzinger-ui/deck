#!/bin/sh
# Install the GitHub CLI release pinned in the Deck Dockerfile into a private tools directory.
set +x
set -eu
fail() { printf '%s\n' "$1" >&2; exit 1; }
[ "$#" -eq 2 ] || fail 'Pass the verified Deck checkout and the absolute private tools directory.'
checkout=$1
tools=$2
case "$checkout" in /*) ;; *) fail 'Checkout path must be absolute.' ;; esac
case "$tools" in /*) ;; *) fail 'Tools directory must be absolute.' ;; esac
[ -f "$checkout/Dockerfile" ] || fail 'No Dockerfile in the Deck checkout.'
[ -d "$tools" ] && [ ! -L "$tools" ] || fail 'The private tools directory must already exist and not be a symlink.'
[ ! -e "$tools/gh" ] || fail 'The tools directory already has a gh; remove it first to replace it.'

pin() {
  value=$(sed -n "s/^ARG $1=\([^[:space:]]*\)\$/\1/p" "$checkout/Dockerfile")
  [ -n "$value" ] && [ "$(printf '%s\n' "$value" | wc -l)" -eq 1 ] || fail "Dockerfile must declare $1 exactly once."
  printf '%s\n' "$value"
}
version=$(pin GH_CLI_VERSION)
want=$(pin GH_CLI_SHA256_DARWIN_ARM64)
case "$version" in *[!0-9.]*|'') fail 'Pinned gh version is not a plain version number.' ;; esac
case "$want" in *[!0-9a-f]*) fail 'Pinned gh checksum is not lowercase hex.' ;; esac
[ "${#want}" -eq 64 ] || fail 'Pinned gh checksum must be 64 hex characters.'

sha256() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1"; else sha256sum "$1"; fi | awk '{ print $1 }'
}

work=$(mktemp -d "${TMPDIR:-/tmp}/deck-gh.XXXXXX")
trap 'rm -rf "$work"' EXIT
zip=$work/gh.zip
curl -fsSL --proto '=https' --tlsv1.2 -o "$zip" \
  "https://github.com/cli/cli/releases/download/v${version}/gh_${version}_macOS_arm64.zip"
[ "$(sha256 "$zip")" = "$want" ] || fail 'Downloaded gh does not match the pinned SHA-256; nothing was installed.'

mkdir "$work/out"
unzip -q "$zip" -d "$work/out"
bin=$work/out/gh_${version}_macOS_arm64/bin/gh
[ -f "$bin" ] && [ ! -L "$bin" ] || fail 'The gh archive does not contain the expected bin/gh.'
[ "$("$bin" --version | awk 'NR == 1 { print $3 }')" = "$version" ] || fail 'The downloaded gh does not report the pinned version.'
install -m 0555 "$bin" "$tools/gh"
printf 'Installed gh %s at %s/gh\n' "$version" "$tools"
