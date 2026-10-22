package workspacegrants

import (
	"context"
	"database/sql"
	"errors"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
)

// CompleteNative follows a positive native observation of the requested volume
// running at epoch. It atomically completes the fresh proof, revokes old browser
// access and preserves only this operation for Issue. Epoch is never browser input.
func (s *Store) CompleteNative(ctx context.Context, login auth.Login, permit workspacekeys.AuthorizedOperation, epoch string) (_ workspacekeys.AuthorizedOperation, resultErr error) {
	keys := s.keys.Load()
	if keys == nil || s.closed.Load() || s.fault.Load() {
		return workspacekeys.AuthorizedOperation{}, ErrDenied
	}
	// Native state has already changed. A failed or uncertain storage write
	// cannot leave old-epoch streams authorized until another request arrives.
	defer func() {
		if resultErr != nil && !errors.Is(resultErr, ErrDenied) && !errors.Is(resultErr, workspacekeys.ErrDenied) &&
			!errors.Is(resultErr, workspacekeys.ErrConflict) && !errors.Is(resultErr, workspacekeys.ErrInvalid) && !errors.Is(resultErr, workspacekeys.ErrNotFound) {
			s.failClosed()
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workspacekeys.AuthorizedOperation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var revoked bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspacegrants_logouts WHERE instance=? AND login_id=?) OR EXISTS(SELECT 1 FROM workspacegrants_claims WHERE operation_id=?)`, s.cfg.InstanceID, login.ID, permit.ID).Scan(&revoked)
	if err != nil {
		return workspacekeys.AuthorizedOperation{}, err
	}
	if revoked {
		return workspacekeys.AuthorizedOperation{}, ErrDenied
	}
	completed, err := keys.CompleteNativeTx(ctx, tx, login, permit, epoch)
	if err != nil {
		return workspacekeys.AuthorizedOperation{}, err
	}
	if err := s.fenceOperationsTx(ctx, tx, completed.ID); err != nil {
		return workspacekeys.AuthorizedOperation{}, err
	}
	s.mu.Lock()
	if s.closed.Load() || s.fault.Load() {
		s.mu.Unlock()
		return workspacekeys.AuthorizedOperation{}, ErrDenied
	}
	if err := tx.Commit(); err != nil {
		s.mu.Unlock()
		return workspacekeys.AuthorizedOperation{}, err
	}
	callbacks := s.detachLocked(func(*streamGroup) bool { return true })
	s.mu.Unlock()
	closeStreams(callbacks)
	return completed, nil
}

func (s *Store) fenceOperationsTx(ctx context.Context, tx *sql.Tx, preserve string) error {
	keys := s.keys.Load()
	if keys == nil {
		return ErrDenied
	}
	ids, err := keys.BrowserOperationIDsTx(ctx, tx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if id == preserve {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workspacegrants_claims(operation_id,instance) VALUES(?,?)`, id, s.cfg.InstanceID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM workspacegrants_grants WHERE instance=?`, s.cfg.InstanceID)
	return err
}
