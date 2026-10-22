-- foreign_keys: off
-- 022_cline_provider: widen sessions.provider for Cline while preserving the
-- complete released schema. SQLite cannot alter a CHECK constraint in place.

CREATE TABLE sessions_new (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  provider   TEXT NOT NULL CONSTRAINT chk_sessions_provider
               CHECK (provider IN ('claude', 'cline', 'command-code', 'codex', 'kimi', 'shell', 'opencode', 'pi', 'omp')),

  tmux_socket_path    TEXT NOT NULL,
  tmux_session_name   TEXT NOT NULL,
  tmux_session_id     TEXT,
  tmux_pane_id        TEXT,
  generation          INTEGER NOT NULL DEFAULT 1
                      CONSTRAINT chk_sessions_generation CHECK (generation >= 1),
  identity_marker     TEXT,
  pane_pid            INTEGER,
  process_group_id    INTEGER,
  process_start_ticks INTEGER,
  expected_executable TEXT,
  native_session_id   TEXT,

  status TEXT NOT NULL CONSTRAINT chk_sessions_status CHECK (status IN (
    'starting', 'running', 'exited', 'interrupted', 'needs_attention'
  )),
  exit_code   INTEGER,
  exit_signal TEXT,
  exited_at   TEXT,

  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  removed_at   TEXT,
  display_name TEXT NOT NULL DEFAULT '',

  resume_attempts          INTEGER NOT NULL DEFAULT 0
                           CONSTRAINT chk_sessions_resume_attempts CHECK (resume_attempts >= 0),
  resume_next_at           TEXT,
  resume_series_started_at TEXT,
  stop_requested_at        TEXT,
  last_output_at           TEXT,
  identity_capture_lost_at TEXT,
  resume_probation_until   TEXT,
  boot_epoch               TEXT,
  color                    TEXT,

  CONSTRAINT chk_sessions_no_exit_while_live
  CHECK (exited_at IS NULL OR status IN ('exited', 'interrupted', 'needs_attention')),
  CONSTRAINT chk_sessions_exit_cause
  CHECK (
    status <> 'exited'
    OR (exited_at IS NOT NULL AND (exit_code IS NOT NULL OR exit_signal IS NOT NULL))
  ),
  CONSTRAINT chk_sessions_exit_fields_need_exit
  CHECK (exited_at IS NOT NULL OR (exit_code IS NULL AND exit_signal IS NULL)),
  CONSTRAINT chk_sessions_exit_code_xor_signal
  CHECK (exit_code IS NULL OR exit_signal IS NULL),
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
  created_at, updated_at, removed_at, display_name,
  resume_attempts, resume_next_at, resume_series_started_at,
  stop_requested_at, last_output_at, identity_capture_lost_at,
  resume_probation_until, boot_epoch, color
)
SELECT
  id, project_id, provider,
  tmux_socket_path, tmux_session_name, tmux_session_id, tmux_pane_id,
  generation, identity_marker, pane_pid, process_group_id,
  process_start_ticks, expected_executable, native_session_id,
  status, exit_code, exit_signal, exited_at,
  created_at, updated_at, removed_at, display_name,
  resume_attempts, resume_next_at, resume_series_started_at,
  stop_requested_at, last_output_at, identity_capture_lost_at,
  resume_probation_until, boot_epoch, color
FROM sessions;

DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;

CREATE INDEX idx_sessions_project ON sessions (project_id);
CREATE INDEX idx_sessions_status  ON sessions (status);
