#!/bin/sh
# Capture the local Mac identity without printing it or replacing an existing file.
set +x
set -eu
fail() { printf '%s\n' "$1" >&2; exit 1; }
[ "$#" -eq 1 ] || fail 'Pass the agreed absolute private host-identity file path.'
case "$1" in /*) ;; *) fail 'Host-identity path must be absolute.' ;; esac
[ "$(/usr/bin/uname -s)" = Darwin ] || fail 'Run this helper on the actual Mac host.'
[ -d "$(dirname "$1")" ] || fail 'The agreed private parent directory must already exist.'
[ ! -L "$1" ] || fail 'Refusing a symlink host-identity file.'
deck_host_uuid=$(/usr/sbin/ioreg -rd1 -c IOPlatformExpertDevice | /usr/bin/awk -F '"' '
  /"IOPlatformUUID"[[:space:]]*=/ { count++; uuid = $4 }
  END {
    if (count != 1 || uuid !~ /^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$/) exit 1
    print uuid
  }') || fail 'Expected exactly one valid IOPlatformUUID.'
if [ -e "$1" ]; then
  [ -f "$1" ] || fail 'Existing host-identity path is not a regular file.'
else
  (umask 077; set -C; printf '%s\n' "$deck_host_uuid" > "$1") 2>/dev/null ||
    fail 'Exclusive host-identity creation failed; existing paths are never replaced.'
fi
[ "$(/usr/bin/stat -f '%Lp' "$1")" = 600 ] || fail 'Host-identity file must already have private mode 600.'
[ "$(/usr/bin/stat -f '%u' "$1")" = "$(/usr/bin/id -u)" ] || fail 'Host-identity file must belong to the invoking user.'
printf '%s\n' "$deck_host_uuid" | /usr/bin/cmp -s - "$1" ||
  fail 'Existing host identity does not match the actual Mac; preserved unchanged.'
unset deck_host_uuid
