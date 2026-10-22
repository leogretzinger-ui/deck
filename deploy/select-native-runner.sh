#!/bin/sh
set -eu

backend=${NOISEDECK_NATIVE_BACKEND:-github}

case "$backend" in
  auto|github|blacksmith) ;;
  *)
    echo "NOISEDECK_NATIVE_BACKEND must be auto, github, or blacksmith" >&2
    exit 64
    ;;
esac

if test "$backend" = blacksmith; then
  selected_backend=blacksmith
  runner='"blacksmith-4vcpu-ubuntu-2404"'
else
  selected_backend=github
  runner='"ubuntu-24.04"'
fi

printf 'backend=%s\n' "$selected_backend"
printf 'runner=%s\n' "$runner"

if test -n "${GITHUB_OUTPUT:-}"; then
  {
    printf 'backend=%s\n' "$selected_backend"
    printf 'runner=%s\n' "$runner"
  } >>"${GITHUB_OUTPUT}"
fi
