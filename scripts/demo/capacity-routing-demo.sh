#!/usr/bin/env bash
# Demo script for the explicitly unconfined capacity-routing experiment.
# This script only composes existing `noise deck` CLI commands; it adds no
# endpoint, capability, or authority and never reads or writes provider
# credentials.
set -euo pipefail

cd "$(dirname "$0")/../.."
readonly PROGNAME="${0##*/}"

usage() {
  cat <<EOF
usage: $PROGNAME <command> [options]

Commands:
  preflight   Verify every precondition before the demo.
  run         Run the demo beats with presenter pauses.

Common options:
  --provider <id>       Target provider id (default: claude, env: CAPACITY_DEMO_PROVIDER)
  --model <id>          Target model id (default: fable, env: CAPACITY_DEMO_MODEL)
  --role <id>           Lifecycle role id (default: reviewer, env: CAPACITY_DEMO_ROLE)
  --task <id>           Canonical noise task id (env: CAPACITY_DEMO_TASK)
  --project-id <id>     Deck project id (default: \$NOISEDECK_PROJECT_ID, env: CAPACITY_DEMO_PROJECT_ID)
  --correlation-id <id> Logstream correlation id (default: capacity-demo-<timestamp>,
                        env: CAPACITY_DEMO_CORRELATION_ID)
  --timeout <dur>       Acceptance wait, at most 5m (default: 3m,
                        env: CAPACITY_DEMO_TIMEOUT)
  --completion-timeout <dur>
                        Closure wait (default: 10m,
                        env: CAPACITY_DEMO_COMPLETION_TIMEOUT)
  --receiver-ready      Acknowledge the one-time receiver warm-up is done in the
                        demo project (env: CAPACITY_DEMO_RECEIVER_READY=1);
                        preflight fails without it

Examples:
  $PROGNAME preflight --task <id> --receiver-ready
  $PROGNAME run --task <id> --project-id my-project
EOF
}

# Defaults: provider/model/role are the documented example values; task and
# project-id must come from you because they name real project state.
provider="${CAPACITY_DEMO_PROVIDER:-claude}"
model="${CAPACITY_DEMO_MODEL:-fable}"
role="${CAPACITY_DEMO_ROLE:-reviewer}"
task="${CAPACITY_DEMO_TASK:-}"
project_id="${CAPACITY_DEMO_PROJECT_ID:-${NOISEDECK_PROJECT_ID:-}}"
correlation_id="${CAPACITY_DEMO_CORRELATION_ID:-}"
# The receiver warm-up acknowledgement is your own attestation: first-run
# trust and approval state lives in provider CLI state, which Deck never
# reads. Explicit negations stay unacknowledged so `=0` cannot pass silently.
case "$(printf '%s' "${CAPACITY_DEMO_RECEIVER_READY:-}" | tr '[:upper:]' '[:lower:]')" in
  ""|0|false|no|off) receiver_ready="" ;;
  *) receiver_ready=1 ;;
esac
# Presenter-tuned waits. The CLI defaults (5m acceptance, 30m closure) suit an
# unattended run; a demo needs the beat to resolve or fail while the audience
# is still watching it.
timeout="${CAPACITY_DEMO_TIMEOUT:-3m}"
completion_timeout="${CAPACITY_DEMO_COMPLETION_TIMEOUT:-10m}"

# Options below take one operand unless they are flags. Checking for the
# operand before the expansion keeps a missing value on the usage path rather
# than aborting on `set -u`.
require_option_value() {
  if [ "$#" -lt 2 ]; then
    echo "$PROGNAME: $1 requires a value" >&2
    usage >&2
    exit 2
  fi
}

parse_common_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --provider)
        require_option_value "$@"
        provider="$2"
        shift 2
        ;;
      --model)
        require_option_value "$@"
        model="$2"
        shift 2
        ;;
      --role)
        require_option_value "$@"
        role="$2"
        shift 2
        ;;
      --task)
        require_option_value "$@"
        task="$2"
        shift 2
        ;;
      --project-id)
        require_option_value "$@"
        project_id="$2"
        shift 2
        ;;
      --correlation-id)
        require_option_value "$@"
        correlation_id="$2"
        shift 2
        ;;
      --timeout)
        require_option_value "$@"
        timeout="$2"
        shift 2
        ;;
      --completion-timeout)
        require_option_value "$@"
        completion_timeout="$2"
        shift 2
        ;;
      --receiver-ready)
        receiver_ready=1
        shift
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      --)
        shift
        break
        ;;
      -*)
        echo "$PROGNAME: unknown option: $1" >&2
        usage >&2
        exit 2
        ;;
      *)
        break
        ;;
    esac
  done
}

require_json_tool() {
  if command -v jq >/dev/null 2>&1; then
    return 0
  fi
  if command -v python3 >/dev/null 2>&1; then
    return 0
  fi
  echo "FAIL: preflight needs jq or python3 to read normalized capacity evidence." >&2
  echo "Fix: install jq (https://jqlang.github.io/jq/) or python3." >&2
  return 1
}

print_provider_windows() {
  local json="$1"
  if command -v jq >/dev/null 2>&1; then
    jq -r '.profiles[] | select(.evidence.windows | length > 0) | "\(.id): " + (.evidence.windows | map("\(.name) \(.used_percent)%") | join(", "))' <<<"$json"
  else
    python3 - <<'PY' "$json"
import json, sys
json_text = sys.argv[1]
d = json.loads(json_text)
for p in d.get("profiles", []):
    wins = p.get("evidence", {}).get("windows", [])
    if wins:
        line = p["id"] + ": " + ", ".join(f'{w["name"]} {w["used_percent"]}%' for w in wins)
        print(line)
PY
  fi
}

count_providers_with_windows() {
  local json="$1"
  if command -v jq >/dev/null 2>&1; then
    jq '[.profiles[] | select(.evidence.windows | length > 0)] | length' <<<"$json"
  else
    python3 - <<'PY' "$json"
import json, sys
json_text = sys.argv[1]
d = json.loads(json_text)
print(len([p for p in d.get("profiles", []) if p.get("evidence", {}).get("windows", [])]))
PY
  fi
}

harnesses_unconfined() {
  local json="$1"
  if command -v jq >/dev/null 2>&1; then
    jq -r '.unconfined' <<<"$json"
  else
    python3 - <<'PY' "$json"
import json, sys
json_text = sys.argv[1]
print(json.dumps(json.loads(json_text).get("unconfined")))
PY
  fi
}

cmd_preflight() {
  parse_common_args "$@"
  local failed=0

  echo "=== Capacity routing demo preflight ==="
  echo

  # 1. Claude Code raises per-project, first-run gates before a delegated
  #    receiver can act: the workspace trust dialog and an approval prompt for
  #    each unseen command prefix. A receiver stopped at either never reads the
  #    handoff prompt, so the handoff times out with an idle receiver that
  #    looks like a product failure. Provider CLI state would be needed to
  #    detect the gates, and Deck never reads it, so this check is your
  #    acknowledgement, not a verified state.
  if [ -z "$receiver_ready" ]; then
    echo "FAIL: one-time receiver warm-up is not acknowledged." >&2
    echo "Fix: before the demo, run one delegated handoff (or open one Claude" >&2
    echo "     session) in the exact demo project, accept the workspace trust" >&2
    echo "     dialog, and choose the \"don't ask again\" option for the command" >&2
    echo "     prefixes the receiver uses (such as noise tasks *)." >&2
    echo "     Then rerun with --receiver-ready or CAPACITY_DEMO_RECEIVER_READY=1." >&2
    failed=1
  else
    echo "PASS: one-time receiver warm-up is acknowledged."
  fi

  # 2. The CLI fails closed without the process-local capability Deck injects.
  if [ -z "${NOISEDECK_EXPERIMENT_CAPABILITY:-}" ]; then
    echo "FAIL: NOISEDECK_EXPERIMENT_CAPABILITY is not set." >&2
    echo "Fix: this session was not launched by Deck. Close the shell and relaunch" >&2
    echo "     the session from Deck so the capability is injected." >&2
    failed=1
  else
    echo "PASS: Deck experiment capability is present."
  fi

  # 3. The authoritative proof the experiment is enabled is the running app's
  #    own answer, valid on every deployment shape: a disabled app refuses the
  #    experimental harnesses endpoint outright, so a successful round trip is
  #    possible only with the experiment on, and the response carries the
  #    top-level "unconfined" marker. The same round trip surfaces the
  #    normalized capacity evidence counted below.
  require_json_tool || failed=1

  local harnesses_json=""
  local err_file=""
  err_file=$(mktemp "${TMPDIR:-/tmp}/capacity-harnesses.XXXXXX.err")
  # The trap owns the file so an interrupt or an error under `set -e` cannot
  # leak it; the branches below only read it.
  trap 'rm -f "$err_file"' EXIT INT TERM
  if ! harnesses_json=$(noise deck harnesses list --json 2>"$err_file"); then
    echo "FAIL: noise deck harnesses list did not complete; a disabled app refuses" >&2
    echo "     this endpoint, so the running app has not proven the experiment enabled." >&2
    echo "Fix: start the app with NOISEDECK_EXPERIMENTS_UNCONFINED_CAPACITY_ROUTING=true" >&2
    echo "     (see deploy/compose.env.example), then rerun from a Deck-launched session." >&2
    if [ -s "$err_file" ]; then
      echo "Error:" >&2
      cat "$err_file" >&2
    fi
    failed=1
  else
    local unconfined=""
    unconfined=$(harnesses_unconfined "$harnesses_json") || unconfined=""
    if [ "$unconfined" != "true" ]; then
      echo "FAIL: the harnesses response does not report \"unconfined\": true." >&2
      failed=1
    else
      echo "PASS: unconfined capacity-routing experiment is enabled in the running app"
      echo "      (authoritative: the app's own harnesses answer)."
    fi

    local count
    count=$(count_providers_with_windows "$harnesses_json")
    if [ "$count" -lt 2 ]; then
      echo "FAIL: only $count configured provider(s) reported non-empty capacity windows." >&2
      echo "Fix: at least two providers must have measured capacity evidence." >&2
      echo "     Check provider installation, subscription status, and adapters." >&2
      failed=1
    else
      echo "PASS: $count providers reported capacity windows:"
      print_provider_windows "$harnesses_json"
    fi
  fi

  # 4. Compose-only confirmation of the deployed container's own environment.
  #    Check 3 is the authority, so missing docker or no running noisefloor-deck
  #    container (a Deck started as a local binary) is a skip, not a failure.
  #    A running container carrying anything but true is a real
  #    misconfiguration of that deployment and fails.
  local container_running=""
  local flag_value=""
  if ! command -v docker >/dev/null 2>&1; then
    echo "INFO: docker is unavailable; skipped the Compose-only container flag check."
  else
    container_running=$(docker container inspect -f '{{.State.Running}}' noisefloor-deck 2>/dev/null) || container_running=""
    if [ "$container_running" != "true" ]; then
      echo "INFO: no running noisefloor-deck container; skipped the Compose-only container flag check."
    else
      flag_value=$(docker exec noisefloor-deck printenv NOISEDECK_EXPERIMENTS_UNCONFINED_CAPACITY_ROUTING 2>/dev/null) || flag_value=""
      if [ "$flag_value" != "true" ]; then
        echo "FAIL: NOISEDECK_EXPERIMENTS_UNCONFINED_CAPACITY_ROUTING is not true in the running noisefloor-deck container." >&2
        echo "Fix: enable the experiment in deploy/compose.env, redeploy the app container," >&2
        echo "     and verify with:" >&2
        echo "       docker exec noisefloor-deck printenv NOISEDECK_EXPERIMENTS_UNCONFINED_CAPACITY_ROUTING" >&2
        failed=1
      else
        echo "PASS: noisefloor-deck container also carries the experiment flag (Compose confirmation)."
      fi
    fi
  fi

  # 5. The demo task must exist so the handoff has real project work to name.
  if [ -z "$task" ]; then
    echo "FAIL: --task is required (or set CAPACITY_DEMO_TASK)." >&2
    failed=1
  elif ! noise tasks show "$task" >/dev/null 2>&1; then
    echo "FAIL: task $task does not exist or cannot be read." >&2
    echo "Fix: create the task or pass an existing task id with --task." >&2
    failed=1
  else
    echo "PASS: task $task exists."
  fi

  echo
  if [ "$failed" -ne 0 ]; then
    echo "=== Preflight FAILED ===" >&2
    exit 1
  fi
  echo "=== Preflight PASSED ==="
  exit 0
}

render_harnesses() {
  local json="$1"
  if command -v jq >/dev/null 2>&1; then
    jq -r '.profiles[] | "  \(.id):", ( .evidence.windows[] | "    \(.name): \(.used_percent)%" )' <<<"$json"
  else
    echo "$json"
  fi
}

pause_for_presenter() {
  local prompt="$1"
  read -r -p "$prompt" || true
}

cmd_run() {
  parse_common_args "$@"
  local missing=0
  if [ -z "$provider" ]; then
    echo "$PROGNAME run: --provider is required" >&2
    missing=1
  fi
  if [ -z "$model" ]; then
    echo "$PROGNAME run: --model is required" >&2
    missing=1
  fi
  if [ -z "$role" ]; then
    echo "$PROGNAME run: --role is required" >&2
    missing=1
  fi
  if [ -z "$task" ]; then
    echo "$PROGNAME run: --task is required" >&2
    missing=1
  fi
  if [ -z "$project_id" ]; then
    echo "$PROGNAME run: --project-id is required (or set NOISEDECK_PROJECT_ID)" >&2
    missing=1
  fi
  if [ "$missing" -ne 0 ]; then
    usage >&2
    exit 2
  fi
  if [ -z "$correlation_id" ]; then
    correlation_id="capacity-demo-$(date +%s)"
  fi

  echo "=== Capacity routing demo ==="
  echo "Provider: $provider"
  echo "Model:    $model"
  echo "Role:     $role"
  echo "Task:     $task"
  echo "Project:  $project_id"
  echo

  # Beat 1: show the audience live, normalized capacity evidence per provider.
  echo "Beat 1: configured harnesses and live capacity windows"
  echo
  local harnesses_json
  harnesses_json=$(noise deck harnesses list --json)
  render_harnesses "$harnesses_json"
  echo

  pause_for_presenter "Press Enter to execute the cross-subscription handoff..."

  # Beat 2: run the handoff and stream the JSON result as it resolves.
  echo
  echo "Beat 2: delegating to $provider/$model as $role"
  echo
  local result_file
  result_file=$(mktemp "${TMPDIR:-/tmp}/capacity-handoff.XXXXXX.json")
  trap 'rm -f "$result_file"' EXIT INT TERM

  local handoff_status=0
  set +o pipefail
  noise deck agent-handoff \
    --project-id "$project_id" \
    --provider "$provider" \
    --model "$model" \
    --role "$role" \
    --task "$task" \
    --correlation-id "$correlation_id" \
    --timeout "$timeout" \
    --completion-timeout "$completion_timeout" \
    --json | tee "$result_file"
  handoff_status=${PIPESTATUS[0]}
  set -o pipefail

  echo
  echo "Handoff summary:"
  if command -v jq >/dev/null 2>&1 && jq -e . "$result_file" >/dev/null 2>&1; then
    jq -r '
      "  Accepted:        \(.accepted)",
      "  Completed:       \(.completed)",
      "  Correlation id:  \(.acceptance.correlation_id // "n/a")",
      "  Closure status:  \(.completion.status // "n/a")",
      "  Diagnostic:      \(.diagnostic // "none")"
    ' "$result_file"
  else
    echo "  (install jq to render a structured summary; raw JSON follows)"
    cat "$result_file"
  fi

  exit "$handoff_status"
}

main() {
  if [ $# -eq 0 ]; then
    usage >&2
    exit 2
  fi
  case "$1" in
    preflight)
      shift
      cmd_preflight "$@"
      ;;
    run)
      shift
      cmd_run "$@"
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "$PROGNAME: unknown command: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
}

main "$@"
