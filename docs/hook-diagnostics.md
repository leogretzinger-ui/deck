# Private hook diagnostics

When a real Codex task succeeds but its native identity is absent, normal
silent hooks do not reveal whether the adapter ran, rejected its input, or
could not deliver it. Set `NOISEDECK_HOOK_DIAGNOSTICS=true` in the installation
env file for a deliberately diagnostic deployment. It is off by default.
Changing this setting requires the normal Compose recreation;
it does not change running panes. Do not restart a live task just to obtain a
trace.

Deck forwards the flag on launch and resume only when it has created the
generation's private `TMPDIR`. The provisioned adapter and receiver append
`hook-diagnostics.jsonl` there, never to ordinary logs. The directory must be
owned by the runtime user with mode 0700; the file is mode 0600, regular,
single-linked and not a symlink. Each generation is capped at 4096 bytes;
nonblocking locking, invalid paths, unsafe permissions and full files cause a
silent no-op. The existing temporary-directory lifecycle owns cleanup. This
is diagnostic association, not isolation from another process with the same
user in the unconfined profile.

Records contain only fixed stage enums and, for receiver attempts, integer
attempt 1..3, curl_exit 0..255 and http_status 0 or 100..599 (0 means unavailable).
There are no tokens, URLs, payloads, native IDs, working directories, exception
messages, argv or provider output. Missing records are not proof the provider
never invoked a hook: diagnostics can fail silently too.

`adapter_entered` followed by an adapter rejection category identifies an
envelope problem. `adapter_forwarded` means the receiver process returned,
not that capture succeeded. `receiver_attempt` distinguishes transport errors
from HTTP rejection; 204/2xx is accepted, 4xx is terminal refusal, 5xx retries.
The normal receiver retains its three-attempt/eight-second delivery budget
and always exits 0. This mechanism diagnoses a stage; it does not fix missing
identity, synthesize a callback, infer native status, or alter hook commands
and their trust hashes.

Inspect only the exact owned generation's private file after a genuine task;
do not read provider transcripts or state stores. Backend rejection still
requires the receiver's HTTP category and the authenticated session status;
these records never disclose or reuse its bearer credential.
