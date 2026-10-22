-- 024_git_identity_profiles: reusable non-secret Git author profiles and
-- immutable project setup snapshots (FR-P10, NFR-REL15).
--
-- The migration is deliberately database-only. Existing repository-local Git
-- configuration is neither read nor written during upgrade.

CREATE TABLE git_identity_profiles (
  id         TEXT PRIMARY KEY
             CONSTRAINT chk_git_identity_profile_id CHECK (
               length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'
             ),
  name       TEXT NOT NULL CONSTRAINT chk_git_identity_profile_name CHECK (
               length(trim(name)) > 0 AND name = trim(name)
               AND instr(name, char(10)) = 0 AND instr(name, char(13)) = 0
             ),
  email      TEXT NOT NULL CONSTRAINT chk_git_identity_profile_email CHECK (
               length(trim(email)) > 0 AND email = trim(email)
               AND instr(email, char(10)) = 0 AND instr(email, char(13)) = 0
             ),
  revision   INTEGER NOT NULL CONSTRAINT chk_git_identity_profile_revision CHECK (revision >= 1),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
) STRICT;

CREATE UNIQUE INDEX idx_git_identity_profiles_active_pair
  ON git_identity_profiles (name, email) WHERE deleted_at IS NULL;

CREATE TABLE git_identity_catalog (
  singleton          INTEGER PRIMARY KEY CONSTRAINT chk_git_identity_catalog_singleton CHECK (singleton = 1),
  default_profile_id TEXT REFERENCES git_identity_profiles(id) ON DELETE RESTRICT,
  revision           INTEGER NOT NULL CONSTRAINT chk_git_identity_catalog_revision CHECK (revision >= 0),
  updated_at         TEXT NOT NULL
) STRICT;

INSERT INTO git_identity_catalog (singleton, default_profile_id, revision, updated_at)
VALUES (1, NULL, 0, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

CREATE TABLE project_git_identity_snapshots (
  project_id       TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  revision         INTEGER NOT NULL CONSTRAINT chk_project_git_identity_snapshot_revision CHECK (revision >= 1),
  profile_id       TEXT NOT NULL REFERENCES git_identity_profiles(id) ON DELETE RESTRICT,
  profile_revision INTEGER NOT NULL CONSTRAINT chk_project_git_identity_profile_revision CHECK (profile_revision >= 1),
  name             TEXT NOT NULL,
  email            TEXT NOT NULL,
  created_at       TEXT NOT NULL,
  PRIMARY KEY (project_id, revision)
) STRICT;

ALTER TABLE projects ADD COLUMN git_identity_profile_id TEXT
  REFERENCES git_identity_profiles(id) ON DELETE RESTRICT;
ALTER TABLE projects ADD COLUMN git_identity_profile_revision INTEGER
  CONSTRAINT chk_projects_git_identity_profile_revision CHECK (
    git_identity_profile_revision IS NULL OR git_identity_profile_revision >= 1
  );
ALTER TABLE projects ADD COLUMN git_identity_setup_revision INTEGER NOT NULL DEFAULT 0
  CONSTRAINT chk_projects_git_identity_setup_revision CHECK (git_identity_setup_revision >= 0);

-- Backfill one revision-1 profile per distinct complete legacy pair. SQLite's
-- randomblob supplies the same 128-bit opaque ID shape as the application.
INSERT INTO git_identity_profiles (id, name, email, revision, created_at, updated_at)
SELECT lower(hex(randomblob(16))), git_author_name, git_author_email, 1,
       min(created_at), max(last_used_at)
FROM projects
WHERE git_author_name <> '' AND git_author_email <> ''
GROUP BY git_author_name, git_author_email;

INSERT INTO project_git_identity_snapshots (
  project_id, revision, profile_id, profile_revision, name, email, created_at
)
SELECT p.id, 1, i.id, 1, p.git_author_name, p.git_author_email, p.created_at
FROM projects p
JOIN git_identity_profiles i
  ON i.name = p.git_author_name AND i.email = p.git_author_email
WHERE p.git_author_name <> '' AND p.git_author_email <> '';

UPDATE projects
SET git_identity_profile_id = (
      SELECT i.id FROM git_identity_profiles i
      WHERE i.name = projects.git_author_name
        AND i.email = projects.git_author_email
        AND i.deleted_at IS NULL
    ),
    git_identity_profile_revision = 1,
    git_identity_setup_revision = 1
WHERE git_author_name <> '' AND git_author_email <> '';

-- Prefer the identity of the most recently used active project. If every
-- matching project is removed, the most recently used historical project is a
-- deterministic fallback so a non-empty catalog still has exactly one default.
UPDATE git_identity_catalog
SET default_profile_id = (
      SELECT i.id
      FROM projects p
      JOIN git_identity_profiles i
        ON i.name = p.git_author_name AND i.email = p.git_author_email
      ORDER BY (p.removed_at IS NULL) DESC, p.last_used_at DESC, i.id ASC
      LIMIT 1
    ),
    revision = CASE WHEN EXISTS (SELECT 1 FROM git_identity_profiles) THEN 1 ELSE 0 END,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

CREATE TRIGGER trg_git_identity_catalog_no_delete
BEFORE DELETE ON git_identity_catalog
BEGIN
  SELECT RAISE(ABORT, 'git identity catalog cannot be deleted');
END;

CREATE TRIGGER trg_git_identity_catalog_active_default
BEFORE UPDATE OF default_profile_id ON git_identity_catalog
WHEN NEW.default_profile_id IS NOT NULL
 AND NOT EXISTS (
   SELECT 1 FROM git_identity_profiles
   WHERE id = NEW.default_profile_id AND deleted_at IS NULL
 )
BEGIN
  SELECT RAISE(ABORT, 'default Git identity must be active');
END;

CREATE TRIGGER trg_git_identity_profile_default_not_deleted
BEFORE UPDATE OF deleted_at ON git_identity_profiles
WHEN OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL
 AND EXISTS (
   SELECT 1 FROM git_identity_catalog WHERE default_profile_id = OLD.id
 )
BEGIN
  SELECT RAISE(ABORT, 'default Git identity must be replaced before deletion');
END;

CREATE TRIGGER trg_project_git_identity_snapshots_no_update
BEFORE UPDATE ON project_git_identity_snapshots
BEGIN
  SELECT RAISE(ABORT, 'project Git identity snapshots are immutable');
END;

CREATE TRIGGER trg_project_git_identity_snapshots_no_delete
BEFORE DELETE ON project_git_identity_snapshots
BEGIN
  SELECT RAISE(ABORT, 'project Git identity snapshots cannot be deleted');
END;
