#!/bin/sh
# Native workspaceplain controller entrypoint. No storage creation, migration,
# fallback, restart or down operation is exposed by this wrapper.
set -eu
refuse() { echo "plain-compose: installation precondition failed" >&2; exit 78; }
test "$#" = 2 && test "$2" = up || refuse
test "$(id -u)" != 0 || refuse
test -f "$1" || refuse
test "${NOISEDECK_WORKSPACE_GATEWAY:-}" = true || refuse
test -n "${COMPOSE_PROJECT_NAME:-}" && test "$COMPOSE_PROJECT_NAME" != deck || refuse
test -n "${NOISEDECK_PLAIN_INSTANCE:-}" || refuse
test -n "${NOISEDECK_PLAIN_CONTAINER:-}" || refuse
test -n "${NOISEDECK_PLAIN_STATE_ROOT:-}" || refuse
test -n "${NOISEDECK_PUBLIC_ORIGIN:-}" || refuse
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)

# Validate only the controller's fixed layout. Never source the Compose env
# file as shell or print it: it may contain the configured OIDC credentials.
python3 - "$NOISEDECK_PLAIN_STATE_ROOT" <<'PY'
import os, pathlib, stat, sys
root = pathlib.Path(sys.argv[1])
uid = os.getuid()
try:
    if not root.is_absolute() or root.resolve() != root:
        raise ValueError()
    for name in ('', 'data', 'home', 'projects', 'tmp', 'gh', 'data/gateway'):
        p = root / name
        s = p.lstat()
        if not stat.S_ISDIR(s.st_mode) or s.st_uid != uid or s.st_mode & 0o077:
            raise ValueError()
    marker = root / '.deck-plain-installation.json'
    s = marker.lstat()
    if not stat.S_ISREG(s.st_mode) or s.st_uid != uid or s.st_mode & 0o077:
        raise ValueError()
    value = marker.read_text()
    if len(value) != 65 or value[-1] != '\n' or any(c not in '0123456789abcdef' for c in value[:-1]):
        raise ValueError()
    if {p.name for p in root.iterdir()} != {'data','home','projects','tmp','gh',marker.name}:
        raise ValueError()
except (OSError, ValueError):
    sys.exit('plain-compose: managed plaintext layout unavailable')
PY
test "$(podman info --format '{{.Host.Security.Rootless}}' 2>/dev/null)" = true || refuse
# Interpolation of the earlier mempalace file still requires this value, even
# though the plain overlay keeps that store inside the single home bind.
NOISEDECK_MEMPALACE_HOST_PATH="$NOISEDECK_PLAIN_STATE_ROOT/home/.mempalace"
export NOISEDECK_MEMPALACE_HOST_PATH
exec podman compose --project-name "$COMPOSE_PROJECT_NAME" \
  --file "$repo_dir/compose.yaml" \
  --file "$repo_dir/compose.mempalace.yaml" \
  --file "$repo_dir/compose.workspace-plain.yaml" \
  --file "$repo_dir/compose.workspace-gateway.yaml" \
  --env-file "$1" up --detach --no-recreate --no-build app
