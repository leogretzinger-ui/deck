-- 019_project_git_identity: persist the non-secret Git author identity that
-- project setup owns under FR-P10. Empty values identify projects registered
-- before this migration; session launch refuses them until setup is completed.
ALTER TABLE projects ADD COLUMN git_author_name TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN git_author_email TEXT NOT NULL DEFAULT '';
