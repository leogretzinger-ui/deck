#!/bin/sh
# launch.sh [opencode args...] — exec the pinned opencode CLI in the fully
# isolated spike environment. Used as the tmux pane command so the pane
# process IS the provider (same shape as the codex/kimi containment spikes).
# NOISEDECK_SESSION_HOOK_URL / NOISEDECK_SESSION_HOOK_TOKEN pass through from the caller's
# environment when set (per-launch one-shot capture endpoint).
T90="${T90:-/workspace/noisedeck/.review/t90}"
export HOME="$T90/scratch-home"
export XDG_CONFIG_HOME="$T90/scratch-cfg"
export XDG_DATA_HOME=/dev/shm/noisedeck-t90-data
export XDG_CACHE_HOME="$T90/scratch-cache"
export XDG_STATE_HOME="$T90/scratch-state"
export TMPDIR="$T90/tmp"
export OC_SPIKE_EVENTS="$T90/events.log"
export TERM=xterm-256color
exec /usr/local/bin/opencode "$@"
