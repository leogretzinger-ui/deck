#!/bin/sh
set -eu

test "$#" -eq 1 || {
  echo "usage: $0 <rendered-compose.json>" >&2
  exit 64
}
rendered=$1

tmp_details=$(python3 -c '
import json, sys
app = json.load(sys.stdin)["services"]["app"]
mounts = [mount for mount in app.get("volumes", []) if mount.get("target") == "/tmp"]
if len(mounts) != 1:
    raise SystemExit(1)
print(mounts[0]["source"])
print(app["user"])
' <"$rendered") || {
  echo "production-compose: cannot resolve the production /tmp source" >&2
  exit 1
}
tmp_source=$(printf '%s\n' "$tmp_details" | sed -n '1p')
runtime_identity=$(printf '%s\n' "$tmp_details" | sed -n '2p')
runtime_uid=${runtime_identity%%:*}
runtime_gid=${runtime_identity#*:}

test -d "$tmp_source" || {
  echo "production-compose: production /tmp source is not an existing directory: $tmp_source" >&2
  exit 1
}
tmp_mount=$(findmnt --target "$tmp_source" --noheadings --output FSTYPE,OPTIONS) || {
  echo "production-compose: cannot inspect the production /tmp backing filesystem" >&2
  exit 1
}
tmp_fstype=${tmp_mount%% *}
case "$tmp_fstype" in
  tmpfs|ramfs)
    echo "production-compose: production /tmp source is RAM-backed ($tmp_fstype)" >&2
    exit 1
    ;;
esac
case " $tmp_mount " in
  *",ro,"*|*" ro,"*|*",ro "*|*",noexec,"*|*" noexec,"*|*",noexec "*)
    echo "production-compose: production /tmp source must be writable and executable" >&2
    exit 1
    ;;
esac

tmp_stat=$(stat -c '%u:%g %a' "$tmp_source")
tmp_owner=${tmp_stat%% *}
tmp_mode=${tmp_stat#* }
test "$tmp_owner" = "$runtime_uid:$runtime_gid" || {
  echo "production-compose: production /tmp source owner $tmp_owner does not match runtime $runtime_uid:$runtime_gid" >&2
  exit 1
}
case "$tmp_mode" in
  17[0-7][0-7]|7[0-7][0-7]) ;;
  *)
    echo "production-compose: production /tmp source owner must have rwx permissions (1777 is recommended)" >&2
    exit 1
    ;;
esac
