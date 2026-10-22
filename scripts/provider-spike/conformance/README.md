# Real provider conformance probes

These opt-in tests establish the version-pinned Phase 0 contracts recorded in
`docs/provider-contracts/`. Ordinary `go test ./...` compiles and skips them.

Run the Devin probe with a CLI you have signed in to:

```sh
NOISEDECK_DEVIN_EXECUTABLE=/usr/local/bin/devin \
make provider-spike-devin
```

The version it measures is the contract's `version_tested`
(`docs/provider-contracts/devin.yaml`), so the probe and the contract cannot
drift; the run fails closed when the executable reports any other version.
A bump validates its candidate by naming it — `NOISEDECK_DEVIN_EXPECTED_VERSION=3000.6.14`
— and a pass is what moves `version_tested`, never the reverse. The private
fixture lives under the checkout's gitignored `.conformance/`, so a run from a
worktree never writes beside another lane's.

The Devin probe uses a private tmux namespace and a project-level documented
`SessionStart` hook. It verifies fresh identity, exact `--resume`, silent
unknown-id fallback discrimination, bracketed multiline input, active-turn
rejection, and SIGTERM without capturing terminal output or reading provider
state, session lists, transcripts, or logs. Authentication remains in the
your own Devin home; the harness neither reads nor copies credentials.

Run the Codex 0.145.0 probe from the repository root:

```sh
make provider-spike-codex
```

The real probe is Linux-only because exact cleanup uses `/proc` boot/process
identities and authentication isolation uses a Linux memfd. Non-Linux builds
compile a skipped conformance test and a runner that exits with that explicit
prerequisite instead of silently weakening the checks. On Linux, the target
fails closed unless the exact CLI, tmux, Git, Python, and provider
authentication are available. `CODEX_SPIKE_AUTH_JSON` may point at an existing
provider-owned `auth.json`; otherwise the probe references the current user's
Codex authentication. The source is copied only into a process-owned Linux
memfd and exposed to the isolated home through `/proc`; provider refreshes
therefore cannot mutate your file, and a hard process exit destroys
the in-memory copy. The entire writable Codex home is on a validated
`/dev/shm` tmpfs, so even a provider that atomically replaces the auth symlink
cannot write a credential into durable workspace scratch. Your
Codex configuration is never modified.

The isolated fixture pins `gpt-5.6-sol` with low reasoning, fast service tier,
approval `never`, `danger-full-access`, and a trusted test project. A separate
test-owned provisioning pass accepts Codex's native hook trust prompt. Because
the harness externally pins and verifies the exact CLI version, its isolated
config uses Codex's documented `check_for_update_on_startup = false` setting;
otherwise the updater reappears on exact resume even after choosing the native
"skip until next version" action. The measured launch and resume fail if any
provisioning modal appears; Deck never drives one.

Each run creates a mode-0700 private tmux namespace, project, and hook below
the repository's gitignored `.review/` tree, after refusing symlinked scratch
components. Its separate mode-0700 Codex home lives on tmpfs. The SessionStart
hook sends only `session_id`, `cwd`, and `source` over a mode-0600 Unix socket
into test-process memory; the hook receiver writes no additional native-ID or
payload file. Exact resume necessarily makes Codex persist its own native
session state inside the tmpfs home during the run. That provider-owned state
is never logged or committed and normal cleanup removes it. Visible terminal
observations also stay in memory.

The runner serializes probes with a file lock inherited by the `go test` child,
so killing only the runner cannot release the lock while a probe continues.
It records only boot ID and PID/start-tick identities for private panes and
test-owned tool sentinels. The boot ID prevents a persisted pre-reboot tuple
from targeting an unrelated post-reboot process. Ordinary test failures that
unwind through Go cleanup publish a certification before removing scratch. An
abrupt runner interrupt, test timeout, or hard kill may bypass that cleanup;
the next invocation then performs safe best-effort cleanup but retains the
sanitized current-boot run directory and refuses to continue when no
certification can exclude a process-creation/publication race. After a reboot,
no old process can survive: recorded PIDs are never signaled, refused sockets
are proved stale, and the old run can be removed. Raw prompts, terminal
captures, hook payloads, and transcripts are never reported or kept after a
run.

One lifecycle constraint is deliberate evidence, not setup noise: no
SessionStart hook was observed during a ten-second idle window after a fully
provisioned Codex 0.145.0 launch became ready. The first observed hook arrived
after the first user submission and before its reply. Any session-launch
implementation must account for that measured late-identity window; it may
not inject a synthetic prompt or pretend identity was observed at process
start.

A second probe, `TestCodexHookTrustPersistence`, measures how Codex 0.145.0
persists native hook trust: it diffs the test-owned home across the answered
trust modal, proves the recorded state is what a modal-free relaunch consumes,
and proves whether seeding that state into a fresh home provisions trust
headlessly. Its result constrains the 41(c) provisioning design and is
recorded in `docs/provider-contracts/codex.yaml`. It makes no submissions.

Terminal observation here is test evidence only. Deck runtime must
not parse provider output to infer identity, busy/idle state, modal state, or
delivery.
