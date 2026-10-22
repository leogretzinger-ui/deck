-- 025_session_recovery_candidates: durable successor admission (ADR-0005).
--
-- A recovery candidate is admitted before Deck invalidates a lease, creates a
-- tmux session, or registers a retrieved-identity hook. The partial unique
-- index is the cross-process controller fence: one session can have at most
-- one candidate in flight, even when two backend processes race during an
-- upgrade or failover. Completed nonces remain recorded so an HTTP retry can
-- never create a second candidate for the same operator gesture.

ALTER TABLE sessions ADD COLUMN controller_revision INTEGER NOT NULL DEFAULT 0
  CONSTRAINT chk_sessions_controller_revision CHECK (controller_revision >= 0);

-- Cosmetic metadata and output-activity timestamps deliberately do not bump
-- this fence. Every field that can change which local runtime generation the
-- row controls, or whether a successor may publish, does.
CREATE TRIGGER trg_sessions_controller_revision
AFTER UPDATE OF
  tmux_socket_path, tmux_session_name, tmux_session_id, tmux_pane_id,
  generation, identity_marker, pane_pid, process_group_id,
  process_start_ticks, expected_executable, boot_epoch, native_session_id,
  status, exit_code, exit_signal, exited_at, removed_at, resume_attempts,
  resume_next_at, resume_series_started_at, resume_probation_until,
  stop_requested_at, identity_capture_lost_at
ON sessions
FOR EACH ROW
WHEN NEW.controller_revision = OLD.controller_revision
BEGIN
  UPDATE sessions
  SET controller_revision = OLD.controller_revision + 1
  WHERE id = OLD.id;
END;

CREATE TABLE session_recovery_attempts (
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE RESTRICT,
  nonce      TEXT NOT NULL,
  kind       TEXT NOT NULL CONSTRAINT chk_session_recovery_kind
               CHECK (kind IN ('automatic', 'manual')),

  source_status          TEXT NOT NULL CONSTRAINT chk_session_recovery_source_status
                           CHECK (source_status IN ('exited', 'interrupted')),
  source_generation      INTEGER NOT NULL
                           CONSTRAINT chk_session_recovery_source_generation
                           CHECK (source_generation >= 1),
  source_identity_marker TEXT,
  source_resume_attempts INTEGER NOT NULL
                           CONSTRAINT chk_session_recovery_source_attempts
                           CHECK (source_resume_attempts >= 0),
  source_updated_at      TEXT NOT NULL,
  source_controller_revision INTEGER NOT NULL
                               CONSTRAINT chk_session_recovery_source_revision
                               CHECK (source_controller_revision >= 0),

  candidate_generation          INTEGER NOT NULL
                                  CONSTRAINT chk_session_recovery_candidate_generation
                                  CHECK (candidate_generation > source_generation),
  candidate_identity_marker     TEXT NOT NULL,
  candidate_tmux_socket_path    TEXT NOT NULL,
  candidate_tmux_session_name   TEXT NOT NULL,
  candidate_tmux_session_id     TEXT,
  candidate_tmux_pane_id        TEXT,
  candidate_pane_pid            INTEGER,
  candidate_process_group_id    INTEGER,
  candidate_process_start_ticks INTEGER,
  candidate_expected_executable TEXT NOT NULL,
  candidate_boot_epoch          TEXT NOT NULL,
  candidate_native_session_id   TEXT,
  candidate_identity_capture_lost_at TEXT,

  -- A cleanup token is claimed while the action remains executing. Commit
  -- requires NULL, so cleanup and publication cannot both win.
  cleanup_token TEXT,

  state TEXT NOT NULL CONSTRAINT chk_session_recovery_state
          CHECK (state IN ('starting', 'executing', 'committed', 'exited', 'failed', 'uncertain')),
  exit_code   INTEGER,
  exit_signal TEXT,
  exited_at   TEXT,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,

  PRIMARY KEY (session_id, nonce),
  CONSTRAINT chk_session_recovery_exit_fields
  CHECK (
    (state = 'exited'
      AND exited_at IS NOT NULL
      AND (exit_code IS NOT NULL OR exit_signal IS NOT NULL)
      AND (exit_code IS NULL OR exit_signal IS NULL))
    OR
    (state <> 'exited'
      AND exited_at IS NULL
      AND exit_code IS NULL
      AND exit_signal IS NULL)
  ),
  CONSTRAINT chk_session_recovery_cleanup
  CHECK (
    cleanup_token IS NULL
    OR (length(cleanup_token) > 0
      AND state IN ('executing', 'exited', 'failed', 'uncertain'))
  ),
  CONSTRAINT chk_session_recovery_candidate_tuple
  CHECK (
    (candidate_tmux_session_id IS NULL
      AND candidate_tmux_pane_id IS NULL
      AND candidate_pane_pid IS NULL
      AND candidate_process_group_id IS NULL
      AND candidate_process_start_ticks IS NULL)
    OR
    (candidate_tmux_session_id IS NOT NULL
      AND length(candidate_tmux_session_id) > 0
      AND candidate_tmux_pane_id IS NOT NULL
      AND length(candidate_tmux_pane_id) > 0
      AND candidate_pane_pid > 1
      AND candidate_process_group_id > 1
      AND candidate_process_start_ticks > 0)
  )
) STRICT;

CREATE UNIQUE INDEX idx_session_recovery_one_starting
  ON session_recovery_attempts (session_id)
  WHERE state IN ('starting', 'executing');

CREATE UNIQUE INDEX idx_session_recovery_one_automatic_generation
  ON session_recovery_attempts (session_id, source_generation)
  WHERE kind = 'automatic';

CREATE UNIQUE INDEX idx_session_recovery_candidate_generation
  ON session_recovery_attempts (session_id, candidate_generation);

CREATE INDEX idx_session_recovery_state
  ON session_recovery_attempts (state, created_at);

-- Removal uses the same durable-before-effects rule. The session row is
-- retired atomically with this claim, so no recovery candidate can publish
-- after a remover has been authorized. A crash leaves enough state for boot
-- to retry only an exact current-controller stop.
CREATE TABLE session_removal_attempts (
  session_id          TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE RESTRICT,
  nonce               TEXT NOT NULL UNIQUE,
  controller_verified INTEGER NOT NULL
                        CONSTRAINT chk_session_removal_verified
                        CHECK (controller_verified IN (0, 1)),
  state               TEXT NOT NULL CONSTRAINT chk_session_removal_state
                        CHECK (state IN ('starting', 'executing', 'completed')),
  executor_token      TEXT,
  process_stop_completed INTEGER NOT NULL DEFAULT 0
                           CONSTRAINT chk_session_removal_process_stop
                           CHECK (process_stop_completed IN (0, 1)),
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL,
  CONSTRAINT chk_session_removal_executor
    CHECK (
      (controller_verified = 0 AND state = 'completed'
        AND executor_token IS NULL AND process_stop_completed = 0)
      OR
      (controller_verified = 1 AND (
        (state = 'starting' AND executor_token IS NULL
          AND process_stop_completed = 0)
        OR
        (state IN ('executing', 'completed')
          AND executor_token IS NOT NULL AND length(executor_token) > 0)
      ))
    )
) STRICT;

CREATE INDEX idx_session_removal_state
  ON session_removal_attempts (state, created_at);

CREATE TABLE session_stop_attempts (
  session_id                 TEXT NOT NULL REFERENCES sessions(id) ON DELETE RESTRICT,
  nonce                      TEXT NOT NULL,
  source_generation          INTEGER NOT NULL,
  source_identity_marker     TEXT NOT NULL,
  source_controller_revision INTEGER NOT NULL,
  force                      INTEGER NOT NULL CONSTRAINT chk_session_stop_force
                               CHECK (force IN (0, 1)),
  state                      TEXT NOT NULL CONSTRAINT chk_session_stop_state
                               CHECK (state IN ('claimed', 'executing', 'completed')),
  created_at                 TEXT NOT NULL,
  updated_at                 TEXT NOT NULL,
  PRIMARY KEY (session_id, nonce)
) STRICT;

CREATE UNIQUE INDEX idx_session_stop_one_open
  ON session_stop_attempts (session_id)
  WHERE state IN ('claimed', 'executing');

CREATE INDEX idx_session_stop_state
  ON session_stop_attempts (state, created_at);
