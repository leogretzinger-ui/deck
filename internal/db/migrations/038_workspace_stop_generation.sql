-- An explicit workspace lock stops a generation until the operator resumes it.
-- Ordinary stop-attribution expiry and legacy recovery cleanup cannot rearm it.
ALTER TABLE sessions ADD COLUMN workspace_stop_generation INTEGER NOT NULL DEFAULT 0
  CHECK (workspace_stop_generation >= 0);
