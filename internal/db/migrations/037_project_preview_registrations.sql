-- Ports remain reserved after disabling a preview or removing its project:
-- Deck does not stop the dev server, so reuse could expose another app.
CREATE TABLE project_preview_registrations (
  project_id TEXT PRIMARY KEY REFERENCES projects(id),
  origin TEXT NOT NULL UNIQUE,
  port INTEGER NOT NULL UNIQUE CHECK (port BETWEEN 1024 AND 65535),
  enabled INTEGER NOT NULL CHECK (enabled IN (0, 1))
) STRICT;
