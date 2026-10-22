#!/bin/sh
set -eu

exec /usr/bin/python3 /usr/local/lib/noisedeck/mempalace-hook-forwarder.py \
  --hook stop --harness claude-code
