-- One row per task lane Deck opened through the Tasks engine. The fencing
-- tokens are the engine's write capabilities for the claim and the run
-- attempt; they live only in this table and never leave the server.
CREATE TABLE tutorial_lanes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL REFERENCES projects(id),
  tag TEXT NOT NULL,
  task_id INTEGER NOT NULL CHECK (task_id > 0),
  phase TEXT NOT NULL CHECK (phase IN ('claimed', 'running', 'bound', 'open', 'removed', 'failed')),
  failed_step TEXT NOT NULL DEFAULT '',
  agent TEXT NOT NULL DEFAULT '',
  prior_status TEXT NOT NULL DEFAULT '',
  status_done INTEGER NOT NULL DEFAULT 0 CHECK (status_done IN (0, 1)),
  pending_op TEXT NOT NULL DEFAULT '' CHECK (pending_op IN ('', 'claim', 'renew', 'finish')),
  pending_request_id TEXT NOT NULL DEFAULT '',
  pending_revision INTEGER NOT NULL DEFAULT 0,
  claim_token INTEGER NOT NULL DEFAULT 0,
  run_id INTEGER NOT NULL DEFAULT 0,
  attempt INTEGER NOT NULL DEFAULT 0,
  attempt_token INTEGER NOT NULL DEFAULT 0,
  branch TEXT NOT NULL DEFAULT '',
  worktree_path TEXT NOT NULL DEFAULT '',
  base_commit TEXT NOT NULL DEFAULT '',
  lane_project_id TEXT REFERENCES projects(id),
  review_agent TEXT NOT NULL DEFAULT '',
  review_requested_at TEXT NOT NULL DEFAULT '',
  renewed_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE (project_id, tag, task_id)
) STRICT;
