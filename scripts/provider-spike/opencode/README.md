# opencode 1.18.4 Phase-0 spike

Measures whether the opencode CLI can satisfy the provider contract
(exact resumable identity, pinned version, no state scraping, injection
contract, containment). Results live in
`docs/provider-contracts/opencode.yaml`; every value there was produced by
the procedure below on 2026-08-04/2026-08-05 (host kernel 4.4.302+).

Files:

- `env.sh` — source it; isolates ALL opencode state (XDG config/data/cache/
  state + TMPDIR) into `.review/t90/` scratch, with the data home (which
  holds a copy of your `auth.json` for model access) on
  `/dev/shm` tmpfs so credentials never touch durable workspace scratch.
  Your real `~/.config/opencode` / `~/.local/share/opencode` are
  never written; the only file read from them is `auth.json` (model
  credentials require it), copied once onto tmpfs and destroyed after.
  `oc_setup` writes the pinned fixture config
  (`autoupdate: false`, cheap model `opencode-go/kimi-k2.6`,
  `permission: {"*": "allow"}`) and installs the plugin.
- `noisedeck-session-hook.js` — the identity-capture plugin under test. Subscribes
  to opencode's documented `session.created` event and POSTs
  `{session_id, cwd, source}` to `$NOISEDECK_SESSION_HOOK_URL` (quiet no-op when
  unset — the `deploy/provider-hook/noisedeck-provider-hook` contract). Also logs
  event types + timestamps to `$OC_SPIKE_EVENTS` for timing measurement.
  Never logs message content.
- `launch.sh` — pane command: execs `/usr/local/bin/opencode "$@"` in the
  isolated environment, so the pane process IS the provider.
- `sink.py` — 127.0.0.1 ephemeral-port HTTP sink recording hook POSTs.

## Procedure

```sh
source scripts/provider-spike/opencode/env.sh
oc_setup
setsid nohup python3 scripts/provider-spike/opencode/sink.py \
  "$T90/hook-posts.log" > "$T90/sink.port" &
sleep 1
PORT=$(awk '{print $2}' "$T90/sink.port")   # sink prints "PORT <n>"

# 1. Launch the interactive TUI as the pane process (private socket):
oc_tmux set-option -g remain-on-exit on \; set-option -g exit-empty off \; \
  new-session -d -P -F '#{pane_id} #{pane_pid}' -s spike -c "$OC_WD" \
  "NOISEDECK_SESSION_HOOK_URL=http://127.0.0.1:$PORT/hook NOISEDECK_SESSION_HOOK_TOKEN=t \
   $(pwd)/scripts/provider-spike/opencode/launch.sh"

# 2. Wait for the composer ('Ask anything' in capture-pane). Hold a 10s idle
#    window; count session.created lines in $T90/events.log (measured: 0).
# 3. Submit a distinctive prompt (send-keys -l + Enter). session.created
#    fires ~0.4-0.6s after the submission keystroke, before the reply; the
#    sink receives the POST with the native ses_* id.
# 4. Exact resume: kill -KILL -<pane_pid> (pane pgid == pane pid), then
#    respawn-pane -k with `launch.sh -s <id>`; verify history + a coherent
#    follow-up. Note: resume emits NO session.created (measured).
# 5. Unknown id: `launch.sh -s ses_0000000000000000000000000` exits status 1
#    (TUI pane dead status 1; `opencode run -s …` prints "Session not
#    found", exit 1) and emits no session.created.
# 6. Injection: `set-buffer` + `paste-buffer -p` a two-line body (held in
#    the composer until a separate Enter); type + Enter during an active
#    tool turn (rendered ' QUEUED', processed exactly once after the tool);
#    ctrl+p palette swallows typed/pasted input into its filter (never
#    reaches the composer); interrupt is TWO quick Escapes; idle Ctrl-C and
#    Ctrl-D both exit 0; SIGTERM is handled and reported as exit status 0.
# 7. Containment: follow scripts/provider-spike/containment/README.md with
#    `python3 -c 'import time; time.sleep(180)'` as the foreground tool
#    command; tree.py shows the tool child setsid'd out of the pane's group
#    and session (no ctty); SIGKILL to the pane group kills opencode
#    (pane_dead_signal=9) while the tool child survives, reparented to init.
# 8. Cleanup: kill surviving probe children, the tmux server, the sink, and
#    rm -rf /dev/shm/noisedeck-t90-data (destroys the tmpfs auth.json copy).
```

Model notes: `opencode-go/deepseek-v4-flash` refused ("only available
hosted in China… explicit opt in"); `opencode/deepseek-v4-flash-free` hung;
`opencode-go/kimi-k2.6` answered reliably. No onboarding or plugin-trust
modal ever appeared; no self-update prompt appeared with
`autoupdate: false`.

Two operational warnings, both measured:

- **`--pure` silently kills identity capture.** It runs opencode without
  external plugins, so the session works but no `session.created` handler
  ever runs. Launch/resume argv must never include it.
- **Plugin delivery is at-least-once.** One long-lived TUI process showed
  repeated `plugin.added` bursts (~hourly, no restart) — opencode reloads
  plugins in place and each load re-subscribes the handler. The capture
  receiver must dedupe on `session_id`; idempotency is a design
  requirement, not an edge case.
