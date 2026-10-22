package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/noisefloor-oss/deck/internal/audit"
	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/projectpolicy"
)

// Policy-authority audit events. A protected authority transition is
// auditable in the same transaction that writes it.
const (
	AuditSecurityDomainCreated  = string(audit.ProjectSecurityDomainCreated)
	AuditPolicyRevisionAppended = string(audit.ProjectPolicyRevisionAppended)
)

var (
	// ErrProjectAlreadyConfined rejects a second active domain assignment.
	ErrProjectAlreadyConfined = errors.New("project already has a security domain")
	// ErrProjectNotConfined reports that no active membership exists.
	ErrProjectNotConfined = errors.New("project has no security domain")
	// ErrPolicyAuthorityInput rejects caller-supplied authority: a chosen
	// security-domain ID or a missing creator identity. Domain, membership,
	// and revision identifiers are generated here, never accepted.
	ErrPolicyAuthorityInput = errors.New("policy authority input is repository-owned")
	// ErrPolicyAuthorityConflict rejects canonical path overlap or a
	// duplicate retained backing, writable remote, or writable memory scope.
	// Deliberate shared-backing registration is a separate,
	// currently disabled admission path, so every overlap fails closed.
	ErrPolicyAuthorityConflict = errors.New("policy authority conflicts with another domain")
	// ErrPolicyBackingChanged rejects a registered path whose current
	// resolution drifted from the stored canonical path.
	ErrPolicyBackingChanged = errors.New("registered project backing changed")
	// ErrPolicyRevisionNotFound reports an unknown revision inside a domain
	// the project has ever belonged to.
	ErrPolicyRevisionNotFound = errors.New("project policy revision not found")
	// ErrPolicyRevisionCorrupt fences malformed, noncanonical, or
	// mis-bound stored authority. Stored bytes are re-verified on every
	// authority-bearing read; a row that fails never becomes authority.
	ErrPolicyRevisionCorrupt = errors.New("project policy revision failed verification")
	// ErrPolicyRevisionUnchanged rejects appending a digest the domain
	// already retains: an edit that changes nothing is not a new revision.
	ErrPolicyRevisionUnchanged = errors.New("project policy revision already exists")
	// ErrPolicyRevisionConflict rejects a stale expected-current revision
	// or an append onto an inert current revision.
	ErrPolicyRevisionConflict = errors.New("project policy revision conflict")
)

// PolicyInvalidation is the one-way invalidation tuple a restored or otherwise
// inert revision carries. A non-nil value fences the revision's authority; the
// retained bytes stay verifiable so runs already bound to the revision can
// reconcile, but nothing here reactivates them.
type PolicyInvalidation struct {
	Reason   string
	Evidence string
	At       time.Time
	By       string
}

// StoredPolicyRevision is one verified immutable policy record. Callers never
// construct authority from raw database fields: every read path re-verifies
// the canonical bytes, digest, and domain binding first.
type StoredPolicyRevision struct {
	ProjectID     string
	DomainID      string
	RevisionID    string
	Revision      int64
	Digest        string
	CanonicalJSON string
	CreatedAt     time.Time
	Policy        projectpolicy.ProjectPolicyV1
	// Invalidated is nil for an active revision.
	Invalidated *PolicyInvalidation
}

// CreatePolicyDomain gives one active project a server-generated security
// domain, its membership, and revision 1, all in one transaction. The policy
// argument is an already-resolved policy value except for its repository-owned
// security-domain binding. That field must be empty because caller-controlled
// input cannot choose domain identity. The bound canonical policy is derived
// and verified before anything is persisted.
func (r *Repository) CreatePolicyDomain(
	ctx context.Context, projectID string, policy projectpolicy.ProjectPolicyV1, createdBy string,
) (*StoredPolicyRevision, error) {
	if policy.SecurityDomainID != "" {
		return nil, fmt.Errorf("%w: security domain id", ErrPolicyAuthorityInput)
	}
	if strings.TrimSpace(createdBy) == "" {
		return nil, fmt.Errorf("%w: creator identity is required", ErrPolicyAuthorityInput)
	}
	domainID, err := r.newID()
	if err != nil {
		return nil, err
	}
	policy.SecurityDomainID = domainID
	revision, err := projectpolicy.CanonicalizeProjectPolicy(policy)
	if err != nil {
		return nil, err
	}
	createdAt := r.now()

	var stored *StoredPolicyRevision
	err = r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		projectPath, name, err := activeProjectPath(ctx, tx, projectID)
		if err != nil {
			return err
		}
		var existingDomain string
		err = tx.QueryRowContext(ctx,
			`SELECT security_domain_id FROM project_domain_memberships
			  WHERE project_id = ? AND invalidated_reason IS NULL`,
			projectID).Scan(&existingDomain)
		if err == nil {
			return fmt.Errorf("%w: domain %s", ErrProjectAlreadyConfined, existingDomain)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read project security domain: %w", err)
		}
		if err := rejectPolicyAuthorityConflicts(
			ctx, tx, projectID, domainID, projectPath, revision.Policy); err != nil {
			return err
		}
		membershipID, err := r.newID()
		if err != nil {
			return err
		}
		revisionID, err := r.newID()
		if err != nil {
			return err
		}
		stamped := db.FormatTime(createdAt)
		// display_label is presentation metadata derived from the registered
		// project name at creation; the domain row is frozen after this, so
		// the label never tracks later project renames and never selects
		// authority: identity is independent of display.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO project_security_domains
			     (security_domain_id, display_label, created_at, created_by)
			 VALUES (?, ?, ?, ?)`,
			domainID, name, stamped, createdBy); err != nil {
			return fmt.Errorf("insert project security domain: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO project_domain_memberships
			     (membership_id, project_id, security_domain_id, created_at, created_by)
			 VALUES (?, ?, ?, ?, ?)`,
			membershipID, projectID, domainID, stamped, createdBy); err != nil {
			return fmt.Errorf("insert project domain membership: %w", err)
		}
		if err := insertPolicyRevision(ctx, tx, revisionID, domainID, 1, revision, stamped, createdBy); err != nil {
			return err
		}
		stored = &StoredPolicyRevision{
			ProjectID:     projectID,
			DomainID:      domainID,
			RevisionID:    revisionID,
			Revision:      1,
			Digest:        revision.Digest,
			CanonicalJSON: revision.CanonicalJSON,
			CreatedAt:     createdAt,
			Policy:        revision.Policy,
		}
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			Type: AuditSecurityDomainCreated,
			Details: fmt.Sprintf(
				"project_id=%s domain_id=%s membership_id=%s revision_id=%s revision_no=1 digest=%s",
				projectID, domainID, membershipID, revisionID, revision.Digest),
		})
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

// AppendPolicyRevision creates the next revision for the project's active
// domain under an explicit expected-current compare-and-swap. The caller
// supplies the exact identity and digest of the revision it believes is
// current; within one immediate write transaction the latest retained
// revision is re-verified and must match before the next ordinal is chosen,
// so a stale serialized append conflicts instead of forking history. An old
// revision is never invalidated or rewritten here: an edit is a new row.
func (r *Repository) AppendPolicyRevision(
	ctx context.Context,
	projectID string,
	policy projectpolicy.ProjectPolicyV1,
	expectedRevisionID, expectedDigest, createdBy string,
) (*StoredPolicyRevision, error) {
	if policy.SecurityDomainID != "" {
		return nil, fmt.Errorf("%w: security domain id", ErrPolicyAuthorityInput)
	}
	if strings.TrimSpace(createdBy) == "" {
		return nil, fmt.Errorf("%w: creator identity is required", ErrPolicyAuthorityInput)
	}
	createdAt := r.now()

	var stored *StoredPolicyRevision
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		projectPath, _, err := activeProjectPath(ctx, tx, projectID)
		if err != nil {
			return err
		}
		var domainID string
		if err := tx.QueryRowContext(ctx,
			`SELECT m.security_domain_id
			  FROM project_domain_memberships AS m
			  JOIN project_security_domains AS d ON d.security_domain_id = m.security_domain_id
			  WHERE m.project_id = ?
			    AND m.invalidated_reason IS NULL
			    AND d.invalidated_reason IS NULL`,
			projectID).Scan(&domainID); errors.Is(err, sql.ErrNoRows) {
			return ErrProjectNotConfined
		} else if err != nil {
			return fmt.Errorf("read project security domain: %w", err)
		}

		var latestRevisionID string
		var latestNo int64
		var latestDigest, latestJSON string
		var latestInvalidated sql.NullString
		err = tx.QueryRowContext(ctx, `
			SELECT revision_id, revision_no, policy_digest, canonical_json, invalidated_reason
			FROM project_policy_revisions
			WHERE security_domain_id = ?
			ORDER BY revision_no DESC
			LIMIT 1`, domainID).Scan(
			&latestRevisionID, &latestNo, &latestDigest, &latestJSON, &latestInvalidated)
		if errors.Is(err, sql.ErrNoRows) {
			// Creation always writes revision 1 with the domain, so a domain
			// without revisions is a broken invariant, not an append target.
			return fmt.Errorf("%w: domain %s has no revision", ErrPolicyRevisionCorrupt, domainID)
		}
		if err != nil {
			return fmt.Errorf("read latest policy revision: %w", err)
		}
		if _, err := verifyStoredRevision(domainID, latestDigest, latestJSON); err != nil {
			return fmt.Errorf("%w: domain %s revision %s: %w",
				ErrPolicyRevisionCorrupt, domainID, latestRevisionID, err)
		}
		if latestInvalidated.Valid {
			// A restored revision is inert until re-attested; it is never the
			// current authority a new revision builds on.
			return fmt.Errorf("%w: revision %s is inert",
				ErrPolicyRevisionConflict, latestRevisionID)
		}
		if latestRevisionID != expectedRevisionID || latestDigest != expectedDigest {
			return fmt.Errorf("%w: expected revision %s, current is %s",
				ErrPolicyRevisionConflict, expectedRevisionID, latestRevisionID)
		}
		if latestNo == math.MaxInt64 {
			return fmt.Errorf("choose next project policy revision: revision overflow")
		}

		policy.SecurityDomainID = domainID
		revision, err := projectpolicy.CanonicalizeProjectPolicy(policy)
		if err != nil {
			return err
		}
		var duplicate int
		err = tx.QueryRowContext(ctx,
			`SELECT 1 FROM project_policy_revisions
			  WHERE security_domain_id = ? AND policy_digest = ?`,
			domainID, revision.Digest).Scan(&duplicate)
		if err == nil {
			return fmt.Errorf("%w: digest %s", ErrPolicyRevisionUnchanged, revision.Digest)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check existing policy revision: %w", err)
		}
		if err := rejectPolicyAuthorityConflicts(
			ctx, tx, projectID, domainID, projectPath, revision.Policy); err != nil {
			return err
		}
		revisionID, err := r.newID()
		if err != nil {
			return err
		}
		nextRevision := latestNo + 1
		if err := insertPolicyRevision(
			ctx, tx, revisionID, domainID, nextRevision, revision,
			db.FormatTime(createdAt), createdBy); err != nil {
			return err
		}
		stored = &StoredPolicyRevision{
			ProjectID:     projectID,
			DomainID:      domainID,
			RevisionID:    revisionID,
			Revision:      nextRevision,
			Digest:        revision.Digest,
			CanonicalJSON: revision.CanonicalJSON,
			CreatedAt:     createdAt,
			Policy:        revision.Policy,
		}
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			Type: AuditPolicyRevisionAppended,
			Details: fmt.Sprintf(
				"project_id=%s domain_id=%s revision_id=%s revision_no=%d digest=%s",
				projectID, domainID, revisionID, nextRevision, revision.Digest),
		})
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

// GetPolicyRevision returns one exact revision scoped to a project and
// revision identity. The project must have a membership — active or not — in
// the revision's domain: history stays resolvable after a membership is
// invalidated, but a revision of an unrelated domain is not found. Canonical
// bytes, digest, and the policy's domain binding are re-verified on every
// read; a row that fails verification is corrupt, never authority.
func (r *Repository) GetPolicyRevision(
	ctx context.Context, projectID, revisionID string,
) (*StoredPolicyRevision, error) {
	row := r.db.Reader().QueryRowContext(ctx, `
		SELECT r.security_domain_id, r.revision_no, r.policy_digest,
		       r.canonical_json, r.created_at,
		       r.invalidated_reason, r.invalidated_evidence,
		       r.invalidated_at, r.invalidated_by
		FROM project_policy_revisions AS r
		WHERE r.revision_id = ?
		  AND EXISTS (
		    SELECT 1 FROM project_domain_memberships AS m
		    WHERE m.project_id = ?
		      AND m.security_domain_id IS r.security_domain_id
		  )`, revisionID, projectID)

	stored := StoredPolicyRevision{ProjectID: projectID, RevisionID: revisionID}
	var createdAt string
	var reason, evidence, invalidatedAt, by sql.NullString
	if err := row.Scan(
		&stored.DomainID, &stored.Revision, &stored.Digest,
		&stored.CanonicalJSON, &createdAt,
		&reason, &evidence, &invalidatedAt, &by,
	); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPolicyRevisionNotFound
	} else if err != nil {
		return nil, fmt.Errorf("read project policy revision: %w", err)
	}
	verified, err := verifyStoredRevision(stored.DomainID, stored.Digest, stored.CanonicalJSON)
	if err != nil {
		return nil, fmt.Errorf("%w: domain %s revision %s: %w",
			ErrPolicyRevisionCorrupt, stored.DomainID, revisionID, err)
	}
	stored.Policy = verified.Policy
	if stored.CreatedAt, err = time.Parse(db.TimeFormat, createdAt); err != nil {
		return nil, fmt.Errorf("%w: created_at: %w", ErrPolicyRevisionCorrupt, err)
	}
	if reason.Valid {
		at, err := time.Parse(db.TimeFormat, invalidatedAt.String)
		if err != nil {
			return nil, fmt.Errorf("%w: invalidated_at: %w", ErrPolicyRevisionCorrupt, err)
		}
		if !evidence.Valid || !by.Valid {
			// The schema's consistency CHECK makes a partial tuple
			// unreachable; treat any survivor as corrupt regardless.
			return nil, fmt.Errorf("%w: partial invalidation tuple", ErrPolicyRevisionCorrupt)
		}
		stored.Invalidated = &PolicyInvalidation{
			Reason:   reason.String,
			Evidence: evidence.String,
			At:       at,
			By:       by.String,
		}
	}
	return &stored, nil
}

// verifyStoredRevision re-verifies retained canonical bytes against their
// recorded digest and the domain they are stored under. It is the only path
// by which stored bytes become a trusted policy value.
func verifyStoredRevision(domainID, digest, canonicalJSON string) (*projectpolicy.ProjectPolicyRevision, error) {
	revision, err := projectpolicy.VerifyProjectPolicy(digest, canonicalJSON)
	if err != nil {
		return nil, err
	}
	if revision.Policy.SecurityDomainID != domainID {
		return nil, fmt.Errorf("%w: policy names domain %s",
			projectpolicy.ErrInvalidPolicy, revision.Policy.SecurityDomainID)
	}
	return revision, nil
}

func insertPolicyRevision(
	ctx context.Context, tx *sql.Tx,
	revisionID, domainID string, revisionNo int64,
	revision *projectpolicy.ProjectPolicyRevision, stamped, createdBy string,
) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO project_policy_revisions
		     (revision_id, security_domain_id, revision_no, policy_digest,
		      canonical_json, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		revisionID, domainID, revisionNo, revision.Digest,
		revision.CanonicalJSON, stamped, createdBy); err != nil {
		return fmt.Errorf("insert project policy revision: %w", err)
	}
	return nil
}

// activeProjectPath reads the project's registered canonical path and proves
// it still resolves to exactly those bytes. A vanished or redirected backing
// refuses the write: the confined authority must name the directory that was
// registered, not whatever currently sits at its spelling.
func activeProjectPath(ctx context.Context, tx *sql.Tx, projectID string) (path, name string, err error) {
	if err := tx.QueryRowContext(ctx,
		`SELECT canonical_path, name FROM projects WHERE id = ? AND removed_at IS NULL`,
		projectID).Scan(&path, &name); errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	} else if err != nil {
		return "", "", fmt.Errorf("read active project: %w", err)
	}
	resolved, err := Canonicalize(path)
	if err != nil {
		return "", "", fmt.Errorf("%w: project %s: %w", ErrPolicyBackingChanged, projectID, err)
	}
	if resolved != path {
		return "", "", fmt.Errorf("%w: project %s resolves to %s, registered as %s",
			ErrPolicyBackingChanged, projectID, resolved, path)
	}
	return path, name, nil
}

// rejectPolicyAuthorityConflicts applies the admission scan
// against every other still-active domain:
//
//   - canonical path overlap: nested or overlapping host roots cannot be
//     distinct confined domains, so the candidate project's registered
//     canonical path must not contain, or sit inside, the path of another
//     active membership's project — including a removed project, whose
//     retained membership still binds that directory to its domain;
//   - duplicate retained backing: the managed backing digest is already
//     bound by another active domain. Deliberate same-backing registration
//     (another view or worktree of one managed project) is a separately
//     gated admission path that stays disabled here;
//   - duplicate writable remote: a second contribute-class mapping of a
//     repository another active domain already maps writable. Read-only
//     mappings add no write authority and do not collide;
//   - duplicate writable memory scope: the exact memory scope digest is
//     already bound by another active domain.
//
// Only active authority conflicts: a revoked domain, an invalidated
// membership, or a restored revision is inert and blocks nothing. Every
// retained row consulted is re-verified first, so corrupt stored authority
// fails the write closed instead of silently narrowing the scan.
func rejectPolicyAuthorityConflicts(
	ctx context.Context, tx *sql.Tx,
	projectID, domainID, projectPath string,
	candidate projectpolicy.ProjectPolicyV1,
) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT m.security_domain_id, p.id, p.canonical_path, p.removed_at
		FROM project_domain_memberships AS m
		JOIN projects AS p ON p.id = m.project_id
		JOIN project_security_domains AS d ON d.security_domain_id = m.security_domain_id
		WHERE m.invalidated_reason IS NULL
		  AND d.invalidated_reason IS NULL
		  AND m.security_domain_id <> ?`, domainID)
	if err != nil {
		return fmt.Errorf("list confined project paths: %w", err)
	}
	for rows.Next() {
		var otherDomain, otherProject, otherPath string
		var removedAt sql.NullString
		if err := rows.Scan(&otherDomain, &otherProject, &otherPath, &removedAt); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan confined project path: %w", err)
		}
		if !removedAt.Valid {
			// A live registration must still resolve to its recorded bytes;
			// a removed project's directory may legitimately have drifted,
			// and its retained membership still binds the recorded path.
			resolved, err := Canonicalize(otherPath)
			if err != nil {
				_ = rows.Close()
				return fmt.Errorf("%w: project %s in domain %s: %w",
					ErrPolicyBackingChanged, otherProject, otherDomain, err)
			}
			if resolved != otherPath {
				_ = rows.Close()
				return fmt.Errorf("%w: project %s in domain %s resolves to %s, registered as %s",
					ErrPolicyBackingChanged, otherProject, otherDomain, resolved, otherPath)
			}
		}
		if UnderRoot(projectPath, otherPath) || UnderRoot(otherPath, projectPath) {
			_ = rows.Close()
			return fmt.Errorf("%w: project %s path overlaps project %s in domain %s",
				ErrPolicyAuthorityConflict, projectID, otherProject, otherDomain)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("list confined project paths: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close confined project paths: %w", err)
	}

	rows, err = tx.QueryContext(ctx, `
		SELECT r.security_domain_id, r.revision_id, r.policy_digest, r.canonical_json
		FROM project_policy_revisions AS r
		WHERE r.invalidated_reason IS NULL
		  AND r.security_domain_id <> ?
		  AND EXISTS (
		    SELECT 1 FROM project_domain_memberships AS m
		    WHERE m.security_domain_id IS r.security_domain_id
		      AND m.invalidated_reason IS NULL
		  )
		  AND EXISTS (
		    SELECT 1 FROM project_security_domains AS d
		    WHERE d.security_domain_id IS r.security_domain_id
		      AND d.invalidated_reason IS NULL
		  )
		ORDER BY r.security_domain_id, r.revision_no`, domainID)
	if err != nil {
		return fmt.Errorf("list retained project policy authority: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var otherDomain, revisionID, digest, canonicalJSON string
		if err := rows.Scan(&otherDomain, &revisionID, &digest, &canonicalJSON); err != nil {
			return fmt.Errorf("scan retained project policy authority: %w", err)
		}
		other, err := verifyStoredRevision(otherDomain, digest, canonicalJSON)
		if err != nil {
			return fmt.Errorf("%w: domain %s revision %s: %w",
				ErrPolicyRevisionCorrupt, otherDomain, revisionID, err)
		}
		switch {
		case other.Policy.BackingDigest == candidate.BackingDigest:
			return fmt.Errorf("%w: managed backing is already bound to domain %s",
				ErrPolicyAuthorityConflict, otherDomain)
		case other.Policy.MappedRemote.Repository == candidate.MappedRemote.Repository &&
			other.Policy.MappedRemote.PermissionClass == projectpolicy.RemotePermissionContribute &&
			candidate.MappedRemote.PermissionClass == projectpolicy.RemotePermissionContribute:
			return fmt.Errorf("%w: writable remote is already bound to domain %s",
				ErrPolicyAuthorityConflict, otherDomain)
		case other.Policy.MemoryScopeDigest == candidate.MemoryScopeDigest:
			return fmt.Errorf("%w: memory scope is already bound to domain %s",
				ErrPolicyAuthorityConflict, otherDomain)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list retained project policy authority: %w", err)
	}
	return nil
}
