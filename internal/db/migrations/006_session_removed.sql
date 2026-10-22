-- 006_session_removed: operator-initiated session removal (the sidebar's
-- remove affordance). Soft-delete like projects: the row stays for the
-- audit trail but leaves every active list.
ALTER TABLE sessions ADD COLUMN removed_at TEXT;

