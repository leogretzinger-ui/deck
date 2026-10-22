-- 010_resume_state: durable automatic-resume state (FR-R6 as amended,
-- 2026-08-05). Recovery of resumable sessions is automatic and bounded:
-- at most three attempts per recorded death, counted HERE — on the row,
-- not in memory — so a service restart mid-series continues the same
-- bounded series instead of granting a fresh budget (NFR-REL11: recovery
-- attempt state is a reported fact).
--
-- Plain column additions, so no table rebuild and no foreign_keys toggle.
--
-- resume_attempts counts attempts made for the CURRENT recorded death.
-- It resets only when a resume commits (MarkResumed) — a new death after
-- a committed resume therefore starts at zero, and a failed attempt
-- (including a respawn that dies on the spot) keeps its count.
ALTER TABLE sessions ADD COLUMN resume_attempts INTEGER NOT NULL DEFAULT 0
  CONSTRAINT chk_sessions_resume_attempts CHECK (resume_attempts >= 0);

-- The earliest instant the NEXT automatic attempt may run (backoff state,
-- bounded 5s..2min by code) and the start of the attempt series (the
-- whole series is bounded to ten minutes).
ALTER TABLE sessions ADD COLUMN resume_next_at TEXT;
ALTER TABLE sessions ADD COLUMN resume_series_started_at TEXT;

-- Stamped when the operator requests a stop (graceful or force). An exit
-- that follows an operator stop is a requested exit, not a death to
-- recover from: automatic resume must not resurrect a session the
-- operator just told to die (§8 "CLI exits normally" row). Cleared when
-- a resume commits.
ALTER TABLE sessions ADD COLUMN stop_requested_at TEXT;
