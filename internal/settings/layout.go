package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/noisefloor-oss/deck/internal/db"
)

// KeyLayout is the stored key for the sidebar layout document. It lives in
// the same table as the boolean settings but under its own key so a Settings
// Put — which replaces the whole settings document — cannot overwrite it.
const KeyLayout = "sidebar_layout"

// Layout is the user's sidebar arrangement. It is stored as one JSON
// value rather than split across rows because the whole thing is a single
// arrangement: a partial write would leave projects and sessions out of sync.
type Layout struct {
	Expanded []string            `json:"expanded"`
	Projects []string            `json:"projects"`
	Sessions map[string][]string `json:"sessions"`
	// Colors maps a project id to its user color tag — the same key
	// set the session color endpoint accepts. It rides the layout document
	// because a project colour is presentation state of one arrangement
	// surface, not a per-project row worth its own table.
	Colors map[string]string `json:"colors,omitempty"`
}

// GetLayout reads the whole layout document. The second return value is true
// when a row exists and false when the document has never been written. A
// missing row is not an error: the UI treats it as the default alphabetical
// order and its default expansion.
func (r *Repository) GetLayout(ctx context.Context) (Layout, bool, error) {
	raw, err := r.getString(ctx, KeyLayout)
	if err != nil {
		return Layout{}, false, err
	}
	if raw == "" {
		return Layout{}, false, nil
	}
	var l Layout
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		return Layout{}, false, fmt.Errorf("decode layout: %w", err)
	}
	return l, true, nil
}

// SetLayout writes the whole layout document. Layout changes are not audited:
// dragging a project row is a presentation preference, not a security event,
// and the settings audit trail must stay focused on safety flags.
func (r *Repository) SetLayout(ctx context.Context, next Layout) error {
	now := r.now()
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		raw, err := json.Marshal(next)
		if err != nil {
			return fmt.Errorf("encode layout: %w", err)
		}
		return putString(ctx, tx, KeyLayout, string(raw), now)
	})
}

func (r *Repository) getString(ctx context.Context, key string) (string, error) {
	var value string
	err := r.db.Reader().QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("read setting %s: %w", key, err)
	}
	return value, nil
}

func putString(ctx context.Context, tx *sql.Tx, key, value string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, db.FormatTime(now))
	if err != nil {
		return fmt.Errorf("write setting %s: %w", key, err)
	}
	return nil
}
