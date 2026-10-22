#!/bin/sh
# This probe creates only disposable encrypted test data. It does not start
# Deck or replace the shared-host qualification and hostile root-read tests.
set -eu

if test "$(uname -s)" != Linux || test "$(id -u)" = 0; then
  echo "UNAVAILABLE: run the native probe as the dedicated non-root owner on Linux" >&2
  exit 77
fi
for tool in go gocryptfs podman fusermount3; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "UNAVAILABLE: native probe requires $tool" >&2
    exit 77
  fi
done
if ! test -c /dev/fuse || test -z "${XDG_RUNTIME_DIR:-}"; then
  echo "UNAVAILABLE: native probe requires /dev/fuse and the owner's XDG_RUNTIME_DIR" >&2
  exit 77
fi
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$script_dir/.."
NOISEDECK_NATIVE_HOST_TEST=1 go test -count=1 -timeout=2m -run '^TestNativeHostEncryptedRoundTrip$' -v ./internal/workspaceruntime
