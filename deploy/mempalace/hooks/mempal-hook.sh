#!/bin/sh
set -eu

exec /usr/bin/python3 /usr/local/lib/noisedeck/mempalace-hook-forwarder.py \
  --hook "${1:?missing lifecycle hook}" --harness codex
