# shellcheck shell=bash
# Shared environment for the opencode 1.18.4 Phase-0 spike.
# Source this from every spike script. All state is isolated:
#   - tmux sockets, working dirs, config/cache/state live under the
#     repository's gitignored .review/t90/ scratch tree
#   - the opencode DATA home (which must hold a copy of auth.json for model
#     access) lives on /dev/shm tmpfs so credentials never touch durable
#     workspace scratch (same pattern as the codex conformance harness)
#   - TMPDIR is redirected so /tmp/opencode is never shared with your
#     own opencode runs
# Your real ~/.config/opencode and ~/.local/share/opencode are
# never WRITTEN by anything here, and nothing else in them is read. The one
# exception: oc_setup copies $HOME/.local/share/opencode/auth.json (model
# credentials, read-only) onto the tmpfs data home, because model access
# requires it; that copy is destroyed with the tmpfs after the spike.

T90="${T90:-/workspace/noisedeck/.review/t90}"
OC_SOCK="$T90/opencode.sock"          # private tmux socket
OC_WD="$T90/wd"                       # session working directory
OC_DATA=/dev/shm/noisedeck-t90-data          # tmpfs; holds the auth.json copy
OC_SPIKE_EVENTS="$T90/events.log"     # plugin event log (event types only + ids)

# oc_env <cmd...> — run a command in the isolated opencode environment.
oc_env() {
  env -i \
    HOME="$T90/scratch-home" \
    XDG_CONFIG_HOME="$T90/scratch-cfg" \
    XDG_DATA_HOME="$OC_DATA" \
    XDG_CACHE_HOME="$T90/scratch-cache" \
    XDG_STATE_HOME="$T90/scratch-state" \
    TMPDIR="$T90/tmp" \
    PATH=/usr/local/bin:/usr/bin:/bin \
    TERM=xterm-256color \
    OC_SPIKE_EVENTS="$OC_SPIKE_EVENTS" \
    "$@"
}

# oc_tmux <args...> — tmux against the private spike socket.
oc_tmux() {
  tmux -S "$OC_SOCK" -f /dev/null "$@"
}

oc_setup() {
  mkdir -p "$OC_WD" "$T90/scratch-home" "$T90/scratch-cfg/opencode/plugins" \
    "$T90/scratch-cache" "$T90/scratch-state" "$T90/tmp" "$OC_DATA/opencode"
  chmod 700 "$OC_DATA" "$OC_DATA/opencode"
  # Model credential copy lives only on tmpfs.
  if [ ! -f "$OC_DATA/opencode/auth.json" ]; then
    cp "$HOME/.local/share/opencode/auth.json" "$OC_DATA/opencode/auth.json"
    chmod 600 "$OC_DATA/opencode/auth.json"
  fi
  # Pinned-version fixture: never self-update, cheap model, no interactive
  # permission prompts (provisioning-owned config, like the SessionStart
  # hooks for codex/kimi).
  cat > "$T90/scratch-cfg/opencode/opencode.json" <<'EOF'
{
  "$schema": "https://opencode.ai/config.json",
  "autoupdate": false,
  "model": "opencode-go/kimi-k2.6",
  "permission": { "*": "allow" }
}
EOF
  cp "$(dirname "${BASH_SOURCE[0]}")/noisedeck-session-hook.js" \
    "$T90/scratch-cfg/opencode/plugins/noisedeck-session-hook.js"
}
