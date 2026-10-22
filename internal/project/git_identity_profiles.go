package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/noisefloor-oss/deck/internal/audit"
	"github.com/noisefloor-oss/deck/internal/db"
)

const (
	AuditGitIdentityCreated        = string(audit.GitIdentityCreated)
	AuditGitIdentityUpdated        = string(audit.GitIdentityUpdated)
	AuditGitIdentityDeleted        = string(audit.GitIdentityDeleted)
	AuditGitIdentityDefaultChanged = string(audit.GitIdentityDefaultChanged)
	AuditProjectGitIdentityChanged = string(audit.ProjectGitIdentityChanged)
)

var (
	ErrGitIdentityProfileNotFound = errors.New("git identity profile not found")
	ErrGitIdentityDuplicate       = errors.New("git identity profile already exists")
	ErrGitIdentityRevision        = errors.New("git identity revision conflict")
	ErrGitIdentityReplacement     = errors.New("replacement default Git identity is required")
	ErrGitIdentityRequest         = errors.New("invalid Git identity request")
)

// GitIdentityProfile is one reusable, non-secret author identity.
type GitIdentityProfile struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GitIdentityCatalog is the complete active selection state.
type GitIdentityCatalog struct {
	Profiles         []*GitIdentityProfile `json:"profiles"`
	DefaultProfileID *string               `json:"default_profile_id"`
	Revision         int64                 `json:"revision"`
}

// GitIdentityRevisionConflict carries the current revision for a stale write.
type GitIdentityRevisionConflict struct {
	Resource string `json:"resource"`
	Current  int64  `json:"current_revision"`
}

func (e *GitIdentityRevisionConflict) Error() string {
	return fmt.Sprintf("%s: %s is at revision %d", ErrGitIdentityRevision, e.Resource, e.Current)
}

func (e *GitIdentityRevisionConflict) Unwrap() error { return ErrGitIdentityRevision }

// ListGitIdentityProfiles returns the active profiles and default selection.
func (r *Repository) ListGitIdentityProfiles(ctx context.Context) (*GitIdentityCatalog, error) {
	var catalog GitIdentityCatalog
	if err := r.db.Reader().QueryRowContext(ctx, `
		SELECT default_profile_id, revision
		FROM git_identity_catalog WHERE singleton = 1`,
	).Scan(&catalog.DefaultProfileID, &catalog.Revision); err != nil {
		return nil, fmt.Errorf("read Git identity catalog: %w", err)
	}
	rows, err := r.db.Reader().QueryContext(ctx, `
		SELECT id, name, email, revision, created_at, updated_at
		FROM git_identity_profiles
		WHERE deleted_at IS NULL
		ORDER BY CASE WHEN id = ? THEN 0 ELSE 1 END, name, email, id`, catalog.DefaultProfileID)
	if err != nil {
		return nil, fmt.Errorf("list Git identity profiles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		profile, err := scanGitIdentityProfile(rows)
		if err != nil {
			return nil, err
		}
		catalog.Profiles = append(catalog.Profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Git identity profiles: %w", err)
	}
	if catalog.Profiles == nil {
		catalog.Profiles = []*GitIdentityProfile{}
	}
	return &catalog, nil
}

// GetGitIdentityProfile returns one active profile.
func (r *Repository) GetGitIdentityProfile(ctx context.Context, id string) (*GitIdentityProfile, error) {
	profile, err := scanGitIdentityProfile(r.db.Reader().QueryRowContext(ctx, `
		SELECT id, name, email, revision, created_at, updated_at
		FROM git_identity_profiles WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGitIdentityProfileNotFound
	}
	return profile, err
}

func gitIdentityProfileAtRevisionTx(
	ctx context.Context,
	tx *sql.Tx,
	id string,
	expectedRevision int64,
) (*GitIdentityProfile, error) {
	profile, err := scanGitIdentityProfile(tx.QueryRowContext(ctx, `
		SELECT id, name, email, revision, created_at, updated_at
		FROM git_identity_profiles WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGitIdentityProfileNotFound
	}
	if err != nil {
		return nil, err
	}
	if profile.Revision != expectedRevision {
		return nil, &GitIdentityRevisionConflict{Resource: "profile", Current: profile.Revision}
	}
	return profile, nil
}

// CreateGitIdentityProfile adds a reusable profile. The first profile becomes
// the default in the same transaction.
func (r *Repository) CreateGitIdentityProfile(ctx context.Context, requested GitIdentity) (*GitIdentityProfile, error) {
	identity, err := validateGitIdentity(requested)
	if err != nil {
		return nil, err
	}
	id, err := r.newID()
	if err != nil {
		return nil, err
	}
	now := r.now()
	profile := &GitIdentityProfile{ID: id, Name: identity.Name, Email: identity.Email, Revision: 1, CreatedAt: now, UpdatedAt: now}
	err = r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var existing string
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM git_identity_profiles
			WHERE name = ? AND email = ? AND deleted_at IS NULL`, identity.Name, identity.Email).Scan(&existing)
		switch {
		case err == nil:
			return fmt.Errorf("%w: %s", ErrGitIdentityDuplicate, existing)
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("check Git identity duplicate: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO git_identity_profiles
			  (id, name, email, revision, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?)`, id, identity.Name, identity.Email, db.FormatTime(now), db.FormatTime(now)); err != nil {
			return fmt.Errorf("create Git identity profile: %w", err)
		}
		var defaultID *string
		var catalogRevision int64
		if err := tx.QueryRowContext(ctx, `
			SELECT default_profile_id, revision FROM git_identity_catalog WHERE singleton = 1`,
		).Scan(&defaultID, &catalogRevision); err != nil {
			return fmt.Errorf("read Git identity catalog: %w", err)
		}
		if defaultID == nil {
			if _, err := tx.ExecContext(ctx, `
				UPDATE git_identity_catalog
				SET default_profile_id = ?, revision = revision + 1, updated_at = ?
				WHERE singleton = 1`, id, db.FormatTime(now)); err != nil {
				return fmt.Errorf("select initial Git identity default: %w", err)
			}
			if err := appendIdentityAudit(ctx, tx, AuditGitIdentityDefaultChanged, "profile="+id); err != nil {
				return err
			}
		}
		return appendIdentityAudit(ctx, tx, AuditGitIdentityCreated, "profile="+id)
	})
	if err != nil {
		return nil, err
	}
	return profile, nil
}

// UpdateGitIdentityProfile replaces one active profile at an exact revision.
func (r *Repository) UpdateGitIdentityProfile(ctx context.Context, id string, expectedRevision int64, requested GitIdentity) (*GitIdentityProfile, error) {
	identity, err := validateGitIdentity(requested)
	if err != nil {
		return nil, err
	}
	now := r.now()
	var next *GitIdentityProfile
	err = r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		current, err := scanGitIdentityProfile(tx.QueryRowContext(ctx, `
			SELECT id, name, email, revision, created_at, updated_at
			FROM git_identity_profiles WHERE id = ? AND deleted_at IS NULL`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrGitIdentityProfileNotFound
		}
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return &GitIdentityRevisionConflict{Resource: "profile", Current: current.Revision}
		}
		var duplicate string
		err = tx.QueryRowContext(ctx, `
			SELECT id FROM git_identity_profiles
			WHERE name = ? AND email = ? AND deleted_at IS NULL AND id <> ?`, identity.Name, identity.Email, id).Scan(&duplicate)
		switch {
		case err == nil:
			return fmt.Errorf("%w: %s", ErrGitIdentityDuplicate, duplicate)
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("check Git identity duplicate: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE git_identity_profiles
			SET name = ?, email = ?, revision = revision + 1, updated_at = ?
			WHERE id = ? AND revision = ? AND deleted_at IS NULL`,
			identity.Name, identity.Email, db.FormatTime(now), id, expectedRevision)
		if err != nil {
			return fmt.Errorf("update Git identity profile: %w", err)
		}
		if err := requireOneRow(result, "update Git identity profile"); err != nil {
			return err
		}
		next = &GitIdentityProfile{ID: id, Name: identity.Name, Email: identity.Email, Revision: current.Revision + 1, CreatedAt: current.CreatedAt, UpdatedAt: now}
		return appendIdentityAudit(ctx, tx, AuditGitIdentityUpdated, fmt.Sprintf("profile=%s revision=%d", id, next.Revision))
	})
	return next, err
}

// SetDefaultGitIdentityProfile selects a default at exact catalog and profile
// revisions, so a stale settings tab cannot silently win.
func (r *Repository) SetDefaultGitIdentityProfile(ctx context.Context, id string, expectedCatalogRevision, expectedProfileRevision int64) (*GitIdentityCatalog, error) {
	now := r.now()
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var profileRevision int64
		if err := tx.QueryRowContext(ctx, `
			SELECT revision FROM git_identity_profiles WHERE id = ? AND deleted_at IS NULL`, id,
		).Scan(&profileRevision); errors.Is(err, sql.ErrNoRows) {
			return ErrGitIdentityProfileNotFound
		} else if err != nil {
			return fmt.Errorf("read Git identity profile: %w", err)
		}
		if profileRevision != expectedProfileRevision {
			return &GitIdentityRevisionConflict{Resource: "profile", Current: profileRevision}
		}
		var currentDefault *string
		var catalogRevision int64
		if err := tx.QueryRowContext(ctx, `
			SELECT default_profile_id, revision FROM git_identity_catalog WHERE singleton = 1`,
		).Scan(&currentDefault, &catalogRevision); err != nil {
			return fmt.Errorf("read Git identity catalog: %w", err)
		}
		if catalogRevision != expectedCatalogRevision {
			return &GitIdentityRevisionConflict{Resource: "catalog", Current: catalogRevision}
		}
		if currentDefault != nil && *currentDefault == id {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE git_identity_catalog
			SET default_profile_id = ?, revision = revision + 1, updated_at = ?
			WHERE singleton = 1 AND revision = ?`, id, db.FormatTime(now), expectedCatalogRevision); err != nil {
			return fmt.Errorf("set default Git identity: %w", err)
		}
		return appendIdentityAudit(ctx, tx, AuditGitIdentityDefaultChanged, "profile="+id)
	})
	if err != nil {
		return nil, err
	}
	return r.ListGitIdentityProfiles(ctx)
}

// DeleteGitIdentityProfile soft-deletes one profile. Deleting the current
// default changes the default selection in the same transaction.
func (r *Repository) DeleteGitIdentityProfile(ctx context.Context, id string, expectedProfileRevision, expectedCatalogRevision int64, replacementDefaultID string, expectedReplacementRevision int64) error {
	now := r.now()
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var profileRevision int64
		if err := tx.QueryRowContext(ctx, `
			SELECT revision FROM git_identity_profiles WHERE id = ? AND deleted_at IS NULL`, id,
		).Scan(&profileRevision); errors.Is(err, sql.ErrNoRows) {
			return ErrGitIdentityProfileNotFound
		} else if err != nil {
			return fmt.Errorf("read Git identity profile: %w", err)
		}
		if profileRevision != expectedProfileRevision {
			return &GitIdentityRevisionConflict{Resource: "profile", Current: profileRevision}
		}
		var currentDefault *string
		var catalogRevision int64
		if err := tx.QueryRowContext(ctx, `
			SELECT default_profile_id, revision FROM git_identity_catalog WHERE singleton = 1`,
		).Scan(&currentDefault, &catalogRevision); err != nil {
			return fmt.Errorf("read Git identity catalog: %w", err)
		}
		isDefault := currentDefault != nil && *currentDefault == id
		if !isDefault && replacementDefaultID != "" {
			return fmt.Errorf("%w: replacement default is only valid when deleting the default Git identity", ErrGitIdentityRequest)
		}
		if isDefault {
			if catalogRevision != expectedCatalogRevision {
				return &GitIdentityRevisionConflict{Resource: "catalog", Current: catalogRevision}
			}
			var others int
			if err := tx.QueryRowContext(ctx, `
				SELECT count(*) FROM git_identity_profiles WHERE deleted_at IS NULL AND id <> ?`, id,
			).Scan(&others); err != nil {
				return fmt.Errorf("count replacement Git identities: %w", err)
			}
			if others > 0 && replacementDefaultID == "" {
				return ErrGitIdentityReplacement
			}
			if replacementDefaultID != "" {
				var replacementRevision int64
				if err := tx.QueryRowContext(ctx, `
					SELECT revision FROM git_identity_profiles
					WHERE id = ? AND id <> ? AND deleted_at IS NULL`, replacementDefaultID, id,
				).Scan(&replacementRevision); errors.Is(err, sql.ErrNoRows) {
					return ErrGitIdentityReplacement
				} else if err != nil {
					return fmt.Errorf("read replacement Git identity: %w", err)
				}
				if replacementRevision != expectedReplacementRevision {
					return &GitIdentityRevisionConflict{Resource: "replacement_profile", Current: replacementRevision}
				}
			}
			var replacement any
			if replacementDefaultID != "" {
				replacement = replacementDefaultID
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE git_identity_catalog
				SET default_profile_id = ?, revision = revision + 1, updated_at = ?
				WHERE singleton = 1 AND revision = ?`, replacement, db.FormatTime(now), expectedCatalogRevision); err != nil {
				return fmt.Errorf("replace default Git identity: %w", err)
			}
			if err := appendIdentityAudit(ctx, tx, AuditGitIdentityDefaultChanged, "profile="+replacementDefaultID); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE git_identity_profiles
			SET deleted_at = ?, revision = revision + 1, updated_at = ?
			WHERE id = ? AND revision = ? AND deleted_at IS NULL`,
			db.FormatTime(now), db.FormatTime(now), id, expectedProfileRevision)
		if err != nil {
			return fmt.Errorf("delete Git identity profile: %w", err)
		}
		if err := requireOneRow(result, "delete Git identity profile"); err != nil {
			return err
		}
		return appendIdentityAudit(ctx, tx, AuditGitIdentityDeleted, "profile="+id)
	})
}

func scanGitIdentityProfile(row interface{ Scan(...any) error }) (*GitIdentityProfile, error) {
	var profile GitIdentityProfile
	var createdAt, updatedAt string
	if err := row.Scan(&profile.ID, &profile.Name, &profile.Email, &profile.Revision, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	var err error
	if profile.CreatedAt, err = time.Parse(db.TimeFormat, createdAt); err != nil {
		return nil, fmt.Errorf("parse Git identity created_at: %w", err)
	}
	if profile.UpdatedAt, err = time.Parse(db.TimeFormat, updatedAt); err != nil {
		return nil, fmt.Errorf("parse Git identity updated_at: %w", err)
	}
	return &profile, nil
}

func appendIdentityAudit(ctx context.Context, tx *sql.Tx, eventType, details string) error {
	return db.AppendAudit(ctx, tx, db.AuditEvent{Type: eventType, Details: details})
}

func requireOneRow(result sql.Result, action string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", action, err)
	}
	if rows != 1 {
		return fmt.Errorf("%s affected %d rows, want 1", action, rows)
	}
	return nil
}

func (r *Repository) ensureGitIdentityProfileTx(ctx context.Context, tx *sql.Tx, identity GitIdentity, now time.Time) (string, int64, error) {
	var id string
	var revision int64
	err := tx.QueryRowContext(ctx, `
		SELECT id, revision FROM git_identity_profiles
		WHERE name = ? AND email = ? AND deleted_at IS NULL`, identity.Name, identity.Email,
	).Scan(&id, &revision)
	if err == nil {
		return id, revision, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", 0, fmt.Errorf("find Git identity profile: %w", err)
	}
	id, err = r.newID()
	if err != nil {
		return "", 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO git_identity_profiles
		  (id, name, email, revision, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?)`, id, identity.Name, identity.Email, db.FormatTime(now), db.FormatTime(now)); err != nil {
		return "", 0, fmt.Errorf("create Git identity profile: %w", err)
	}
	var defaultID *string
	if err := tx.QueryRowContext(ctx, `
		SELECT default_profile_id FROM git_identity_catalog WHERE singleton = 1`,
	).Scan(&defaultID); err != nil {
		return "", 0, fmt.Errorf("read Git identity default: %w", err)
	}
	if defaultID == nil {
		if _, err := tx.ExecContext(ctx, `
			UPDATE git_identity_catalog
			SET default_profile_id = ?, revision = revision + 1, updated_at = ?
			WHERE singleton = 1`, id, db.FormatTime(now)); err != nil {
			return "", 0, fmt.Errorf("select initial Git identity default: %w", err)
		}
		if err := appendIdentityAudit(ctx, tx, AuditGitIdentityDefaultChanged, "profile="+id); err != nil {
			return "", 0, err
		}
	}
	if err := appendIdentityAudit(ctx, tx, AuditGitIdentityCreated, "profile="+id); err != nil {
		return "", 0, err
	}
	return id, 1, nil
}

func (r *Repository) recordProjectGitIdentityTx(ctx context.Context, tx *sql.Tx, projectID string, identity GitIdentity, now time.Time) error {
	profileID, profileRevision, err := r.ensureGitIdentityProfileTx(ctx, tx, identity, now)
	if err != nil {
		return err
	}
	var currentRevision int64
	var currentProfileID *string
	var currentProfileRevision *int64
	var currentName, currentEmail string
	if err := tx.QueryRowContext(ctx, `
		SELECT git_identity_setup_revision, git_identity_profile_id,
		       git_identity_profile_revision, git_author_name, git_author_email
		FROM projects WHERE id = ? AND removed_at IS NULL`, projectID,
	).Scan(&currentRevision, &currentProfileID, &currentProfileRevision, &currentName, &currentEmail); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("read project Git identity revision: %w", err)
	}
	if currentProfileID != nil && currentProfileRevision != nil &&
		*currentProfileID == profileID && *currentProfileRevision == profileRevision &&
		currentName == identity.Name && currentEmail == identity.Email {
		return nil
	}
	nextRevision := currentRevision + 1
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO project_git_identity_snapshots
		  (project_id, revision, profile_id, profile_revision, name, email, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, projectID, nextRevision, profileID, profileRevision,
		identity.Name, identity.Email, db.FormatTime(now)); err != nil {
		return fmt.Errorf("record project Git identity snapshot: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET git_author_name = ?, git_author_email = ?, git_identity_profile_id = ?,
		    git_identity_profile_revision = ?, git_identity_setup_revision = ?
		WHERE id = ? AND removed_at IS NULL AND git_identity_setup_revision = ?`,
		identity.Name, identity.Email, profileID, profileRevision, nextRevision, projectID, currentRevision)
	if err != nil {
		return fmt.Errorf("select project Git identity snapshot: %w", err)
	}
	if err := requireOneRow(result, "select project Git identity snapshot"); err != nil {
		return err
	}
	return appendIdentityAudit(ctx, tx, AuditProjectGitIdentityChanged,
		fmt.Sprintf("project=%s profile=%s revision=%d", projectID, profileID, nextRevision))
}

func (r *Repository) recordSelectedProjectGitIdentityTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	profile *GitIdentityProfile,
	expectedSetupRevision int64,
	now time.Time,
) error {
	var currentRevision int64
	var currentProfileID *string
	var currentProfileRevision *int64
	var currentName, currentEmail string
	if err := tx.QueryRowContext(ctx, `
		SELECT git_identity_setup_revision, git_identity_profile_id,
		       git_identity_profile_revision, git_author_name, git_author_email
		FROM projects WHERE id = ? AND removed_at IS NULL`, projectID,
	).Scan(&currentRevision, &currentProfileID, &currentProfileRevision,
		&currentName, &currentEmail); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("read project Git identity revision: %w", err)
	}
	if currentRevision != expectedSetupRevision {
		return &GitIdentityRevisionConflict{Resource: "project", Current: currentRevision}
	}
	if currentProfileID != nil && currentProfileRevision != nil &&
		*currentProfileID == profile.ID && *currentProfileRevision == profile.Revision &&
		currentName == profile.Name && currentEmail == profile.Email {
		return nil
	}
	nextRevision := currentRevision + 1
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO project_git_identity_snapshots
		  (project_id, revision, profile_id, profile_revision, name, email, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, projectID, nextRevision, profile.ID, profile.Revision,
		profile.Name, profile.Email, db.FormatTime(now)); err != nil {
		return fmt.Errorf("record project Git identity snapshot: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET git_author_name = ?, git_author_email = ?, git_identity_profile_id = ?,
		    git_identity_profile_revision = ?, git_identity_setup_revision = ?
		WHERE id = ? AND removed_at IS NULL AND git_identity_setup_revision = ?`,
		profile.Name, profile.Email, profile.ID, profile.Revision, nextRevision,
		projectID, expectedSetupRevision)
	if err != nil {
		return fmt.Errorf("select project Git identity snapshot: %w", err)
	}
	if err := requireOneRow(result, "select project Git identity snapshot"); err != nil {
		return err
	}
	return appendIdentityAudit(ctx, tx, AuditProjectGitIdentityChanged,
		fmt.Sprintf("project=%s profile=%s revision=%d", projectID, profile.ID, nextRevision))
}
