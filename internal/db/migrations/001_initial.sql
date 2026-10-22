-- Migration 001 — the control database's initial schema.
--
-- This database is the sole authority for recorded state concerning projects,
-- application sessions and their lifecycle, controller leases, durable input
-- intents, Taskmaster command executions, and lifecycle/audit events
-- (PRD §9.1). It is NOT an authority on reality: a row here records what was
-- last written, never what is observably true of tmux or the process table.
--
-- Timestamps are ISO-8601 UTC strings ('2026-07-30T17:00:00.000Z') so they sort
-- lexicographically and are readable in a sqlite3 shell. The one exception is
-- process start time, which is an opaque kernel value compared for equality as
-- part of process identity, never ordered — it is stored as INTEGER.

CREATE TABLE projects (
  id             TEXT PRIMARY KEY,
  name           TEXT NOT NULL,
  -- Canonical real path after symlink resolution (FR-P2, FR-P3, NFR-SEC3).
  canonical_path TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  last_used_at   TEXT NOT NULL,
  -- Removal is a soft state. FR-P4 requires that removing a project from Agent
  -- Console leaves its files alone, and NFR-SEC8 requires removal to be
  -- auditable — neither is served by a row delete, which would also have to
  -- destroy or orphan the project's session and audit history. Removal hides
  -- the project; it does not erase what happened in it.
  removed_at     TEXT
) STRICT;

-- Uniqueness applies to ACTIVE projects only, so a directory can be registered
-- again after its previous registration was removed. A plain UNIQUE column
-- would make removal permanent for that path.
CREATE UNIQUE INDEX idx_projects_active_path
  ON projects (canonical_path) WHERE removed_at IS NULL;

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,
  -- Removing a project must not delete its files (FR-P4), and must not
  -- silently destroy session history either; deletion is refused while
  -- sessions reference it.
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  provider   TEXT NOT NULL CONSTRAINT chk_sessions_provider
               CHECK (provider IN ('claude', 'codex', 'kimi')),

  -- The durable runtime identity tuple (FR-S13). A tmux session NAME alone is
  -- insufficient — names are reused — so reconciliation compares this whole
  -- tuple and treats any mismatch as needs_attention. Fields are nullable
  -- because they are only known once the pane exists.
  tmux_socket_path    TEXT NOT NULL,
  -- The NAME is how a session is addressed; it is reusable and therefore not
  -- identity. The ID (tmux's `$N`) is stable for the lifetime of the tmux
  -- server and is what distinguishes this session from a later one that
  -- happened to take the same name — the case FR-S11 warns about.
  tmux_session_name   TEXT NOT NULL,
  tmux_session_id     TEXT,
  tmux_pane_id        TEXT,
  -- Incremented on every launch or respawn. Invalidates outstanding controller
  -- and release fencing tokens (FR-S11, FR-R11).
  generation          INTEGER NOT NULL DEFAULT 1
                      CONSTRAINT chk_sessions_generation CHECK (generation >= 1),
  -- Value also written as a tmux pane option, so a pane can be proven to be
  -- the one we launched (FR-S11).
  identity_marker     TEXT,
  pane_pid            INTEGER,
  process_group_id    INTEGER,
  process_start_ticks INTEGER,
  expected_executable TEXT,

  -- Established at launch via a documented CLI contract, never by parsing
  -- provider transcripts or state files (FR-A6, FR-R7).
  native_session_id TEXT,

  status TEXT NOT NULL CONSTRAINT chk_sessions_status CHECK (status IN (
    'starting', 'running', 'exited', 'interrupted', 'needs_attention'
  )),

  -- Exit evidence (FR-R4). exited_at is required to distinguish "never exited"
  -- from "exited with status 0".
  exit_code   INTEGER,
  exit_signal TEXT,
  exited_at   TEXT,

  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,

  -- Exit evidence is constrained in BOTH directions. One-directional checks
  -- leave the two states that actually mislead an operator representable: a
  -- session claiming to have exited with no evidence, and a live session
  -- carrying an exit code.
  --
  -- A session cannot carry exit evidence while claimed to be live (FR-R3).
  CONSTRAINT chk_sessions_no_exit_while_live
  CHECK (exited_at IS NULL OR status IN ('exited', 'interrupted', 'needs_attention')),
  -- FR-R4: an observed exit records when it happened AND how it ended. The two
  -- checks together permit exactly one outcome — a status or a signal, never
  -- both and never neither.
  --
  -- This is what separates `exited` from `interrupted`. `exited` means the exit
  -- was observed and its cause is known. A process that is simply gone —
  -- discovered missing by reconciliation after a restart, with no exit status
  -- recoverable — is `interrupted`, not `exited` with an empty cause. Making
  -- the unknown case unrepresentable here is what forces that distinction to
  -- be made honestly at the call site.
  CONSTRAINT chk_sessions_exit_cause
  CHECK (
    status <> 'exited'
    OR (exited_at IS NOT NULL AND (exit_code IS NOT NULL OR exit_signal IS NOT NULL))
  ),
  -- No exit code or signal without a recorded exit.
  CONSTRAINT chk_sessions_exit_fields_need_exit
  CHECK (exited_at IS NOT NULL OR (exit_code IS NULL AND exit_signal IS NULL)),
  -- A process exits with a status or on a signal, never both.
  CONSTRAINT chk_sessions_exit_code_xor_signal
  CHECK (exit_code IS NULL OR exit_signal IS NULL),

  -- FR-S13: a session may only be recorded as running once its complete
  -- runtime identity tuple is known. "A name, PID, or command line alone is
  -- insufficient" — so a row asserting `running` while missing the fields
  -- reconciliation compares would be an unverifiable claim of liveness, which
  -- is the failure this schema exists to prevent. `starting` permits nulls
  -- because the pane does not exist yet.
  CONSTRAINT chk_sessions_running_identity
  CHECK (
    status <> 'running'
    OR (tmux_session_id IS NOT NULL
        AND tmux_pane_id IS NOT NULL
        AND identity_marker IS NOT NULL
        AND pane_pid IS NOT NULL
        AND process_group_id IS NOT NULL
        AND process_start_ticks IS NOT NULL
        AND expected_executable IS NOT NULL)
  )
) STRICT;

-- One controller attachment per session (FR-S6). PRIMARY KEY on session_id is
-- what enforces it; a second attachment sees a takeover screen until it
-- deliberately takes control.
CREATE TABLE controller_leases (
  session_id     TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  controller_id  TEXT NOT NULL,
  -- Atomically incremented on deliberate takeover. Every raw-input frame and
  -- release request carries a token the server validates before writing
  -- (FR-S12).
  fencing_token  INTEGER NOT NULL DEFAULT 1
                 CONSTRAINT chk_leases_fencing_token CHECK (fencing_token >= 1),
  -- Renewed at least every 5s; expires no later than 15s after heartbeat loss.
  last_heartbeat TEXT NOT NULL,
  expires_at     TEXT NOT NULL
) STRICT;

-- The durable input outbox (PRD §7.4).
CREATE TABLE composer_messages (
  -- Client-generated UUID. Replaying it with the same body returns this record
  -- rather than enqueuing a second copy; replaying with a different body is a
  -- conflict (FR-Q4).
  id TEXT PRIMARY KEY,
  -- RESTRICT, not CASCADE. Cascading here would let deleting a session erase
  -- unresolved ambiguous input — the exact records the operator has been
  -- promised will not vanish, and which NFR-SEC15 forbids retention from
  -- touching. Session deletion is not a supported operation; history is kept.
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE RESTRICT,
  -- Transactionally allocated, monotonically ordered per session (FR-Q1).
  -- FIFO ordering applies to every nonterminal record (FR-Q17).
  sequence   INTEGER NOT NULL CONSTRAINT chk_messages_sequence CHECK (sequence >= 1),

  -- Nullable ONLY because completed bodies are removed after the retention
  -- window while transition metadata is retained (NFR-SEC15). The CHECK below
  -- makes it impossible to remove a body that is still live.
  body            TEXT,
  -- Retained after body removal, so a UUID replay can still be classified as
  -- same-body or conflicting (FR-Q4) once the body itself is gone.
  body_sha256     TEXT NOT NULL,
  body_removed_at TEXT,

  status TEXT NOT NULL CONSTRAINT chk_messages_status CHECK (status IN (
    'held', 'queued', 'injecting', 'injected',
    'ambiguous', 'assumed_delivered', 'skipped', 'retry_linked', 'cancelled'
  )),

  -- A release is bound to the controller's fencing token, the session
  -- generation, and the exact pane (FR-Q7). Recorded on release.
  released_token  INTEGER,
  pane_generation INTEGER,
  target_pane_id  TEXT,

  -- Recorded when the injecting transition is committed, BEFORE tmux is
  -- touched, so a crash at any point afterwards is provably ambiguous
  -- (FR-Q3, FR-Q12).
  backend_instance    TEXT,
  inject_attempted_at TEXT,

  -- Set when an ambiguous item is resolved by creating a new linked record.
  -- Both audit records are preserved; the original is never replayed (FR-Q13).
  retry_of TEXT REFERENCES composer_messages(id) ON DELETE RESTRICT,

  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,

  UNIQUE (session_id, sequence),

  -- NFR-SEC15, enforced by the database rather than trusted to the retention
  -- job: held, queued, injecting, and unresolved ambiguous bodies can never be
  -- removed, because those are exactly the messages an operator may still need
  -- to read before deciding what to do with them.
  CONSTRAINT chk_messages_retention
  CHECK (
    body_removed_at IS NULL
    OR status IN ('injected', 'assumed_delivered', 'skipped', 'retry_linked', 'cancelled')
  ),
  -- A message always has either its body or a record of when it was removed.
  CONSTRAINT chk_messages_body_present
  CHECK (body IS NOT NULL OR body_removed_at IS NOT NULL),
  -- A message cannot be its own retry target.
  CONSTRAINT chk_messages_retry_not_self
  CHECK (retry_of IS NULL OR retry_of <> id),

  -- The evidence FR-Q3 and FR-Q7 require is not optional, so the states that
  -- imply it cannot be recorded without it. Without these, a row could claim
  -- `injecting` while recording nothing about WHICH pane it was injecting
  -- into — leaving an ambiguous item the operator cannot investigate, which
  -- defeats the purpose of committing the transition before touching tmux.
  --
  -- FR-Q7: release is bound to controller token, generation, and pane
  -- identity. Every state at or past `queued` carries that binding.
  -- `cancelled` is excluded: FR-Q14 permits cancelling from `held`, which was
  -- never released.
  CONSTRAINT chk_messages_release_binding
  CHECK (
    status NOT IN ('queued', 'injecting', 'injected', 'ambiguous',
                   'assumed_delivered', 'skipped', 'retry_linked')
    OR (released_token IS NOT NULL
        AND pane_generation IS NOT NULL
        AND target_pane_id IS NOT NULL)
  ),
  -- FR-Q3: the injecting commit records backend instance and attempt time.
  -- Every state reachable only THROUGH injecting therefore carries them.
  CONSTRAINT chk_messages_inject_evidence
  CHECK (
    status NOT IN ('injecting', 'injected', 'ambiguous',
                   'assumed_delivered', 'skipped', 'retry_linked')
    OR (backend_instance IS NOT NULL AND inject_attempted_at IS NOT NULL)
  )
) STRICT;

-- Taskmaster command executions (PRD §9.1, FR-T5, FR-T6).
CREATE TABLE taskmaster_runs (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  -- Fixed executable plus argv, stored as a JSON array. User text is never
  -- evaluated by a shell (NFR-SEC4), so this records exactly what was executed.
  argv       TEXT NOT NULL,
  prd_path   TEXT,
  -- Runs as an attachable managed tmux job so progress and exit status survive
  -- a web-service restart (FR-T5).
  tmux_session_name TEXT,

  status TEXT NOT NULL CONSTRAINT chk_taskmaster_status
         CHECK (status IN ('running', 'exited')),
  exit_code INTEGER,
  stdout    TEXT,
  stderr    TEXT,

  started_at  TEXT NOT NULL,
  finished_at TEXT,

  CONSTRAINT chk_taskmaster_finished
  CHECK (status <> 'exited' OR finished_at IS NOT NULL)
) STRICT;

-- Generation is serialized per project (FR-T6): at most one running execution
-- per project, enforced by a partial unique index rather than application
-- logic.
CREATE UNIQUE INDEX idx_taskmaster_one_running
  ON taskmaster_runs (project_id) WHERE status = 'running';

-- Ordered audit log. Every lifecycle and outbox transition appends an event in
-- the SAME transaction as the state change (NFR-REL10); the autoincrementing
-- id provides the total order.
--
-- `details` carries identifiers and state transitions only. It must never
-- contain prompt bodies, terminal contents, environment secrets, or
-- authentication material (NFR-SEC7).
-- References are RESTRICT rather than SET NULL: an audit row whose subject has
-- been nulled out is worse than no audit row, because it still looks like
-- evidence while no longer identifying what it is evidence of. Control
-- takeover, forced termination, project removal, and ambiguous-message
-- resolution must remain auditable (NFR-SEC8), which they cannot be if the
-- correlation can be erased by a delete elsewhere.
CREATE TABLE audit_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT REFERENCES sessions(id) ON DELETE RESTRICT,
  message_id TEXT REFERENCES composer_messages(id) ON DELETE RESTRICT,
  event_type TEXT NOT NULL,
  details    TEXT,
  created_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_sessions_project        ON sessions (project_id);
CREATE INDEX idx_sessions_status         ON sessions (status);
CREATE INDEX idx_leases_expires          ON controller_leases (expires_at);
CREATE INDEX idx_messages_session_status ON composer_messages (session_id, status);
-- Serves the FIFO head lookup: the lowest-sequence nonterminal item per
-- session, which is the barrier the UI must show (FR-Q17).
CREATE INDEX idx_messages_fifo           ON composer_messages (session_id, sequence);
CREATE INDEX idx_taskmaster_project      ON taskmaster_runs (project_id, started_at);
CREATE INDEX idx_audit_session           ON audit_events (session_id, id);
CREATE INDEX idx_audit_message           ON audit_events (message_id, id);
