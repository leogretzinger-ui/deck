#!/usr/bin/env bash
# Night Shift — drain the project's actionable tasks into whichever configured
# subscription has headroom, one visible delegated handoff at a time.
#
# This script is the source agent's tooling: you launch
# it inside a Deck session, state the routing policy as flags, and the script
# compares the normalized capacity evidence Deck serves and names a target for
# each handoff. Deck itself never ranks providers and never initiates from a
# timer; every wait and every choice below happens on your side of
# the experiment capability. It only composes existing `noise deck` and
# `noise tasks` invocations and never reads provider state or credentials.
set -euo pipefail

readonly PROGNAME="${0##*/}"
readonly LEDGER="night-shift-ledger.jsonl"
readonly OBSERVATIONS="capacity-observations.jsonl"

usage() {
  cat <<EOF
usage: $PROGNAME <command> [options]

Commands:
  drain    Route actionable tasks to subscription headroom until done.
  observe  Sample capacity across reset boundaries without dispatching.
  report   Render the ledger written by drain.
  yield    Report how much observed capacity expired unused, and what was
           harvested against it.

Options for drain:
  --project-id <id>     Deck project id (default: \$NOISEDECK_PROJECT_ID)
  --targets <list>      Ordered provider=model pairs, comma separated
                        (default: claude=fable,codex=gpt-5.6-sol)
  --ceiling <pct>       Never dispatch to a provider with any capacity window
                        at or above this used percentage (default: 80)
  --policy <name>       most-idle (default) or preference. most-idle picks the
                        qualifying target whose busiest window is least used;
                        preference picks the first qualifying target in order.
  --timeout <dur>       Acceptance wait per handoff, at most 5m (default: 4m)
  --completion-timeout <dur>
                        Closure wait per handoff (default: 20m)
  --max-sleep <secs>    Longest single wait for a window reset (default: 36000)
  --max-tasks <n>       Stop after routing n tasks (default: unlimited)
  --dry-run             Print the routing decision for the current evidence
                        and exit without dispatching.

Options for observe:
  --interval <secs>     Sampling period away from a reset (default: 1800)
  --near-interval <secs>
                        Sampling period near a reset (default: 60)
  --near-window <secs>  How close to a reset counts as near (default: 300)
  --for <secs>          Stop after this long, then report (default: run until
                        stopped)

The ledger is $LEDGER in the project directory: one JSON object per
dispatched handoff, carrying only normalized capacity evidence.
EOF
}

project_id="${NOISEDECK_PROJECT_ID:-}"
targets="claude=fable,codex=gpt-5.6-sol"
ceiling=80
policy="most-idle"
timeout="4m"
completion_timeout="20m"
max_sleep=36000
max_tasks=0
dry_run=""
observe_interval=1800
near_interval=60
near_window=300
observe_for=0

require_option_value() {
  if [ "$#" -lt 2 ]; then
    echo "$PROGNAME: $1 requires a value" >&2
    usage >&2
    exit 2
  fi
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --project-id) require_option_value "$@"; project_id="$2"; shift 2 ;;
      --targets) require_option_value "$@"; targets="$2"; shift 2 ;;
      --ceiling) require_option_value "$@"; ceiling="$2"; shift 2 ;;
      --policy) require_option_value "$@"; policy="$2"; shift 2 ;;
      --timeout) require_option_value "$@"; timeout="$2"; shift 2 ;;
      --completion-timeout) require_option_value "$@"; completion_timeout="$2"; shift 2 ;;
      --max-sleep) require_option_value "$@"; max_sleep="$2"; shift 2 ;;
      --max-tasks) require_option_value "$@"; max_tasks="$2"; shift 2 ;;
      --dry-run) dry_run=1; shift ;;
      --interval) require_option_value "$@"; observe_interval="$2"; shift 2 ;;
      --near-interval) require_option_value "$@"; near_interval="$2"; shift 2 ;;
      --near-window) require_option_value "$@"; near_window="$2"; shift 2 ;;
      --for) require_option_value "$@"; observe_for="$2"; shift 2 ;;
      --help|-h) usage; exit 0 ;;
      *) echo "$PROGNAME: unknown option: $1" >&2; usage >&2; exit 2 ;;
    esac
  done
  case "$policy" in most-idle|preference) ;; *)
    echo "$PROGNAME: --policy must be most-idle or preference" >&2; exit 2 ;;
  esac
  # A zero cadence would spin against the provider CLIs, and a negative
  # duration would read as the unlimited default rather than as the mistake
  # it is.
  for pair in "--interval:$observe_interval" "--near-interval:$near_interval"               "--near-window:$near_window"; do
    case "${pair#*:}" in
      ''|*[!0-9]*) echo "$PROGNAME: ${pair%%:*} must be a positive whole number of seconds" >&2; exit 2 ;;
      0) echo "$PROGNAME: ${pair%%:*} must be greater than zero" >&2; exit 2 ;;
    esac
  done
  case "$observe_for" in
    ''|*[!0-9]*) echo "$PROGNAME: --for must be a whole number of seconds" >&2; exit 2 ;;
  esac
}

require_environment() {
  if [ -z "${NOISEDECK_EXPERIMENT_CAPABILITY:-}" ]; then
    echo "FAIL: NOISEDECK_EXPERIMENT_CAPABILITY is not set." >&2
    echo "Fix: launch this loop from a Deck session; a plain shell cannot" >&2
    echo "     reach the experiment surface." >&2
    exit 1
  fi
  command -v python3 >/dev/null 2>&1 || {
    echo "FAIL: python3 is required to read normalized capacity evidence." >&2
    exit 1
  }
}

harnesses_json() {
  noise deck harnesses list --json
}

# select_target emits one "REFUSE<TAB>provider<TAB>reason" line per
# disqualified target, then a final decision line: "PICK<TAB>provider<TAB>
# model<TAB>busiest%", "SLEEP<TAB><epoch seconds>" when every target is at or
# above the ceiling and a future reset exists, or "NONE" when no target can
# ever qualify. A provider whose capacity state is anything but available is
# refused outright and contributes no reset to sleep for: unknown is never
# available capacity.
select_target() {
  local evidence="$1"
  python3 - "$evidence" "$targets" "$ceiling" "$policy" <<'PY'
import json, sys, datetime
evidence, targets, ceiling, policy = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4]
doc = json.loads(evidence)
profiles = {p["id"]: p for p in doc.get("profiles", [])}
candidates = []
resets = []
for pair in targets.split(","):
    provider, _, model = pair.partition("=")
    provider, model = provider.strip(), model.strip()
    if not provider or not model:
        continue
    profile = profiles.get(provider)
    if not profile:
        print(f"REFUSE\t{provider}\tnot configured")
        continue
    state = profile.get("evidence", {}).get("state")
    if state != "available":
        print(f"REFUSE\t{provider}\tstate {state or 'unknown'}")
        continue
    windows = profile.get("evidence", {}).get("windows", [])
    if not windows:
        print(f"REFUSE\t{provider}\tno capacity windows")
        continue
    busiest = max(w.get("used_percent", 100) for w in windows)
    if busiest < ceiling:
        candidates.append((provider, model, busiest))
        continue
    print(f"REFUSE\t{provider}\tbusiest window {busiest}% >= ceiling {ceiling}%")
    now = datetime.datetime.now(datetime.timezone.utc)
    for w in windows:
        if w.get("used_percent", 0) < ceiling or not w.get("resets_at"):
            continue
        try:
            reset = datetime.datetime.fromisoformat(w["resets_at"].replace("Z", "+00:00"))
        except ValueError:
            continue
        if reset > now:
            resets.append(reset.timestamp())
if candidates:
    if policy == "most-idle":
        candidates.sort(key=lambda c: c[2])
    provider, model, busiest = candidates[0]
    print(f"PICK\t{provider}\t{model}\t{busiest}")
elif resets:
    print(f"SLEEP\t{int(min(resets))}")
else:
    print("NONE")
PY
}

provider_windows() {
  local evidence="$1" provider="$2"
  python3 - "$evidence" "$provider" <<'PY'
import json, sys
doc = json.loads(sys.argv[1])
for p in doc.get("profiles", []):
    if p["id"] == sys.argv[2]:
        windows = [
            {"name": w.get("name"), "scope": w.get("scope"),
             "used_percent": w.get("used_percent")}
            for w in p.get("evidence", {}).get("windows", [])
        ]
        print(json.dumps(windows))
        break
else:
    print("[]")
PY
}

# next_task_id prints the actionable task id, "DRAINED" when the engine
# reports no actionable task (a null next), or "ERROR" when the engine fails —
# an unreadable backlog must stop the run visibly, never masquerade as a
# drained board.
# record_observations appends one line per reported window, carrying only the
# normalized fields the capacity API already serves. A window's worth is only
# knowable across its reset: once it refills, how much of the previous one
# expired unused is unrecoverable unless it was observed first.
record_observations() {
  local evidence="$1"
  python3 - "$evidence" >> "$OBSERVATIONS" <<'PY'
import json, sys, datetime
doc = json.loads(sys.argv[1])
now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
for profile in doc.get("profiles", []):
    ev = profile.get("evidence", {})
    if ev.get("state") != "available":
        continue
    for w in ev.get("windows", []):
        print(json.dumps({
            "observed_at": now,
            "provider": profile["id"],
            "name": w.get("name"),
            "scope": w.get("scope"),
            "used_percent": w.get("used_percent"),
            "resets_at": w.get("resets_at"),
        }))
PY
}

next_task_id() {
  local out
  if ! out=$(noise tasks next -json 2>&1); then
    echo "night-shift: noise tasks next failed: $out" >&2
    echo "ERROR"
    return 0
  fi
  printf '%s' "$out" | python3 -c '
import json, sys
try:
    doc = json.load(sys.stdin)
except Exception:
    print("ERROR")
else:
    if doc is None:
        print("DRAINED")
    elif isinstance(doc, dict) and doc.get("id"):
        print(doc["id"])
    else:
        print("ERROR")'
}

resumed_applied_tasks() {
  # The applied receipts this run must not dispatch again. The drain loop's
  # own list is memory and the ledger is the durable record, so a restarted
  # run reads it before its first decision: a task a receipt has closed is
  # work already paid for, whatever the board still offers.
  #
  # Only the applied guard is restored. The one-failover budget stays
  # per-process: the ledger outlives a night, and counting last night's
  # attempts would retire a task tonight without dispatching it once.
  #
  # A final row with no newline is read, not discarded: a receipt that was
  # written whole and lost only its newline records work that was done, and
  # the restart reads the ledger BEFORE the next append repairs any boundary,
  # so discarding it would dispatch that task a second time. Only a fragment
  # that cannot be parsed is ignored, because a half-written line commits
  # nothing. Malformed anywhere else stops the run rather than forgetting a
  # task it may have paid for.
  [ -f "$LEDGER" ] || return 0
  python3 - "$LEDGER" <<'RESUME'
import json, sys

path = sys.argv[1]
with open(path, encoding="utf-8") as ledger:
    raw = ledger.read()
if not raw:
    print("")
    sys.exit(0)

lines = raw.split("\n")
torn = ""
if raw.endswith("\n"):
    lines = lines[:-1]
else:
    torn = lines.pop()

applied = []
for number, line in enumerate(lines, start=1):
    if not line.strip():
        continue
    try:
        row = json.loads(line)
    except ValueError as err:
        sys.stderr.write(
            "night-shift: %s line %d is not a readable receipt (%s); "
            "refusing to resume against a ledger it cannot account for.\n"
            % (path, number, err))
        sys.exit(1)
    if row.get("completed") is True and row.get("status") == "applied":
        task = str(row.get("task", "")).strip()
        if task and task not in applied:
            applied.append(task)

if torn.strip():
    try:
        row = json.loads(torn)
    except ValueError:
        sys.stderr.write(
            "night-shift: %s ends in a fragment that commits nothing; it is "
            "ignored and the rows before it stand.\n" % path)
    else:
        if row.get("completed") is True and row.get("status") == "applied":
            task = str(row.get("task", "")).strip()
            if task and task not in applied:
                applied.append(task)

print(" ".join(applied))
RESUME
}

append_receipt() {
  # A row cut mid-write leaves no newline, so appending straight onto it would
  # fuse the fragment and the next receipt into one unreadable line and lose
  # the work that receipt records. Close the boundary first, and answer the
  # only question that decides how: does the fragment parse?
  #
  #   It does — the receipt was complete and only its newline was lost, so
  #   terminating it keeps a real record.
  #   It does not — nothing was committed by a half-written line, so it moves
  #   aside to <ledger>.torn where you can still see it, and the
  #   ledger stays readable instead of stopping every later run.
  if [ -s "$LEDGER" ] && [ -n "$(tail -c 1 "$LEDGER")" ]; then
    python3 - "$LEDGER" <<'BOUNDARY'
import json, sys

path = sys.argv[1]
with open(path, encoding="utf-8") as ledger:
    raw = ledger.read()
head, _, tail = raw.rpartition("\n")
head = head + "\n" if head else ""
try:
    json.loads(tail)
except ValueError:
    # Order matters, because a death between any two steps must leave the
    # committed receipts intact: the replacement is written beside the ledger,
    # the fragment is preserved, and only then does the ledger become the new
    # file in one rename. An interruption before that rename leaves the ledger
    # exactly as it was.
    import os
    replacement = path + ".rewrite"
    with open(replacement, "w", encoding="utf-8") as rewritten:
        rewritten.write(head)
        rewritten.flush()
        os.fsync(rewritten.fileno())
    with open(path + ".torn", "a", encoding="utf-8") as quarantine:
        quarantine.write(tail + "\n")
        quarantine.flush()
        os.fsync(quarantine.fileno())
    os.replace(replacement, path)
    sys.stderr.write(
        "night-shift: %s ended in a fragment that commits nothing; it moved "
        "to %s.torn.\n" % (path, path))
else:
    with open(path, "a", encoding="utf-8") as ledger:
        ledger.write("\n")
BOUNDARY
  fi
  python3 - "$@" >> "$LEDGER" <<'PY'
import json, sys
keys = ["started_at", "finished_at", "task", "provider", "model",
        "correlation_id", "accepted", "completed", "status",
        "windows_before", "windows_after"]
row = dict(zip(keys, sys.argv[1:]))
for k in ("windows_before", "windows_after"):
    row[k] = json.loads(row[k])
for k in ("accepted", "completed"):
    row[k] = row[k] == "true"
print(json.dumps(row))
PY
}

cmd_drain() {
  parse_args "$@"
  require_environment
  if [ -z "$project_id" ]; then
    echo "$PROGNAME drain: --project-id is required (or set NOISEDECK_PROJECT_ID)" >&2
    exit 2
  fi

  local routed=0 applied_tasks="" attempt_task="" attempts=0
  applied_tasks=$(resumed_applied_tasks)
  if [ -n "$applied_tasks" ]; then
    echo "night-shift: resuming; the ledger already shows applied:$applied_tasks"
  fi
  while :; do
    if [ "$max_tasks" -gt 0 ] && [ "$routed" -ge "$max_tasks" ]; then
      echo "night-shift: routed $routed task(s); --max-tasks reached."
      break
    fi
    local task_id
    task_id=$(next_task_id)
    case "$task_id" in
      DRAINED)
        echo "night-shift: no actionable task remains; the board is drained."
        break ;;
      ERROR|"")
        echo "night-shift: the task engine did not return a readable next task; stopping." >&2
        exit 1 ;;
    esac
    case " $applied_tasks " in *" $task_id "*)
      echo "night-shift: task $task_id closed as applied but the board still returns it; refusing to dispatch it again." >&2
      exit 1 ;;
    esac
    if [ "$task_id" = "$attempt_task" ]; then
      if [ "$attempts" -ge 2 ]; then
        echo "night-shift: task $task_id failed its dispatch and its one failover; stopping rather than burning the remaining window." >&2
        exit 1
      fi
    else
      attempt_task="$task_id"
      attempts=0
    fi

    local evidence selection choice
    evidence=$(harnesses_json)
    record_observations "$evidence"
    selection=$(select_target "$evidence")
    while IFS=$'\t' read -r kind a b _; do
      [ "$kind" = "REFUSE" ] && echo "night-shift: refused $a ($b)"
    done <<< "$selection"
    choice=$(printf '%s' "$selection" | grep -E '^(PICK|SLEEP|NONE)') || {
      echo "night-shift: target selection produced no decision; stopping." >&2
      exit 1
    }
    case "$choice" in
      NONE)
        echo "night-shift: no configured target qualifies and no future reset exists; stopping." >&2
        exit 1 ;;
      SLEEP*)
        local reset_at now wait
        reset_at=$(printf '%s' "$choice" | cut -f2)
        now=$(date +%s)
        wait=$((reset_at - now + 60))
        if [ "$wait" -gt "$max_sleep" ]; then
          echo "night-shift: every window is at or above ${ceiling}% and the nearest reset is ${wait}s away (past --max-sleep); stopping." >&2
          exit 1
        fi
        echo "night-shift: every window is at or above ${ceiling}%; sleeping ${wait}s until the nearest reset."
        [ -n "$dry_run" ] && exit 0
        sleep "$wait"
        continue ;;
    esac

    local provider model busiest
    provider=$(printf '%s' "$choice" | cut -f2)
    model=$(printf '%s' "$choice" | cut -f3)
    busiest=$(printf '%s' "$choice" | cut -f4)
    echo "night-shift: task $task_id -> $provider/$model (busiest window ${busiest}% used, policy $policy, ceiling ${ceiling}%)"
    if [ -n "$dry_run" ]; then
      echo "night-shift: dry run; nothing dispatched."
      exit 0
    fi
    attempts=$((attempts + 1))

    local before started finished result accepted completed status corr
    before=$(provider_windows "$evidence" "$provider")
    corr="ns-$(date +%s)-$task_id"
    started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    set +e
    result=$(noise deck agent-handoff \
      --project-id "$project_id" --provider "$provider" --model "$model" \
      --role night-shift --task "$task_id" --correlation-id "$corr" \
      --timeout "$timeout" --completion-timeout "$completion_timeout" --json)
    set -e
    finished=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    accepted=$(printf '%s' "$result" | python3 -c 'import json,sys
try: print(str(json.load(sys.stdin).get("accepted",False)).lower())
except Exception: print("false")')
    completed=$(printf '%s' "$result" | python3 -c 'import json,sys
try: print(str(json.load(sys.stdin).get("completed",False)).lower())
except Exception: print("false")')
    status=$(printf '%s' "$result" | python3 -c 'import json,sys
try: print(json.load(sys.stdin).get("completion",{}).get("status") or "none")
except Exception: print("unparsed")')
    local after
    after=$(provider_windows "$(harnesses_json)" "$provider")
    append_receipt "$started" "$finished" "$task_id" "$provider" "$model" \
      "$corr" "$accepted" "$completed" "$status" "$before" "$after"

    if [ "$completed" = "true" ] && [ "$status" = "applied" ]; then
      routed=$((routed + 1))
      applied_tasks="$applied_tasks $task_id"
      attempt_task=""
      attempts=0
      echo "night-shift: task $task_id applied by $provider/$model."
      continue
    fi
    echo "night-shift: task $task_id did not close on $provider (accepted=$accepted status=$status); its session stays visible for inspection."
    # The attempt counter above allows exactly one more pass for this task —
    # the single failover — before the run stops.
  done
  cmd_report
}

cmd_report() {
  if [ ! -f "$LEDGER" ]; then
    echo "night-shift: no ledger at ./$LEDGER"
    return 0
  fi
  python3 - "$LEDGER" <<'PY'
import json, sys, datetime
rows, skipped = [], 0
for line in open(sys.argv[1]):
    if not line.strip():
        continue
    try:
        row = json.loads(line)
        row["task"], row["provider"], row["status"]
        rows.append(row)
    except Exception:
        skipped += 1
if skipped:
    print(f"night-shift: skipped {skipped} unreadable ledger line(s).", file=sys.stderr)
if not rows:
    print("night-shift: the ledger is empty.")
    raise SystemExit(0)

def span(row):
    try:
        a = datetime.datetime.fromisoformat(row["started_at"].replace("Z", "+00:00"))
        b = datetime.datetime.fromisoformat(row["finished_at"].replace("Z", "+00:00"))
        return f"{int((b - a).total_seconds())}s"
    except Exception:
        return "?"

def delta(row):
    before = {(w["name"], w["scope"]): w["used_percent"] for w in row["windows_before"]}
    moves = []
    for w in row["windows_after"]:
        key = (w["name"], w["scope"])
        if key in before and w["used_percent"] != before[key]:
            moves.append(f'{w["name"]}/{w["scope"]} {before[key]}%->{w["used_percent"]}%')
    return ", ".join(moves) if moves else "no window moved"

print(f'{"task":>4}  {"target":<22} {"status":<8} {"wall":>6}  window movement')
totals = {}
for r in rows:
    target = f'{r["provider"]}/{r["model"]}'
    print(f'{r["task"]:>4}  {target:<22} {r["status"]:<8} {span(r):>6}  {delta(r)}')
    if r["status"] == "applied":
        totals[r["provider"]] = totals.get(r["provider"], 0) + 1
applied = sum(totals.values())
print(f"\n{applied}/{len(rows)} handoffs applied", end="")
if totals:
    print(" (" + ", ".join(f"{k}: {v}" for k, v in sorted(totals.items())) + ")", end="")
print(f"; ledger spans {rows[0]['started_at']} .. {rows[-1]['finished_at']}")
PY
}

# seconds_until_nearest_reset prints the seconds to the soonest future reset
# across every reported window, or an empty string when none is reported.
seconds_until_nearest_reset() {
  python3 - "$1" <<'PY'
import json, sys, datetime
doc = json.loads(sys.argv[1])
now = datetime.datetime.now(datetime.timezone.utc)
soonest = None
for profile in doc.get("profiles", []):
    for w in profile.get("evidence", {}).get("windows", []):
        if not w.get("resets_at"):
            continue
        try:
            at = datetime.datetime.fromisoformat(w["resets_at"].replace("Z", "+00:00"))
        except ValueError:
            continue
        if at > now and (soonest is None or at < soonest):
            soonest = at
if soonest:
    print(int((soonest - now).total_seconds()))
PY
}

cmd_observe() {
  parse_args "$@"
  require_environment
  local deadline=""
  if [ "$observe_for" -gt 0 ]; then
    deadline=$(( $(date +%s) + observe_for ))
  fi
  echo "night-shift: sampling capacity every ${observe_interval}s, and every ${near_interval}s within ${near_window}s of a reset."
  local failures=0
  while :; do
    local evidence
    if ! evidence=$(harnesses_json 2>/dev/null); then
      failures=$((failures + 1))
      if [ "$failures" -ge 5 ]; then
        echo "night-shift: capacity read failed $failures times; stopping. The samples already recorded stay valid." >&2
        exit 1
      fi
      local backoff=$near_interval
      [ "$backoff" -gt 300 ] && backoff=300
      echo "night-shift: capacity read failed (attempt $failures); retrying in ${backoff}s." >&2
      sleep "$backoff"
      continue
    fi
    failures=0
    record_observations "$evidence"
    local until_reset sleep_for
    until_reset=$(seconds_until_nearest_reset "$evidence")
    # A reset is the only moment that proves what the previous window wasted,
    # so sampling tightens around it and stays sparse the rest of the time.
    sleep_for="$observe_interval"
    if [ -n "$until_reset" ] && [ "$until_reset" -le "$near_window" ]; then
      sleep_for="$near_interval"
    fi
    if [ -n "$deadline" ]; then
      local remaining=$((deadline - $(date +%s)))
      if [ "$remaining" -le 0 ]; then
        echo "night-shift: observation window elapsed."
        break
      fi
      [ "$sleep_for" -gt "$remaining" ] && sleep_for="$remaining"
    fi
    sleep "$sleep_for"
  done
  cmd_yield
}

cmd_yield() {
  if [ ! -f "$OBSERVATIONS" ]; then
    echo "night-shift: no observations at ./$OBSERVATIONS; run observe or drain first."
    return 0
  fi
  python3 - "$OBSERVATIONS" "$LEDGER" <<'PY'
import json, sys, os, datetime

def load(path, required):
    rows, skipped = [], 0
    if not os.path.exists(path):
        return rows, skipped
    for line in open(path):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
            for key in required:
                row[key]
            rows.append(row)
        except Exception:
            skipped += 1
    return rows, skipped

obs, skipped = load(sys.argv[1], ("observed_at", "provider", "name", "scope", "used_percent"))
if skipped:
    print(f"night-shift: skipped {skipped} unreadable observation(s).", file=sys.stderr)
if not obs:
    print("night-shift: no readable observations.")
    raise SystemExit(0)

def instant(value):
    # A zone-less timestamp is unusable rather than assumed: comparing it with
    # an offset-aware one raises, and guessing a zone would silently reorder
    # the boundary the whole report rests on. The documented used-percent
    # fallback covers it.
    if not value:
        return None
    try:
        parsed = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except (AttributeError, ValueError):
        return None
    return parsed if parsed.tzinfo else None

series = {}
for row in obs:
    series.setdefault((row["provider"], row["name"], row["scope"]), []).append(row)

# A reset is the provider moving its own reset instant forward; a used-percent
# drop is the fallback for a window that reports no instant. Only an observed
# reset proves the previous window's fate, so an unreset window is reported as
# still open rather than counted as waste.
resets, open_windows = [], []
for key, rows in sorted(series.items()):
    rows.sort(key=lambda r: r["observed_at"])
    for prev, cur in zip(rows, rows[1:]):
        before, after = instant(prev.get("resets_at")), instant(cur.get("resets_at"))
        if before and after:
            # The provider's own instant is authoritative. A usage correction
            # inside one window must not read as a reset, so the fallback
            # below is only for a window that reports no usable instant.
            rolled = after > before
        else:
            rolled = cur["used_percent"] < prev["used_percent"]
        if rolled:
            resets.append({
                "key": key, "at": cur["observed_at"],
                "peak": prev["used_percent"], "after": cur["used_percent"],
            })
    open_windows.append({"key": key, "used": rows[-1]["used_percent"],
                         "resets_at": rows[-1].get("resets_at"), "samples": len(rows)})

span = f'{obs[0]["observed_at"]} .. {obs[-1]["observed_at"]}'
print(f"Observed {len(obs)} capacity samples across {len(series)} window(s); {span}\n")

if resets:
    print("Windows observed resetting — utilisation when capacity expired:")
    print(f'  {"window":<34} {"reset observed":<21} {"last seen":>10} {"expired >=":>11}')
    wasted = []
    for r in resets:
        provider, name, scope = r["key"]
        label = f"{provider} {name}/{scope}"
        expired = 100 - r["peak"]
        wasted.append(expired)
        print(f'  {label:<34} {r["at"]:<21} {r["peak"]:>9}% {expired:>10}%')
    print(f"\n  {len(wasted)} reset(s) observed; at least "
          f"{sum(wasted)/len(wasted):.0f}% of a window expired unused on average.")
else:
    print("No window was observed resetting yet: utilisation is only provable across a reset.")

harvest, _ = load(sys.argv[2], ("task", "provider", "status"))
applied = [r for r in harvest if r.get("status") == "applied"]
if applied:
    by_provider = {}
    for r in applied:
        by_provider[r["provider"]] = by_provider.get(r["provider"], 0) + 1
    detail = ", ".join(f"{k}: {v}" for k, v in sorted(by_provider.items()))
    print(f"\nHarvested against that capacity: {len(applied)} task(s) applied ({detail}).")

print("\nCurrent windows:")
for w in sorted(open_windows, key=lambda x: x["key"]):
    provider, name, scope = w["key"]
    reset = w["resets_at"] or "no reported reset"
    print(f'  {provider} {name}/{scope}: {w["used"]}% used, resets {reset} ({w["samples"]} sample(s))')
print("\nPercentages are the provider's own window figures. \"Last seen\" is the"
      "\nfinal sample before the reset, so real use may have been higher between"
      "\nsamples and the expired share is a floor, not a measurement; sample more"
      "\noften near a reset to tighten it. Unused capacity at reset is not"
      "\nconverted to tokens or currency, which are not subscription capacity.")
PY
}

main() {
  if [ $# -eq 0 ]; then
    usage >&2
    exit 2
  fi
  case "$1" in
    drain) shift; cmd_drain "$@" ;;
    observe) shift; cmd_observe "$@" ;;
    yield) shift; parse_args "$@"; cmd_yield ;;
    report) shift; parse_args "$@"; cmd_report ;;
    --help|-h) usage; exit 0 ;;
    *) echo "$PROGNAME: unknown command: $1" >&2; usage >&2; exit 2 ;;
  esac
}

main "$@"
