#!/bin/sh
# sealed-compose.sh — bring Deck up on a sealed state root.
#
#   deploy/sealed-compose.sh <env-file> up|down|check|report
#
# `up` runs the host preflight's qualify step against the unlocked state
# root, refuses on any plaintext store, starts the container with
# compose.sealed.yaml applied, reads the container's own mount table and
# writes the launch-bound report into the host tmpfs the container sees
# read-only, then asks the container to run its own check. Bootstrap inside
# the container starts nothing that writes until that report is present and
# passes, so a wrapper that dies between `compose up` and the report leaves
# an idle container, never a plaintext write.
#
# The env file is Compose dotenv, never shell: it is read by the preflight's
# parser and handed to compose with --env-file, so a value with a space or a
# quote is a value. Each Deck on a host is its own Compose project, named by
# COMPOSE_PROJECT_NAME in the env file, so a partner's `down` can never stop
# your Deck.
#
# Runs as the partner's user under rootless Podman, never as root and never
# against a rootful daemon: the FUSE mount is owner-only, so the daemon that
# validates the binds and the processes inside the container must both be
# the partner on the host. Rootless Podman is that user, and the overlay's
# keep-id namespace makes the container's runtime uid the partner's host
# uid. A rootful daemon cannot stat a bind source inside the mount, and
# rootless Docker maps container uids to subordinate ids that cannot open it.
set -eu

usage() {
  echo "usage: $0 <env-file> up|down|check|report" >&2
  exit 64
}

test "$#" -ge 2 || usage
env_file=$1
action=$2
shift 2

test -f "$env_file" || {
  echo "sealed-compose: env file does not exist: $env_file" >&2
  exit 66
}
env_dir=$(CDPATH= cd -- "$(dirname -- "$env_file")" && pwd)
env_file="$env_dir/$(basename -- "$env_file")"

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
preflight="$script_dir/sealed-preflight.py"

# The effective configuration is what Compose sees: a variable already in
# this shell wins over the file, and the preflight applies the same rule.
envval() {
  python3 "$preflight" env --env-file "$env_file" "$1"
}

project=$(envval COMPOSE_PROJECT_NAME) || {
  echo "sealed-compose: COMPOSE_PROJECT_NAME is not set in the env file; each Deck on a host needs its own project name" >&2
  exit 78
}
case "$project" in
  ""|deck)
    echo "sealed-compose: COMPOSE_PROJECT_NAME must name this Deck alone, not the default project \"deck\"" >&2
    exit 78
    ;;
esac
state_root=$(envval NOISEDECK_SEALED_STATE_ROOT) || {
  echo "sealed-compose: NOISEDECK_SEALED_STATE_ROOT is not set in the env file" >&2
  exit 78
}
report_dir=$(envval NOISEDECK_SEALED_REPORT_DIR) || {
  echo "sealed-compose: NOISEDECK_SEALED_REPORT_DIR is not set in the env file" >&2
  exit 78
}
profile=$(envval NOISEDECK_SEALED_PROFILE) || profile=shared-host

# Podman's compose front end, or podman-compose, honours the same files. Own
# host, where the partner is root anyway, may use Docker; the profile picks.
# Own host, where the partner is root anyway, runs under Docker. The engine
# is one choice made here, and the preflight inspects through the same one.
compose_cmd=${NOISEDECK_COMPOSE_COMMAND:-}
if test -z "$compose_cmd"; then
  if test "$profile" = shared-host; then
    compose_cmd="podman compose"
  else
    compose_cmd="docker compose"
  fi
fi
engine=${compose_cmd%% *}
profile_files=""
if test "$profile" = shared-host; then
  profile_files="--file $repo_dir/compose.sealed-podman.yaml"
fi
gateway=$(envval NOISEDECK_WORKSPACE_GATEWAY) || gateway=false
if test "$gateway" = true; then
  test "$profile" = shared-host || {
    echo "sealed-compose: workspace gateway requires the shared-host profile" >&2
    exit 78
  }
  profile_files="$profile_files --file $repo_dir/compose.workspace-gateway.yaml"
elif test "$gateway" != false; then
  echo "sealed-compose: NOISEDECK_WORKSPACE_GATEWAY must be true or false" >&2
  exit 78
fi

compose() {
  # shellcheck disable=SC2086
  $compose_cmd \
    --project-name "$project" \
    --file "$repo_dir/compose.yaml" \
    --file "$repo_dir/compose.mempalace.yaml" \
    --file "$repo_dir/compose.sealed.yaml" \
    $profile_files \
    --env-file "$env_file" "$@"
}

refuse_root_and_rootful() {
  if test "$(id -u)" = 0; then
    echo "sealed-compose: refusing to run as root; run as the user that owns the state root" >&2
    exit 77
  fi
  if test "$profile" != shared-host; then
    return 0
  fi
  command -v podman >/dev/null 2>&1 || {
    echo "sealed-compose: the shared-host profile needs rootless Podman; podman is not installed" >&2
    exit 78
  }
  rootless=$(podman info --format '{{.Host.Security.Rootless}}' 2>/dev/null) || rootless=""
  test "$rootless" = true || {
    echo "sealed-compose: podman is not running rootless for this user; a root daemon cannot open the owner-only mount" >&2
    exit 78
  }
}

qualify() {
  refuse_root_and_rootful
  test -d "$state_root" || {
    echo "sealed-compose: state root $state_root is not a directory; is the mount open?" >&2
    exit 78
  }
  # The report directory must be RAM-backed: a report on disk would outlive
  # the boot it describes. Who may read it depends on the profile. On the
  # shared host the container's runtime uid is this user (keep-id), so the
  # directory is owner-only under this user's runtime tmpfs. On the own host
  # the container runs as the image's runtime uid, a different host uid, so
  # the directory sits on a tmpfs every uid can traverse and is readable by
  # all; the report holds no secret, only mount facts.
  test -d "$report_dir" || mkdir -p "$report_dir"
  fstype=$(stat -f -c %T "$report_dir")
  test "$fstype" = tmpfs || {
    echo "sealed-compose: $report_dir is $fstype, not tmpfs; point NOISEDECK_SEALED_REPORT_DIR at a RAM-backed directory" >&2
    exit 78
  }
  if test "$profile" = shared-host; then
    chmod 0700 "$report_dir"
  else
    chmod 0755 "$report_dir"
    # Every component above must let the runtime uid through, or the
    # container sees an empty mount and bootstrap waits for nothing.
    walk=$report_dir
    while test "$walk" != / ; do
      case "$(stat -c %A "$walk")" in
        d????????[xt]) ;;
        *) echo "sealed-compose: $walk is not traversable by other users; the own-host container runs as the image's runtime uid, so put NOISEDECK_SEALED_REPORT_DIR under a shared tmpfs such as /dev/shm" >&2; exit 78 ;;
      esac
      walk=$(dirname -- "$walk")
    done
  fi
  python3 "$preflight" qualify --env-file "$env_file"
}

write_report() {
  app_id=$(compose ps -q app) || {
    echo "sealed-compose: cannot inspect the app container" >&2
    return 1
  }
  test -n "$app_id" || {
    echo "sealed-compose: app container is absent" >&2
    return 1
  }
  python3 "$preflight" report --env-file "$env_file" --engine "$engine" --container "$app_id"
}

container_check() {
  compose exec -T app /usr/local/bin/noise deck sealed-check
}

case "$action" in
  check)
    qualify
    ;;
  up)
    qualify
    rm -f "$report_dir/report.json"
    compose up -d "$@"
    write_report
    container_check
    ;;
  report)
    qualify
    write_report
    container_check
    ;;
  down)
    compose down "$@"
    rm -f "$report_dir/report.json"
    ;;
  *)
    usage
    ;;
esac
