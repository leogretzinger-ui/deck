#!/bin/sh
# Lifecycle hook for a2a chat: stdin carries the harness hook JSON.
# usage: a2a-hook.sh <session-start|prompt|stop|session-end> <harness>
set -eu

exec /usr/bin/python3 /usr/local/bin/a2a hook "${1:?missing lifecycle event}" \
  --harness "${2:?missing harness}"
