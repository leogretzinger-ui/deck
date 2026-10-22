-- foreign_keys: off
-- 009_opencode_provider: widen the sessions.provider CHECK for the opencode
-- provider (task 90). SQLite cannot alter a CHECK constraint, so the
-- table is rebuilt: copy into sessions_new under the widened schema, drop,
-- rename, recreate the indexes.
--
-- The marker above routes this migration through applyWithForeignKeysOff
-- (migrations.go): dropping a table that controller_leases and audit_events
-- reference requires foreign_keys=OFF around the transaction, and the
-- pragma cannot change inside one. defer_foreign_keys is not a substitute —
-- it only defers constraints declared DEFERRABLE, and ours are immediate
-- (measured: the DROP fails the commit-time FK check without the toggle).

CREATE TABLE sessions_new (
  id         TEXT PRIMARY KEY,
  -- Removing a project must not delete its files (FR-P4), and must not
  -- silently destroy session history either; deletion is refused while
  -- sessions reference it.
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  provider   TEXT NOT NULL CONSTRAINT chk_sessions_provider
               CHECK (provider IN ('claude', 'codex', 'kimi', 'shell', 'opencode')),

  -- The durable runtime identity tuple (FR-S13). A tmux session NAME alone is
  -- insufficient — names are reused — so reconciliation compares this whole
  -- tuple and treats any mismatch as needs_attention. Fields are nullable
  -- because they are only known once the pane exists.
  tmux_socket_path    TEXT NOT NULL,
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
  -- provider transcripts or state files (FR-A6, FR-R7). Identity-none
  -- providers (FR-A7) leave it NULL by contract.
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
  removed_at TEXT,
  display_name TEXT NOT NULL DEFAULT '',

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
  -- runtime identity tuple is known. `starting` permits nulls because the
  -- pane does not exist yet.
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

INSERT INTO sessions_new (
  id, project_id, provider,
  tmux_socket_path, tmux_session_name, tmux_session_id, tmux_pane_id,
  generation, identity_marker, pane_pid, process_group_id,
  process_start_ticks, expected_executable, native_session_id,
  status, exit_code, exit_signal, exited_at,
  created_at, updated_at, removed_at, display_name
)
SELECT
  id, project_id, provider,
  tmux_socket_path, tmux_session_name, tmux_session_id, tmux_pane_id,
  generation, identity_marker, pane_pid, process_group_id,
  process_start_ticks, expected_executable, native_session_id,
  status, exit_code, exit_signal, exited_at,
  created_at, updated_at, removed_at, display_name
FROM sessions;

DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;

CREATE INDEX idx_sessions_project ON sessions (project_id);
CREATE INDEX idx_sessions_status  ON sessions (status);
