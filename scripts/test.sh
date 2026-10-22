#!/usr/bin/env bash
# Run the full pre-PR gate: vet, formatting, module integrity, tests.
set -euo pipefail

cd "$(dirname "$0")/.."
exec make verify "$@"
