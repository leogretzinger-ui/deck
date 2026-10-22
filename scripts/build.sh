#!/usr/bin/env bash
# Build the Deck backend binary into build/.
set -euo pipefail

cd "$(dirname "$0")/.."
exec make build "$@"
