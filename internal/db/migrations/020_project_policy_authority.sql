-- 020_project_policy_authority: inert persistence for confined-project
-- security-domain identity, immutable policy revisions, supervisor-attested
-- backing, and operator approval/revocation (FR-P6).
--
-- This migration adds the protected tables only. It creates no memberships,
-- so every existing project stays explicitly unconfined. There is no typed
-- model, no repository/control API, no overlap or resource resolution, no
-- restore-quarantine workflow, and no admission or runtime behavior here: a
-- future slice reconciles recorded authority against live state. canonical_json
-- columns carry opaque canonical object bytes; this layer checks only that they
-- are valid JSON objects, never their meaning.
--
-- Identifiers are lowercase 32-character hex (128 bits). Digests are lowercase
-- 64-character hex (SHA-256). The GLOB class [0-9a-f] rejects uppercase and
-- every non-hex byte, and the length check fixes the width, so the two together
-- are exact and leave no room for a mixed-case or padded value to slip through.

-- A security domain is the stable identity of one confined project, independent
-- of its display path (FR-P6). Only an authenticated operator control surface
-- may create or import a domain; the creator is recorded for attribution only
-- and never confers authority by itself.
CREATE TABLE project_security_domains (
  security_domain_id TEXT PRIMARY KEY
    CONSTRAINT chk_psd_id CHECK (
      length(security_domain_id) = 32
      AND security_domain_id NOT GLOB '*[^0-9a-f]*'
    ),
  display_label TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  created_by    TEXT NOT NULL,

  -- Closed one-way invalidation tuple. The consistency CHECK admits exactly two
  -- shapes: every column NULL (active) or every column set (invalidated). It
  -- does NOT force a row to be inserted active: an invalidation tuple may be
  -- present on insert only to retain or import already-inert historical
  -- authority, so a row can be recorded-as-inert at birth. Runtime creation
  -- controls (which operator surface may create an active row, which
  -- transitions may set the tuple) are a later slice; this layer stores what it
  -- is given and then freezes it via the immutability triggers below. The
  -- consistency CHECK uses IS NULL / IS NOT NULL rather than equality because
  -- comparing to NULL with '=' is always NULL, which a hostile or buggy writer
  -- could use to record a partial tuple that looks invalidated in one column
  -- and active in another.
  invalidated_reason   TEXT
    CONSTRAINT chk_psd_reason CHECK (
      invalidated_reason IS NULL
      OR invalidated_reason IN ('revoked', 'restore', 'backing_mismatch')
    ),
  invalidated_evidence TEXT
    CONSTRAINT chk_psd_evidence CHECK (
      invalidated_evidence IS NULL
      OR (length(invalidated_evidence) = 64
          AND invalidated_evidence NOT GLOB '*[^0-9a-f]*')
    ),
  invalidated_at TEXT,
  invalidated_by TEXT,

  CONSTRAINT chk_psd_invalidation_tuple CHECK (
    (invalidated_reason IS NULL
     AND invalidated_evidence IS NULL
     AND invalidated_at IS NULL
     AND invalidated_by IS NULL)
    OR
    (invalidated_reason IS NOT NULL
     AND invalidated_evidence IS NOT NULL
     AND invalidated_at IS NOT NULL
     AND invalidated_by IS NOT NULL)
  )
) STRICT;

-- A membership is what makes a registered project confined: it binds one
-- project to one security domain. None are created here, so every project
-- remains unconfined until an authenticated operator attests one. Removing a
-- project is refused while an active membership references it (ON DELETE
-- RESTRICT), and the row itself is never deletable.
CREATE TABLE project_domain_memberships (
  membership_id       TEXT PRIMARY KEY
    CONSTRAINT chk_pdm_id CHECK (
      length(membership_id) = 32
      AND membership_id NOT GLOB '*[^0-9a-f]*'
    ),
  project_id          TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  security_domain_id  TEXT NOT NULL REFERENCES project_security_domains(security_domain_id) ON DELETE RESTRICT,
  created_at          TEXT NOT NULL,
  created_by          TEXT NOT NULL,

  invalidated_reason   TEXT
    CONSTRAINT chk_pdm_reason CHECK (
      invalidated_reason IS NULL
      OR invalidated_reason IN ('revoked', 'restore', 'backing_mismatch')
    ),
  invalidated_evidence TEXT
    CONSTRAINT chk_pdm_evidence CHECK (
      invalidated_evidence IS NULL
      OR (length(invalidated_evidence) = 64
          AND invalidated_evidence NOT GLOB '*[^0-9a-f]*')
    ),
  invalidated_at TEXT,
  invalidated_by TEXT,

  CONSTRAINT chk_pdm_invalidation_tuple CHECK (
    (invalidated_reason IS NULL
     AND invalidated_evidence IS NULL
     AND invalidated_at IS NULL
     AND invalidated_by IS NULL)
    OR
    (invalidated_reason IS NOT NULL
     AND invalidated_evidence IS NOT NULL
     AND invalidated_at IS NOT NULL
     AND invalidated_by IS NOT NULL)
  )
) STRICT;

-- A policy revision is the immutable canonical content of one domain policy:
-- the digest plus the opaque canonical object bytes that produced it. It is
-- versioned per domain. A policy edit creates a NEW revision and never mutates
-- an old one; revoking old authority is recorded on the attestation/approval
-- that governs the revision, not by editing history. A restore can mark a
-- revision inert until it is re-attested, which is why revisions also carry the
-- invalidation tuple.
CREATE TABLE project_policy_revisions (
  revision_id         TEXT PRIMARY KEY
    CONSTRAINT chk_ppr_id CHECK (
      length(revision_id) = 32
      AND revision_id NOT GLOB '*[^0-9a-f]*'
    ),
  security_domain_id  TEXT NOT NULL REFERENCES project_security_domains(security_domain_id) ON DELETE RESTRICT,
  revision_no         INTEGER NOT NULL
    CONSTRAINT chk_ppr_revision_no CHECK (revision_no >= 1),
  policy_digest       TEXT NOT NULL
    CONSTRAINT chk_ppr_policy_digest CHECK (
      length(policy_digest) = 64
      AND policy_digest NOT GLOB '*[^0-9a-f]*'
    ),
  canonical_json      TEXT NOT NULL
    CONSTRAINT chk_ppr_canonical_json CHECK (
      json_valid(canonical_json) = 1
      AND json_type(canonical_json) = 'object'
    ),
  created_at          TEXT NOT NULL,
  created_by          TEXT NOT NULL,

  invalidated_reason   TEXT
    CONSTRAINT chk_ppr_reason CHECK (
      invalidated_reason IS NULL
      OR invalidated_reason IN ('revoked', 'restore', 'backing_mismatch')
    ),
  invalidated_evidence TEXT
    CONSTRAINT chk_ppr_evidence CHECK (
      invalidated_evidence IS NULL
      OR (length(invalidated_evidence) = 64
          AND invalidated_evidence NOT GLOB '*[^0-9a-f]*')
    ),
  invalidated_at TEXT,
  invalidated_by TEXT,

  CONSTRAINT chk_ppr_invalidation_tuple CHECK (
    (invalidated_reason IS NULL
     AND invalidated_evidence IS NULL
     AND invalidated_at IS NULL
     AND invalidated_by IS NULL)
    OR
    (invalidated_reason IS NOT NULL
     AND invalidated_evidence IS NOT NULL
     AND invalidated_at IS NOT NULL
     AND invalidated_by IS NOT NULL)
  ),

  -- Unique domain revision: (domain, revision_no) is taken once and stays taken
  -- even after the revision is invalidated, so a re-attestation allocates a new
  -- number rather than recycling an inert one.
  CONSTRAINT uq_ppr_domain_revision UNIQUE (security_domain_id, revision_no)
) STRICT;

-- A backing attestation records the supervisor-attested managed backing
-- identity for one revision. At most one active attestation may exist per
-- revision. The backing_digest is the attested identity; canonical_json carries
-- the opaque attestation object.
CREATE TABLE project_backing_attestations (
  attestation_id      TEXT PRIMARY KEY
    CONSTRAINT chk_pba_id CHECK (
      length(attestation_id) = 32
      AND attestation_id NOT GLOB '*[^0-9a-f]*'
    ),
  revision_id         TEXT NOT NULL REFERENCES project_policy_revisions(revision_id) ON DELETE RESTRICT,
  security_domain_id  TEXT NOT NULL REFERENCES project_security_domains(security_domain_id) ON DELETE RESTRICT,
  backing_digest      TEXT NOT NULL
    CONSTRAINT chk_pba_backing_digest CHECK (
      length(backing_digest) = 64
      AND backing_digest NOT GLOB '*[^0-9a-f]*'
    ),
  canonical_json      TEXT NOT NULL
    CONSTRAINT chk_pba_canonical_json CHECK (
      json_valid(canonical_json) = 1
      AND json_type(canonical_json) = 'object'
    ),
  attested_at         TEXT NOT NULL,
  attested_by         TEXT NOT NULL,

  invalidated_reason   TEXT
    CONSTRAINT chk_pba_reason CHECK (
      invalidated_reason IS NULL
      OR invalidated_reason IN ('revoked', 'restore', 'backing_mismatch')
    ),
  invalidated_evidence TEXT
    CONSTRAINT chk_pba_evidence CHECK (
      invalidated_evidence IS NULL
      OR (length(invalidated_evidence) = 64
          AND invalidated_evidence NOT GLOB '*[^0-9a-f]*')
    ),
  invalidated_at TEXT,
  invalidated_by TEXT,

  CONSTRAINT chk_pba_invalidation_tuple CHECK (
    (invalidated_reason IS NULL
     AND invalidated_evidence IS NULL
     AND invalidated_at IS NULL
     AND invalidated_by IS NULL)
    OR
    (invalidated_reason IS NOT NULL
     AND invalidated_evidence IS NOT NULL
     AND invalidated_at IS NOT NULL
     AND invalidated_by IS NOT NULL)
  )
) STRICT;

-- An approval records an authenticated operator approving one revision through
-- one attestation. An active approval must bind an active attestation for the
-- same policy; the binding is verified at insert time and the invalidation
-- order trigger below keeps an attestation active while any approval bound to
-- it is still active.
CREATE TABLE project_policy_approvals (
  approval_id         TEXT PRIMARY KEY
    CONSTRAINT chk_ppa_id CHECK (
      length(approval_id) = 32
      AND approval_id NOT GLOB '*[^0-9a-f]*'
    ),
  attestation_id      TEXT NOT NULL REFERENCES project_backing_attestations(attestation_id) ON DELETE RESTRICT,
  revision_id         TEXT NOT NULL REFERENCES project_policy_revisions(revision_id) ON DELETE RESTRICT,
  security_domain_id  TEXT NOT NULL REFERENCES project_security_domains(security_domain_id) ON DELETE RESTRICT,
  approved_at         TEXT NOT NULL,
  approved_by         TEXT NOT NULL,

  invalidated_reason   TEXT
    CONSTRAINT chk_ppa_reason CHECK (
      invalidated_reason IS NULL
      OR invalidated_reason IN ('revoked', 'restore', 'backing_mismatch')
    ),
  invalidated_evidence TEXT
    CONSTRAINT chk_ppa_evidence CHECK (
      invalidated_evidence IS NULL
      OR (length(invalidated_evidence) = 64
          AND invalidated_evidence NOT GLOB '*[^0-9a-f]*')
    ),
  invalidated_at TEXT,
  invalidated_by TEXT,

  CONSTRAINT chk_ppa_invalidation_tuple CHECK (
    (invalidated_reason IS NULL
     AND invalidated_evidence IS NULL
     AND invalidated_at IS NULL
     AND invalidated_by IS NULL)
    OR
    (invalidated_reason IS NOT NULL
     AND invalidated_evidence IS NOT NULL
     AND invalidated_at IS NOT NULL
     AND invalidated_by IS NOT NULL)
  )
) STRICT;

-- At most one active attestation per revision.
CREATE UNIQUE INDEX idx_pba_active_per_revision
  ON project_backing_attestations (revision_id) WHERE invalidated_reason IS NULL;

-- At most one active approval per attestation.
CREATE UNIQUE INDEX idx_ppa_active_per_attestation
  ON project_policy_approvals (attestation_id) WHERE invalidated_reason IS NULL;

-- A project belongs to at most one active security domain. Removing the active
-- membership (by invalidation) frees the project to join another.
CREATE UNIQUE INDEX idx_pdm_active_per_project
  ON project_domain_memberships (project_id) WHERE invalidated_reason IS NULL;

-- Restore and backing-mismatch evidence is single-use within each table: the
-- same proof must not justify two invalidations. Plain revocation is excluded,
-- so a revoked row's evidence may legitimately repeat (FR-P6 permits reuse of
-- ordinary revocation). Indexed only where the reason is one of the two
-- non-reusable kinds, so revoked rows never collide.
CREATE UNIQUE INDEX idx_psd_evidence_unique
  ON project_security_domains (invalidated_evidence)
  WHERE invalidated_reason IN ('restore', 'backing_mismatch');
CREATE UNIQUE INDEX idx_pdm_evidence_unique
  ON project_domain_memberships (invalidated_evidence)
  WHERE invalidated_reason IN ('restore', 'backing_mismatch');
CREATE UNIQUE INDEX idx_ppr_evidence_unique
  ON project_policy_revisions (invalidated_evidence)
  WHERE invalidated_reason IN ('restore', 'backing_mismatch');
CREATE UNIQUE INDEX idx_pba_evidence_unique
  ON project_backing_attestations (invalidated_evidence)
  WHERE invalidated_reason IN ('restore', 'backing_mismatch');
CREATE UNIQUE INDEX idx_ppa_evidence_unique
  ON project_policy_approvals (invalidated_evidence)
  WHERE invalidated_reason IN ('restore', 'backing_mismatch');

-- A backing attestation binds one revision within its own security domain: its
-- revision_id and security_domain_id must name the same row in
-- project_policy_revisions. The two foreign keys only prove each id exists on
-- its own; without this trigger an attestation could pin a revision belonging
-- to a different domain, smuggling a foreign policy under a trusted domain.
-- The EXISTS lookup is empty — and so aborts — both when the revision is
-- absent and when its domain differs, which is fail-closed. Comparisons use IS
-- rather than '=' so a NULL operand cannot make the match evaluate to unknown
-- and silently pass.
CREATE TRIGGER trg_pba_binds_revision_domain
BEFORE INSERT ON project_backing_attestations
BEGIN
  SELECT RAISE(ABORT, 'attestation revision and security domain must name one policy revision')
  WHERE NOT EXISTS (
    SELECT 1
    FROM project_policy_revisions r
    WHERE r.revision_id        IS NEW.revision_id
      AND r.security_domain_id IS NEW.security_domain_id
  );
END;

-- An active approval must bind an active attestation for the same policy. The
-- EXISTS lookup checks the bound attestation is active and that revision and
-- domain match, so an approval cannot attach to a foreign or already-inert
-- attestation. Comparisons use IS rather than '=' so a NULL operand on either
-- side cannot make the match evaluate to unknown and silently pass.
CREATE TRIGGER trg_ppa_binds_active_attestation
BEFORE INSERT ON project_policy_approvals
BEGIN
  SELECT RAISE(ABORT, 'active approval must bind an active attestation for the same policy')
  WHERE NOT EXISTS (
    SELECT 1
    FROM project_backing_attestations b
    WHERE b.attestation_id     IS NEW.attestation_id
      AND b.revision_id        IS NEW.revision_id
      AND b.security_domain_id IS NEW.security_domain_id
      AND b.invalidated_reason IS NULL
  );
END;

-- Invalidation order: an attestation may not be invalidated while any approval
-- bound to it is still active. The approval must be invalidated first. This is
-- checked on the attestation's invalidation transition so the ordering cannot
-- be bypassed by updating the attestation directly.
CREATE TRIGGER trg_pba_invalidation_order
BEFORE UPDATE ON project_backing_attestations
BEGIN
  SELECT RAISE(ABORT, 'invalidate the approval before its attestation')
  WHERE NEW.invalidated_reason IS NOT NULL
    AND OLD.invalidated_reason IS NULL
    AND EXISTS (
      SELECT 1
      FROM project_policy_approvals a
      WHERE a.attestation_id     IS NEW.attestation_id
        AND a.invalidated_reason IS NULL
    );
END;

-- Immutability: the only UPDATE any of these rows accepts is the one-way
-- invalidation transition (all four invalidation columns go from NULL to set,
-- every other column unchanged). Each non-invalidation column is compared with
-- IS rather than '=': a plain equality against a NULL new value is NULL, which
-- would let a writer null out a column and bypass the immutability guarantee.
-- The transition itself is one-way because OLD invalidation columns must all be
-- NULL, so a second invalidation or any re-edit of an invalidated row is
-- rejected. INSERT OR REPLACE is resisted below by the no-delete triggers: a
-- replace resolves a primary-key conflict by deleting the existing row first,
-- which fires the BEFORE DELETE trigger and aborts the statement.

CREATE TRIGGER trg_psd_immutable BEFORE UPDATE ON project_security_domains
BEGIN
  SELECT RAISE(ABORT, 'project_security_domains is immutable except a one-way invalidation')
  WHERE NOT (
        OLD.security_domain_id   IS NEW.security_domain_id
    AND OLD.display_label        IS NEW.display_label
    AND OLD.created_at           IS NEW.created_at
    AND OLD.created_by           IS NEW.created_by
    AND OLD.invalidated_reason   IS NULL
    AND OLD.invalidated_evidence IS NULL
    AND OLD.invalidated_at       IS NULL
    AND OLD.invalidated_by       IS NULL
    AND NEW.invalidated_reason   IS NOT NULL
    AND NEW.invalidated_evidence IS NOT NULL
    AND NEW.invalidated_at       IS NOT NULL
    AND NEW.invalidated_by       IS NOT NULL
  );
END;

CREATE TRIGGER trg_pdm_immutable BEFORE UPDATE ON project_domain_memberships
BEGIN
  SELECT RAISE(ABORT, 'project_domain_memberships is immutable except a one-way invalidation')
  WHERE NOT (
        OLD.membership_id        IS NEW.membership_id
    AND OLD.project_id           IS NEW.project_id
    AND OLD.security_domain_id   IS NEW.security_domain_id
    AND OLD.created_at           IS NEW.created_at
    AND OLD.created_by           IS NEW.created_by
    AND OLD.invalidated_reason   IS NULL
    AND OLD.invalidated_evidence IS NULL
    AND OLD.invalidated_at       IS NULL
    AND OLD.invalidated_by       IS NULL
    AND NEW.invalidated_reason   IS NOT NULL
    AND NEW.invalidated_evidence IS NOT NULL
    AND NEW.invalidated_at       IS NOT NULL
    AND NEW.invalidated_by       IS NOT NULL
  );
END;

CREATE TRIGGER trg_ppr_immutable BEFORE UPDATE ON project_policy_revisions
BEGIN
  SELECT RAISE(ABORT, 'project_policy_revisions is immutable except a one-way invalidation')
  WHERE NOT (
        OLD.revision_id           IS NEW.revision_id
    AND OLD.security_domain_id    IS NEW.security_domain_id
    AND OLD.revision_no           IS NEW.revision_no
    AND OLD.policy_digest         IS NEW.policy_digest
    AND OLD.canonical_json        IS NEW.canonical_json
    AND OLD.created_at            IS NEW.created_at
    AND OLD.created_by            IS NEW.created_by
    AND OLD.invalidated_reason    IS NULL
    AND OLD.invalidated_evidence  IS NULL
    AND OLD.invalidated_at        IS NULL
    AND OLD.invalidated_by        IS NULL
    AND NEW.invalidated_reason    IS NOT NULL
    AND NEW.invalidated_evidence  IS NOT NULL
    AND NEW.invalidated_at        IS NOT NULL
    AND NEW.invalidated_by        IS NOT NULL
  );
END;

CREATE TRIGGER trg_pba_immutable BEFORE UPDATE ON project_backing_attestations
BEGIN
  SELECT RAISE(ABORT, 'project_backing_attestations is immutable except a one-way invalidation')
  WHERE NOT (
        OLD.attestation_id        IS NEW.attestation_id
    AND OLD.revision_id           IS NEW.revision_id
    AND OLD.security_domain_id    IS NEW.security_domain_id
    AND OLD.backing_digest        IS NEW.backing_digest
    AND OLD.canonical_json        IS NEW.canonical_json
    AND OLD.attested_at           IS NEW.attested_at
    AND OLD.attested_by           IS NEW.attested_by
    AND OLD.invalidated_reason    IS NULL
    AND OLD.invalidated_evidence  IS NULL
    AND OLD.invalidated_at        IS NULL
    AND OLD.invalidated_by        IS NULL
    AND NEW.invalidated_reason    IS NOT NULL
    AND NEW.invalidated_evidence  IS NOT NULL
    AND NEW.invalidated_at        IS NOT NULL
    AND NEW.invalidated_by        IS NOT NULL
  );
END;

CREATE TRIGGER trg_ppa_immutable BEFORE UPDATE ON project_policy_approvals
BEGIN
  SELECT RAISE(ABORT, 'project_policy_approvals is immutable except a one-way invalidation')
  WHERE NOT (
        OLD.approval_id           IS NEW.approval_id
    AND OLD.attestation_id        IS NEW.attestation_id
    AND OLD.revision_id           IS NEW.revision_id
    AND OLD.security_domain_id    IS NEW.security_domain_id
    AND OLD.approved_at           IS NEW.approved_at
    AND OLD.approved_by           IS NEW.approved_by
    AND OLD.invalidated_reason    IS NULL
    AND OLD.invalidated_evidence  IS NULL
    AND OLD.invalidated_at        IS NULL
    AND OLD.invalidated_by        IS NULL
    AND NEW.invalidated_reason    IS NOT NULL
    AND NEW.invalidated_evidence  IS NOT NULL
    AND NEW.invalidated_at        IS NOT NULL
    AND NEW.invalidated_by        IS NOT NULL
  );
END;

-- These rows are never deletable. The trigger also defeats INSERT OR REPLACE on
-- a conflicting primary key: that statement deletes the existing row before
-- inserting the replacement, and this trigger aborts on the delete. With
-- recursive_triggers left at its default OFF the trigger chain fired by that
-- conflict-resolution delete is not guaranteed to run in full, so Open() sets
-- _recursive_triggers=ON and reads it back; the immutability guarantee depends
-- on these triggers firing deterministically.

CREATE TRIGGER trg_psd_nodelete BEFORE DELETE ON project_security_domains
BEGIN
  SELECT RAISE(ABORT, 'project_security_domains rows are never deletable') WHERE 1;
END;
CREATE TRIGGER trg_pdm_nodelete BEFORE DELETE ON project_domain_memberships
BEGIN
  SELECT RAISE(ABORT, 'project_domain_memberships rows are never deletable') WHERE 1;
END;
CREATE TRIGGER trg_ppr_nodelete BEFORE DELETE ON project_policy_revisions
BEGIN
  SELECT RAISE(ABORT, 'project_policy_revisions rows are never deletable') WHERE 1;
END;
CREATE TRIGGER trg_pba_nodelete BEFORE DELETE ON project_backing_attestations
BEGIN
  SELECT RAISE(ABORT, 'project_backing_attestations rows are never deletable') WHERE 1;
END;
CREATE TRIGGER trg_ppa_nodelete BEFORE DELETE ON project_policy_approvals
BEGIN
  SELECT RAISE(ABORT, 'project_policy_approvals rows are never deletable') WHERE 1;
END;
