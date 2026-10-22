-- A successor slot records the process its spawn actually produced.
--
-- Recovery replaces the occupant of the session's own pane, so a candidate now
-- lives where an operator can see and reach it. Pane options and session
-- environment -- the marker and generation a candidate is recognized by --
-- survive an out-of-band `tmux respawn-pane`, which means an operator who
-- reclaims that pane inherits the candidate's whole identity. Without a
-- process-scoped member the convergence sweep would adopt their process into
-- the row, and every later identity check would then defend the wrong process
-- all the way to `kill(-pgid)`.
--
-- These columns are the process-scoped members: pid, its group, and the start
-- ticks that make a reused pid distinguishable. They are nullable because they
-- are written after the spawn is observed; a slot still carrying NULL has no
-- proven occupant and authorizes nothing.
ALTER TABLE session_successor_slots ADD COLUMN candidate_pane_pid INTEGER
  CONSTRAINT chk_successor_candidate_pid
  CHECK (candidate_pane_pid IS NULL OR candidate_pane_pid > 1);

ALTER TABLE session_successor_slots ADD COLUMN candidate_process_group_id INTEGER
  CONSTRAINT chk_successor_candidate_pgid
  CHECK (candidate_process_group_id IS NULL OR candidate_process_group_id > 1);

ALTER TABLE session_successor_slots ADD COLUMN candidate_start_ticks INTEGER
  CONSTRAINT chk_successor_candidate_ticks
  CHECK (candidate_start_ticks IS NULL OR candidate_start_ticks > 0);
