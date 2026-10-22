#!/bin/sh
# Run native Deck as a per-user launchd agent: render, install or uninstall. Never needs root.
set +x
set -eu
fail() { printf '%s\n' "$1" >&2; exit 1; }
usage() {
  fail 'Usage: macos-launch-agent.sh render|install --label L --binary BIN --config FILE --path PATH --log-dir DIR [--env NAME=VALUE]... [--setup-file FILE]
       macos-launch-agent.sh uninstall --label L'
}

here=$(cd "$(dirname "$0")" && pwd)
[ "$#" -ge 1 ] || usage
action=$1
shift

xml() { printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'; }
plain() {
  case "$2" in
    '') fail "$1 is required." ;;
    *'
'*) fail "$1 must be a single line." ;;
  esac
}
absolute() {
  plain "$1" "$2"
  case "$2" in /*) ;; *) fail "$1 must be an absolute path." ;; esac
}
replace() {
  rest=$1 out=
  while :; do
    case $rest in
      *"$2"*) out=$out${rest%%"$2"*}$3; rest=${rest#*"$2"} ;;
      *) printf '%s%s' "$out" "$rest"; return ;;
    esac
  done
}

label= binary= config= runtime_path= log_dir= setup_file= env_entries=
while [ "$#" -gt 0 ]; do
  [ "$#" -ge 2 ] || usage
  case "$1" in
    --label) label=$2 ;;
    --binary) binary=$2 ;;
    --config) config=$2 ;;
    --path) runtime_path=$2 ;;
    --log-dir) log_dir=$2 ;;
    --setup-file) setup_file=$2 ;;
    --env)
      case "$2" in [A-Za-z_]*=*) ;; *) fail '--env takes NAME=VALUE.' ;; esac
      plain --env "$2"
      name=${2%%=*}
      case "$name" in *[!A-Za-z0-9_]*) fail '--env name may use letters, digits and underscores only.' ;; esac
      [ "$name" != PATH ] || fail 'Pass the runtime PATH with --path.'
      [ "$name" != HOME ] || fail 'HOME comes from your macOS login environment; do not override it with --env.'
      env_entries="$env_entries
$name=${2#*=}" ;;
    *) usage ;;
  esac
  shift 2
done

validate() {
  plain --label "$label"
  case "$label" in *[!A-Za-z0-9.-]*) fail '--label may use letters, digits, dots and hyphens only.' ;; esac
  absolute --binary "$binary"
  absolute --config "$config"
  absolute --log-dir "$log_dir"
  absolute HOME "$HOME"
  absolute --path "${runtime_path%%:*}"
  plain --path "$runtime_path"
}

render() {
  validate
  environment="    <key>PATH</key>
    <string>$(xml "$runtime_path")</string>"
  oldifs=$IFS
  IFS='
'
  for entry in $env_entries; do
    [ -n "$entry" ] || continue
    environment="$environment
    <key>${entry%%=*}</key>
    <string>$(xml "${entry#*=}")</string>"
  done
  IFS=$oldifs
  text=$(cat "$here/macos-launch-agent.plist.template")
  text=$(replace "$text" @LABEL@ "$(xml "$label")")
  text=$(replace "$text" @BINARY@ "$(xml "$binary")")
  text=$(replace "$text" @CONFIG@ "$(xml "$config")")
  text=$(replace "$text" @LOG_DIR@ "$(xml "$log_dir")")
  text=$(replace "$text" @ENVIRONMENT@ "$environment")
  text=$(replace "$text" @HOME@ "$(xml "$HOME")")
  printf '%s\n' "$text"
}

private_mode() {
  mode=$(stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1")
  owner=$(stat -c %u "$1" 2>/dev/null || stat -f %u "$1")
  [ "$mode" = 600 ] && [ "$owner" = "$(id -u)" ]
}

# Deck writes its one-time setup link next to its password state, which sits beside the database.
default_setup_file() {
  db=$(awk '/^database:[ \t]*$/ {inside=1; next} /^[^ \t#]/ {inside=0} inside && $1 == "path:" {sub(/^[ \t]*path:[ \t]*/, ""); sub(/[ \t]+$/, ""); print; exit}' "$config")
  case $db in
    \"*) db=${db#\"}; db=${db%%\"*} ;;
    \'*) db=${db#\'}; db=${db%%\'*} ;;
    *) db=${db%% \#*} ;;
  esac
  [ -n "$db" ] || fail 'Could not read database.path from --config; pass --setup-file.'
  absolute database.path "$db"
  printf '%s/setup-url\n' "$(dirname "$db")"
}

# The link reaches the browser opener on stdin so it never appears in a process list.
open_in_browser() {
  case $1 in *[\"\\[:space:]]*) return 1 ;; esac
  printf 'open location "%s"\n' "$1" | osascript >/dev/null 2>&1
}

# Opens the one-time setup link without ever printing it.
open_setup_link() {
  waited=0
  while [ "$waited" -lt 60 ]; do
    if [ -f "$setup_file" ] && [ ! -L "$setup_file" ] && [ -n "$(find "$setup_file" -newer "$plist" 2>/dev/null)" ]; then
      private_mode "$setup_file" || fail 'The setup-link file is not owner-only; not opening it.'
      url=$(sed -n 1p "$setup_file")
      case "$url" in
        http://*|https://*) open_in_browser "$url" || fail 'Could not open the setup link in a browser.'; return ;;
        *) fail 'The setup-link file does not hold a web address.' ;;
      esac
    fi
    sleep 1
    waited=$((waited + 1))
  done
  printf '%s\n' "Deck did not write a setup link within a minute. If your Deck release predates first-run password creation, it prints a one-time password in $log_dir/deck.log instead." \
    "Open that file yourself in Terminal, copy the password into Deck's sign-in page, and do not share it with an agent."
}

case "$action" in
  render) render ;;
  install)
    [ "$(id -u)" -ne 0 ] || fail 'Run this as your own macOS user, not root.'
    [ -x "$binary" ] || fail '--binary must be an executable file.'
    [ -f "$config" ] || fail '--config must be an existing file.'
    if [ -n "$setup_file" ]; then absolute --setup-file "$setup_file"; else setup_file=$(default_setup_file); fi
    agents=$HOME/Library/LaunchAgents
    plist=$agents/$label.plist
    [ ! -e "$plist" ] || fail "$plist already exists; uninstall it first."
    text=$(render)
    (umask 077; mkdir -p "$log_dir")
    chmod 700 "$log_dir"
    mkdir -p "$agents"
    printf '%s\n' "$text" > "$plist"
    chmod 644 "$plist"
    launchctl bootstrap "gui/$(id -u)" "$plist"
    printf 'Deck is running as launchd agent %s; logs are in %s/deck.log\n' "$label" "$log_dir"
    open_setup_link ;;
  uninstall)
    plain --label "$label"
    case "$label" in *[!A-Za-z0-9.-]*) fail '--label may use letters, digits, dots and hyphens only.' ;; esac
    plist=$HOME/Library/LaunchAgents/$label.plist
    target=gui/$(id -u)/$label
    launchctl bootout "$target" >/dev/null 2>&1 || :
    ! launchctl print "$target" >/dev/null 2>&1 || fail "Deck is still loaded as $label; kept $plist."
    rm -f "$plist"
    printf 'Removed launchd agent %s\n' "$label" ;;
  *) usage ;;
esac
