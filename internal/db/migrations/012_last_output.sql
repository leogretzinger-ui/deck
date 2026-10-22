-- 012_last_output: when a session's pty last produced bytes (FR-U3).
--
-- The sidebar shows time since last output, so the row can say "4m"
-- instead of nothing. Until now the field existed only in the browser's
-- type and was never populated, so the cell always rendered empty.
--
-- This is OBSERVED transport activity, not interpretation: the terminal
-- proxy already copies pane bytes to the browser and stamps the row as
-- it does. Nothing reads the bytes, and the value never contributes to
-- session state — the reconciler remains the only authority on status
-- (FR-A6, FR-S7).
--
-- Nullable with no default: a session that has produced nothing since
-- this migration has no honest value to report, and the UI renders
-- nothing rather than pretending the migration time was output time.
--
-- Plain column addition, so no table rebuild and no foreign_keys toggle.

ALTER TABLE sessions ADD COLUMN last_output_at TEXT;
