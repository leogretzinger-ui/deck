package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// keyLastSessionV1 prefixes the settings-table row that remembers which
// session the user last opened. The full key is the prefix plus the
// authenticated principal, like the tutorial record: a user's place
// is theirs, and on an unauthenticated single-tenant installation the one
// record is installation-wide.
const keyLastSessionV1 = "last_session_v1"

// maxSessionIDLength bounds a stored id. Ids are opaque to this package;
// the bound only keeps a client from storing a document here.
const maxSessionIDLength = 256

var errSessionIDTooLong = errors.New("session id is too long")

func lastSessionKey(principal string) string {
	if principal == "" {
		return keyLastSessionV1
	}
	return keyLastSessionV1 + ":" + principal
}

// GetLastSession reads the remembered session id. The second return value
// is false when nothing is remembered; a cleared record reads the same as
// one never written. The id is a preference, never an authority: the
// console validates it against the live session list before opening it.
func (r *Repository) GetLastSession(ctx context.Context, principal string) (string, bool, error) {
	id, err := r.getString(ctx, lastSessionKey(principal))
	if err != nil {
		return "", false, err
	}
	return id, id != "", nil
}

// SetLastSession remembers the id, or forgets it when id is empty. Like the
// layout, it is not audited: where the user was last looking is a
// presentation preference, not a safety event.
func (r *Repository) SetLastSession(ctx context.Context, principal, id string) error {
	if len(id) > maxSessionIDLength {
		return errSessionIDTooLong
	}
	now := r.now()
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := putString(ctx, tx, lastSessionKey(principal), id, now); err != nil {
			return fmt.Errorf("write last session: %w", err)
		}
		return nil
	})
}
