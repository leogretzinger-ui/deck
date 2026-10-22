package workspacekeys

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/noisefloor-oss/deck/internal/auth"
)

type executionProof struct {
	browserProof
	payloadDigest string
	resume        bool
}

// RequireInitializationResume distinguishes a new resume ceremony from the
// original initialization proof, even though both carry the same durable ID.
func (r *Registry) RequireInitializationResume(ctx context.Context, login auth.Login, permit AuthorizedOperation) error {
	if permit.execution == nil || !permit.execution.resume || permit.Action.Operation != Initialize {
		return ErrDenied
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return r.ValidateExecutionTx(ctx, tx, login, permit)
}

// OperationRecord is a login-bound durable intent, never permission to dispatch
// a native command or issue a browser grant after a gateway restart.
type OperationRecord struct {
	ID           string
	Action       Action
	Status       string
	RuntimeEpoch string
}

func (r *Registry) Operation(ctx context.Context, login auth.Login, id string) (OperationRecord, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return OperationRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	record, _, err := r.operationTx(ctx, tx, login, id)
	return record, err
}

func (r *Registry) operationTx(ctx context.Context, tx *sql.Tx, login auth.Login, id string) (OperationRecord, []byte, error) {
	if !identifier.MatchString(id) {
		return OperationRecord{}, nil, ErrDenied
	}
	row, err := r.actorTx(ctx, tx, NewActor(login, ""))
	if err != nil {
		return OperationRecord{}, nil, ErrDenied
	}
	var record OperationRecord
	var identity, loginID string
	var payload []byte
	err = tx.QueryRowContext(ctx, `SELECT id,owner,login_id,epoch,action,status FROM workspacekeys_operations WHERE id=?`, id).
		Scan(&record.ID, &identity, &loginID, &record.RuntimeEpoch, &payload, &record.Status)
	if err != nil || identity != row.owner || loginID != login.ID || json.Unmarshal(payload, &record.Action) != nil {
		return OperationRecord{}, nil, ErrDenied
	}
	return record, payload, nil
}

// ValidateExecutionTx checks a fresh process-local receipt before native
// dispatch. The grant store checks its logout and revocation fences in this
// same transaction. Public fields and durable records cannot create receipts.
func (r *Registry) ValidateExecutionTx(ctx context.Context, tx *sql.Tx, login auth.Login, permit AuthorizedOperation) error {
	proof := permit.execution
	if permit.registry != r || proof == nil || permit.ID != proof.id || permit.RuntimeEpoch != proof.epoch ||
		proof.login != login.ID || !proof.at.Add(5*time.Minute).After(r.cfg.Now()) || proof.at.After(r.cfg.Now()) {
		return ErrDenied
	}
	identity, err := owner(login)
	if err != nil || identity != proof.owner {
		return ErrDenied
	}
	record, payload, err := r.operationTx(ctx, tx, login, permit.ID)
	if err != nil {
		return ErrDenied
	}
	supplied, err := json.Marshal(permit.Action)
	if err != nil || !bytes.Equal(supplied, payload) || !bytes.Equal(permit.Payload, payload) || digest(payload) != proof.payloadDigest ||
		record.RuntimeEpoch != proof.epoch || record.Action.Operation != proof.operation || record.Action.Method != proof.method {
		return ErrDenied
	}
	switch record.Action.Operation {
	case Initialize:
		row, err := r.actorTx(ctx, tx, NewActor(login, ""))
		if err != nil || row.RuntimeEpoch != proof.epoch || record.Status != "authorized" {
			return ErrDenied
		}
		pending, err := r.initializationTx(ctx, tx, row)
		if err != nil || pending.ID != permit.ID {
			return ErrDenied
		}
	case Unlock, Lock, AuthorizeBrowser:
		wantStatus := "authorized"
		if record.Action.Operation == AuthorizeBrowser {
			wantStatus = "complete"
		}
		if record.Status != wantStatus {
			return ErrDenied
		}
		if _, err := r.ValidateGrantMethodTx(ctx, tx, login, r.cfg.InstanceID, proof.epoch, proof.method); err != nil {
			return ErrDenied
		}
	case VerifyMethod, ResumeEnrollment, AddMethod, ReplaceMethod, RemoveMethod, RenameMethod:
		// These mutations already committed with proof consumption. A successful
		// self-removal must not fail because its authorizing method is now gone.
		if record.Status != "complete" {
			return ErrDenied
		}
	default:
		return ErrDenied
	}
	return nil
}
