package workspacegrants

import (
	"context"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
)

// ValidateExecution checks revocation before dispatch, not just after a native
// mount has happened. Lock has an execution receipt but never a grant receipt.
func (s *Store) ValidateExecution(ctx context.Context, login auth.Login, permit workspacekeys.AuthorizedOperation) error {
	keys := s.keys.Load()
	if keys == nil || s.closed.Load() || s.fault.Load() {
		return ErrDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := keys.ValidateExecutionTx(ctx, tx, login, permit); err != nil {
		return ErrDenied
	}
	var revoked bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspacegrants_logouts WHERE instance=? AND login_id=?) OR EXISTS(SELECT 1 FROM workspacegrants_claims WHERE operation_id=?)`, s.cfg.InstanceID, login.ID, permit.ID).Scan(&revoked)
	if err != nil || revoked {
		return ErrDenied
	}
	return nil
}
