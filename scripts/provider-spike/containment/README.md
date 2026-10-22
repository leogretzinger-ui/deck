# Containment spike — procedure

Measures whether a provider's tool subprocesses stay inside the tmux pane's
process group, and what survives a SIGKILL to that group. Results are recorded
in `docs/provider-contracts/*.yaml` (`containment:` blocks) and must be
re-measured after any provider version change. This directory holds
the probe and the exact procedure that produced the recorded values on
2026-07-31.

The spike uses a private tmux socket so it can never touch your sessions,
and the one-invocation option/creation form from
`internal/tmux/launch.go` (with the option list in `internal/tmux/server.go`). Setting `remain-on-exit` after `new-session`
measurably loses immediately-exiting sessions, and `-f` is silently ignored
when a server already exists.

## Procedure (per provider)

```sh
WD=/workspace/noisedeck/.review/t39/<provider>-wd   # scratch, gitignored
SOCK=/workspace/noisedeck/.review/t39/<provider>.sock
mkdir -p "$WD"

# 1. Launch the interactive TUI as the pane process. Record the exact argv in
#    the contract. Used on 2026-07-31:
#      claude                                        (bare)
#      codex --dangerously-bypass-approvals-and-sandbox
#          (bwrap cannot work on this 4.4 kernel, so bypass is what this
#           platform would actually run)
#      kimi -y
tmux -S "$SOCK" -f /dev/null \
  set-option -g remain-on-exit on \; \
  set-option -g exit-empty off \; \
  new-session -d -P -F '#{pane_id} #{pane_pid}' -s spike -c "$WD" <argv…>

# 2. Drive the TUI by hand (capture-pane + send-keys): decline any
#    self-update offer (it would invalidate every recorded contract), answer
#    onboarding prompts, then submit:
#      Run this exact shell command in the foreground and wait for it:
#      python3 -c 'import time; time.sleep(180)'
#    Codex may need a second Enter to submit; input during a Claude modal is
#    silently discarded (see claude.yaml) — always re-capture the pane and
#    confirm the state before sending more keys.

# 3. While the sleep runs, enumerate the tree and keep the output:
python3 tree.py <pane_pid>          # comm(pid) ppid pgid sid tty per process

# 4. Kill the pane's process group and record survivors and the pane corpse:
kill -KILL -<pane_pid>              # pane_pid == pane pgid leader
sleep 2
python3 tree.py <each recorded descendant pid>   # or check /proc/<pid>
tmux -S "$SOCK" display-message -p -t %0 \
  'dead=#{pane_dead} status=#{pane_dead_status} signal=#{pane_dead_signal}'

# 5. Clean up: kill surviving probe children, then the spike server.
tmux -S "$SOCK" kill-server
```

What to record, measured only: the pre-kill tree (every descendant's pgid/sid
and whether it holds a controlling terminal), which processes survived the
group kill, where survivors were reparented, and the pane's dead
status/signal shape. Label anything not directly observed — e.g. *why*
codex's escaped children die — as inference, or leave it out.
