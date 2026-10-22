-- 026_durable_provider_identity: restart-safe provider identity capture and
-- the minimal crash rediscovery slot for a separately fenced successor.

CREATE TABLE provider_identity_capture_grants (
  token_sha256       TEXT PRIMARY KEY
                       CONSTRAINT chk_capture_token_sha256
                       CHECK (length(token_sha256) = 64
                         AND token_sha256 NOT GLOB '*[^0-9a-f]*'),
  session_id         TEXT NOT NULL REFERENCES sessions(id) ON DELETE RESTRICT,
  provider           TEXT NOT NULL,
  generation         INTEGER NOT NULL
                       CONSTRAINT chk_capture_generation CHECK (generation >= 1),
  identity_marker    TEXT NOT NULL,
  canonical_work_dir TEXT NOT NULL,
  expected_source    TEXT NOT NULL
                       CONSTRAINT chk_capture_expected_source
                       CHECK (expected_source IN ('startup', 'resume')),
  expected_native_id_sha256 TEXT
                       CONSTRAINT chk_capture_expected_native_id_sha256
                       CHECK (expected_native_id_sha256 IS NULL
                         OR (length(expected_native_id_sha256) = 64
                           AND expected_native_id_sha256 NOT GLOB '*[^0-9a-f]*')),
  expires_at         TEXT NOT NULL,
  state              TEXT NOT NULL CONSTRAINT chk_capture_state
                       CHECK (state IN ('pending', 'consumed', 'expired', 'revoked')),
  accepted_event_sha256 TEXT
                       CONSTRAINT chk_capture_event_sha256
                       CHECK (accepted_event_sha256 IS NULL
                         OR (length(accepted_event_sha256) = 64
                           AND accepted_event_sha256 NOT GLOB '*[^0-9a-f]*')),
  closed_at          TEXT,
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  CONSTRAINT chk_capture_terminal_fields CHECK (
    (state = 'consumed' AND accepted_event_sha256 IS NOT NULL AND closed_at IS NOT NULL)
    OR (state = 'pending' AND accepted_event_sha256 IS NULL AND closed_at IS NULL)
    OR (state IN ('expired', 'revoked')
      AND accepted_event_sha256 IS NULL AND closed_at IS NOT NULL)
  ),
  CONSTRAINT chk_capture_expected_identity CHECK (
    (expected_source = 'startup' AND expected_native_id_sha256 IS NULL)
    OR (expected_source = 'resume' AND expected_native_id_sha256 IS NOT NULL)
  ),
  UNIQUE (session_id, generation, identity_marker)
) STRICT;

CREATE INDEX idx_provider_capture_due
  ON provider_identity_capture_grants (expires_at)
  WHERE state = 'pending';

-- The predecessor controller revision is the complete source CAS: migration
-- 025's trigger moves it for every control-relevant field. Candidate identity
-- is rediscovered from the deterministic tmux target and must pass the full
-- live tuple check before this slot authorizes commit or exact cleanup.
CREATE TABLE session_successor_slots (
  session_id                TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE RESTRICT,
  source_generation         INTEGER NOT NULL
                              CONSTRAINT chk_successor_source_generation
                              CHECK (source_generation >= 1),
  source_controller_revision INTEGER NOT NULL
                              CONSTRAINT chk_successor_source_revision
                              CHECK (source_controller_revision >= 0),
  candidate_generation      INTEGER NOT NULL
                              CONSTRAINT chk_successor_candidate_generation
                              CHECK (candidate_generation > source_generation),
  candidate_identity_marker TEXT NOT NULL,
  capture_token_sha256      TEXT
                              REFERENCES provider_identity_capture_grants(token_sha256)
                              ON DELETE RESTRICT,
  created_at                TEXT NOT NULL
) STRICT;

-- Upgrade-only boot convergence records this receipt after every open row in
-- released session_recovery_attempts has been resolved without weakening its
-- recorded identity fences. No new code creates an attempt in that table.
CREATE TABLE session_recovery_upgrade_receipt (
  singleton    INTEGER PRIMARY KEY CHECK (singleton = 1),
  completed_at TEXT NOT NULL
) STRICT;

-- A process applying this migration necessarily discarded every raw token
-- held by the released in-memory registry. Record that one upgrade fact once;
-- later service restarts preserve grants in the table above and do not repeat
-- it. Identity-none and assigned-identity providers are deliberately absent.
INSERT INTO audit_events (session_id, event_type, details, created_at)
SELECT id, 'session.identity_capture_lost',
       'released in-memory identity capture could not survive schema upgrade',
       strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM sessions
WHERE provider IN ('command-code', 'codex', 'kimi', 'opencode', 'omp', 'kilo')
  AND removed_at IS NULL
  AND status IN ('starting', 'running', 'needs_attention')
  AND native_session_id IS NULL
  AND identity_capture_lost_at IS NULL;

UPDATE sessions
SET identity_capture_lost_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE provider IN ('command-code', 'codex', 'kimi', 'opencode', 'omp', 'kilo')
  AND removed_at IS NULL
  AND status IN ('starting', 'running', 'needs_attention')
  AND native_session_id IS NULL
  AND identity_capture_lost_at IS NULL;
