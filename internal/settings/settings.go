// Package settings holds console-wide user choices that the UI can
// change at runtime — as opposed to deployment configuration, which
// lives in the config file and belongs to whoever runs the container.
//
// Every setting names what it SKIPS, not what it enables, so the zero
// value is the careful one. Put replaces the whole document, so a client
// that omits a field it does not know about writes false — and false has
// to mean "keep the safety prompt".
//
// skip_permissions launches every provider CLI in its own
// permission-skipping mode. It is console-wide rather than per-session
// because a per-session variant would multiply the ways a dangerous
// launch can happen.
//
// skip_close_confirm and skip_remove_project_confirm drop the respective
// confirmations after an explicit user choice.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/noisefloor-oss/deck/internal/db"
)

// KeySkipPermissions is the stored key for the permission-skipping
// launch mode. The name says what it does to the CLI, not what the UI
// calls it: "yolo" is the user's word, not a durable contract.
const KeySkipPermissions = "skip_permissions"

// KeySkipCloseConfirm is the stored key for dropping the close-a-session
// confirmation.
const KeySkipCloseConfirm = "skip_close_confirm"

// KeySkipRemoveProjectConfirm is the stored key for dropping the
// remove-a-project confirmation.
const KeySkipRemoveProjectConfirm = "skip_remove_project_confirm"

// AuditChanged is the audit event type for a settings write.
const AuditChanged = "settings.changed"

// Settings is the full console-settings document the API exchanges.
// One struct, so a read and a write speak the same shape.
type Settings struct {
	SkipPermissions          bool `json:"skip_permissions"`
	SkipCloseConfirm         bool `json:"skip_close_confirm"`
	SkipRemoveProjectConfirm bool `json:"skip_remove_project_confirm"`
}

// Repository reads and writes the settings table.
type Repository struct {
	db  *db.DB
	now func() time.Time
}

func NewRepository(d *db.DB) *Repository {
	return &Repository{db: d, now: time.Now}
}

// Get reads the whole document. Missing rows are defaults, not errors:
// a fresh database has no settings row and must behave like every
// setting is off — permission-skipping above all.
func (r *Repository) Get(ctx context.Context) (Settings, error) {
	var s Settings
	skip, err := r.getBool(ctx, KeySkipPermissions)
	if err != nil {
		return Settings{}, err
	}
	s.SkipPermissions = skip
	skipConfirm, err := r.getBool(ctx, KeySkipCloseConfirm)
	if err != nil {
		return Settings{}, err
	}
	s.SkipCloseConfirm = skipConfirm
	skipProjectConfirm, err := r.getBool(ctx, KeySkipRemoveProjectConfirm)
	if err != nil {
		return Settings{}, err
	}
	s.SkipRemoveProjectConfirm = skipProjectConfirm
	return s, nil
}

// SkipPermissions reads just that setting. Session launch calls this on
// every create, so the user's latest choice applies to the next
// launch without a restart.
func (r *Repository) SkipPermissions(ctx context.Context) (bool, error) {
	return r.getBool(ctx, KeySkipPermissions)
}

// Set writes the whole document and audits the change. Auditing is not
// decoration here: turning permission-skipping on changes what every
// subsequent CLI may do without asking, so it belongs on the record with
// its before/after.
func (r *Repository) Set(ctx context.Context, next Settings) error {
	current, err := r.Get(ctx)
	if err != nil {
		return err
	}
	now := r.now()
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		changes := []struct {
			key       string
			was, next bool
		}{
			{KeySkipPermissions, current.SkipPermissions, next.SkipPermissions},
			{KeySkipCloseConfirm, current.SkipCloseConfirm, next.SkipCloseConfirm},
			{KeySkipRemoveProjectConfirm, current.SkipRemoveProjectConfirm, next.SkipRemoveProjectConfirm},
		}
		for _, c := range changes {
			if err := putBool(ctx, tx, c.key, c.next, now); err != nil {
				return err
			}
			if c.was == c.next {
				continue
			}
			if err := db.AppendAudit(ctx, tx, db.AuditEvent{
				Type:    AuditChanged,
				Details: fmt.Sprintf("%s=%t (was %t)", c.key, c.next, c.was),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) getBool(ctx context.Context, key string) (bool, error) {
	var value string
	err := r.db.Reader().QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read setting %s: %w", key, err)
	}
	// Only the exact stored spelling of true is true: an unreadable
	// value must fail CLOSED for a setting that removes safety prompts.
	return value == "true", nil
}

func putBool(ctx context.Context, tx *sql.Tx, key string, value bool, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, fmt.Sprintf("%t", value), db.FormatTime(now))
	if err != nil {
		return fmt.Errorf("write setting %s: %w", key, err)
	}
	return nil
}
