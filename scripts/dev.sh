#!/bin/sh
# The development loop in one command: the backend with the dev preview on,
# and the hot-reloading UI served behind it at http://127.0.0.1:8837/__dev/.
#
# Going through the backend is the whole point. A browser on Vite's own port
# would present http://127.0.0.1:5173 as its origin, which the backend refuses
# on every write unless allowed_origins is edited; behind the preview route
# the origin is the backend's, so nothing needs configuring and the same
# session, sessions, and providers are in play. Both processes stop together.
set -eu
cd "$(dirname "$0")/.."

# Both toolchains are needed before either half starts: discovering a
# missing one after the backend is already building is the slow way.
# node is run directly for Vite, so it is checked directly: a standalone
# pnpm bundles its own runtime and can pass with no node on PATH.
for tool in go pnpm node; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "make dev: '$tool' is not on PATH" >&2
    exit 1
  }
done

config="${NOISEDECK_DEV_CONFIG:-configs/config.example.yaml}"

# No sign-in for the dev loop. auth.mode none is the explicit opt-out the
# validator admits only when every allowed origin is loopback, which the
# example config's are; this is a browser on the same machine talking to a
# backend bound to 127.0.0.1. Set NOISEDECK_AUTH_MODE yourself (an empty
# value counts as unset) to develop against a real mode. A ./run that
# already booted in another mode refuses the switch — the backend's error
# names the transition command.
if [ -z "${NOISEDECK_AUTH_MODE:-}" ]; then
  # Defaulting to none is this script's decision, so the script owns the
  # one hazard in it: none disables authentication outright, and the
  # validator judges exposure by the declared ORIGINS, not the bind
  # (a container binds a wildcard while Compose publishes
  # loopback). A shell with NOISEDECK_SERVER_BIND=0.0.0.0 exported, or a
  # dev config that binds publicly, would therefore get an unauthenticated
  # console on every interface. Refuse unless the effective bind is
  # loopback; a user who chooses none themselves gets the validator's
  # rules, which are theirs to reason about.
  # `bind:` is server.bind in both the block style the example config uses
  # and the flow style (server: {bind: "..."}); it is the only key of that
  # name, so the whole file is searched rather than a server: block. A
  # commented-out line is skipped: the usual edit is to comment out the
  # loopback default and add the real bind below it, and reading the
  # comment would default to none on exactly the bind this guard exists
  # to catch.
  bind="${NOISEDECK_SERVER_BIND:-$(awk '!/^[ \t]*#/ && match($0,/bind:[ \t]*"?[^" ,}\t]+/){v=substr($0,RSTART,RLENGTH);sub(/^bind:[ \t]*"?/,"",v);print v;exit}' "$config" 2>/dev/null)}"
  case "${bind:-127.0.0.1:8837}" in
    127.*|localhost:*|\[::1\]:*|::1:*) ;;
    *)
      echo "make dev: bind $bind is not loopback; auth.mode none is loopback-only." >&2
      echo "make dev: unset NOISEDECK_SERVER_BIND, or choose NOISEDECK_AUTH_MODE yourself." >&2
      exit 1
      ;;
  esac
  NOISEDECK_AUTH_MODE=none
fi
export NOISEDECK_AUTH_MODE

# A fresh checkout has no web dependencies; those are Vite itself. The
# embedded UI is needed only under a real auth mode: the preview route sits
# behind the browser authentication and answers 401 until a session exists,
# and the only place to create one in a browser is the app served at /. It
# is built once and then left alone — /__dev/ is what you develop against.
if [ ! -d web/node_modules ]; then
  (cd web && pnpm install --frozen-lockfile)
fi
if [ "$NOISEDECK_AUTH_MODE" != none ] && [ ! -f internal/server/webassets/dist/index.html ]; then
  make build-frontend
fi

# tmux insists on a 0700 socket directory owned by the runtime user; the
# example config keeps it under ./run.
mkdir -p run/tmux
chmod 700 run/tmux

# Hold the real processes, not wrappers around them. `go run` and `pnpm dev`
# each spawn the thing that matters as a child of their own; killing the
# wrapper leaves that child alive, holding the port and the database lock
# after the loop has supposedly stopped. So: build the backend and run the
# binary, and run Vite's own entry point. Both go to the background so this
# shell is free to act on a signal — a trap cannot run while sh is waiting
# on a foreground child, which is why a plain SIGINT to this script would
# otherwise do nothing until Vite chose to exit.
go build -o run/noise-deck-dev ./cmd/noisedeck
NOISEDECK_EXPERIMENTS_DEV_PREVIEW=true ./run/noise-deck-dev serve -config "$config" &
backend=$!
# Vite reads its keyboard shortcuts from stdin when stdin is a terminal; a
# background job doing that gets stopped by SIGTTIN, so it gets none.
(cd web && NOISEDECK_DEV_PREVIEW_BASE=/__dev/ exec node node_modules/vite/bin/vite.js) < /dev/null &
vite=$!

stop() {
  trap - EXIT INT TERM
  kill "$vite" "$backend" 2>/dev/null || true
  wait "$vite" "$backend" 2>/dev/null || true
}
# A deliberate interrupt is reported as one — not as "the backend exited
# (status 143)", which is what the status block below would say after the
# trap had already stopped both children.
trap 'stop; exit 130' INT
trap 'stop; exit 143' TERM
trap stop EXIT

# Either half exiting ends the loop, and the trap takes the other down.
# POSIX sh has no way to wait for whichever child exits first, so poll: a
# backend that dies at startup (a bad config, a port already taken) must
# not leave Vite serving alone behind a script that looks healthy.
while kill -0 "$vite" 2>/dev/null && kill -0 "$backend" 2>/dev/null; do
  sleep 1
done
# Whichever half ended the loop, its exit status is the loop's. A backend
# that exits at all is a failure, even cleanly: nothing in this loop asks it
# to stop. Vite exiting cleanly is the ordinary way out; Vite exiting with
# a status — its strict port already taken, most often — must not be
# reported as success just because the backend was still up.
status=0
if ! kill -0 "$backend" 2>/dev/null; then
  wait "$backend" || status=$?
  echo "make dev: the backend exited (status $status); stopping" >&2
  [ "$status" -ne 0 ] || status=1
else
  wait "$vite" || status=$?
  [ "$status" -eq 0 ] || echo "make dev: the dev server exited (status $status); stopping" >&2
fi
stop
exit "$status"
