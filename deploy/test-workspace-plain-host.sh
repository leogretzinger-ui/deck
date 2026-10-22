#!/bin/sh
# Explicit SRE opt-in only. The config must describe empty disposable stores
# and a dedicated deck-plain-test-* project and container, never an instance.
set -eu
if test "$(uname -s)" != Linux || test "$(id -u)" = 0; then
  echo "UNAVAILABLE: dedicated non-root Linux owner required" >&2
  exit 77
fi
for tool in go podman; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "UNAVAILABLE: plain runtime probe requires $tool" >&2
    exit 77
  fi
done
if test -z "${NOISEDECK_PLAIN_HOST_CONFIG:-}"; then
  echo "UNAVAILABLE: supply a disposable plain Config JSON through NOISEDECK_PLAIN_HOST_CONFIG" >&2
  exit 77
fi
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$script_dir/.."
NOISEDECK_PLAIN_HOST_TEST=1 go test -count=1 -timeout=4m -run '^TestNativeHostPlainRuntime$' -v ./internal/workspaceplain
