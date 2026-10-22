#!/bin/sh
set -eu

exec /usr/bin/python3 /usr/local/lib/noisedeck/mempalace-hook-forwarder.py \
  --hook precompact --harness claude-code
