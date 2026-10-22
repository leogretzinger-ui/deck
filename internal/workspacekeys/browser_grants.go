package workspacekeys

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/noisefloor-oss/deck/internal/auth"
)

// GrantRevocation is delivered after a registry mutation commits or its storage
// fails. A zero Method revokes every grant. StorageFailed additionally prevents
// subsequent authorization until the gateway restarts.
type GrantRevocation struct {
	Method        MethodRef
	StorageFailed bool
}
type GrantRevocationSink interface{ RevokeRegistryGrants(GrantRevocation) }

// BindGrantRevocations installs the single stream owner before serving requests.
func (r *Registry) BindGrantRevocations(db *sql.DB, instanceID string, sink GrantRevocationSink) error {
	r.grantsMu.Lock()
	defer r.grantsMu.Unlock()
	if db != r.db || instanceID != r.cfg.InstanceID || sink == nil || r.grantsSink != nil {
		return ErrConflict
	}
	r.grantsSink = sink
	return nil
}
func (r *Registry) notifyGrants(event GrantRevocation) {
	r.grantsMu.Lock()
	sink := r.grantsSink
	r.grantsMu.Unlock()
	if sink != nil {
		sink.RevokeRegistryGrants(event)
	}
}

type browserProof struct {
	id, owner, login, epoch string
	method                  MethodRef
	operation               Operation
	at                      time.Time
}

func (r *Registry) markProofReceipts(result *AuthorizedOperation, row instance, actor Actor) {
	result.execution = &executionProof{browserProof: browserProof{id: result.ID, owner: row.owner, login: actor.Login.ID, epoch: row.RuntimeEpoch,
		method: result.Action.Method, operation: result.Action.Operation, at: r.cfg.Now()}, payloadDigest: digest(result.Payload)}
	switch result.Action.Operation {
	case Initialize, Unlock, AuthorizeBrowser:
		result.proof = &browserProof{id: result.ID, owner: row.owner, login: actor.Login.ID, epoch: row.RuntimeEpoch, method: result.Action.Method, operation: result.Action.Operation, at: r.cfg.Now()}
	}
}

// BrowserAuthority contains public grant bindings validated against a fresh,
// process-local proof receipt and its persisted operation. It is not a bearer.
type BrowserAuthority struct {
	InstanceID   string
	Owner        string
	LoginID      string
	Method       MethodRef
	RuntimeEpoch string
	OperationID  string
}

func (r *Registry) proofOperationTx(ctx context.Context, tx *sql.Tx, login auth.Login, permit AuthorizedOperation) (BrowserAuthority, string, error) {
	proof := permit.proof
	if permit.registry != r || proof == nil || permit.ID != proof.id || permit.RuntimeEpoch != proof.epoch || proof.login != login.ID ||
		!proof.at.Add(5*time.Minute).After(r.cfg.Now()) || proof.at.After(r.cfg.Now()) {
		return BrowserAuthority{}, "", ErrDenied
	}
	identity, err := owner(login)
	if err != nil || identity != proof.owner {
		return BrowserAuthority{}, "", ErrDenied
	}
	row, err := r.actorTx(ctx, tx, NewActor(login, ""))
	if err != nil {
		return BrowserAuthority{}, "", err
	}
	if row.RuntimeEpoch != proof.epoch {
		return BrowserAuthority{}, "", ErrDenied
	}
	var persistedOwner, persistedLogin, epoch, status string
	var payload []byte
	err = tx.QueryRowContext(ctx, `SELECT owner,login_id,epoch,status,action FROM workspacekeys_operations WHERE id=?`, proof.id).Scan(&persistedOwner, &persistedLogin, &epoch, &status, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return BrowserAuthority{}, "", ErrDenied
	}
	if err != nil {
		return BrowserAuthority{}, "", err
	}
	var action Action
	suppliedAction, err := json.Marshal(permit.Action)
	if err != nil || !bytes.Equal(suppliedAction, payload) || !bytes.Equal(permit.Payload, payload) || json.Unmarshal(payload, &action) != nil || persistedOwner != identity || persistedLogin != login.ID || epoch != proof.epoch ||
		action.Method != proof.method || action.Operation != proof.operation {
		return BrowserAuthority{}, "", ErrDenied
	}
	return BrowserAuthority{InstanceID: r.cfg.InstanceID, Owner: identity, LoginID: login.ID, Method: proof.method, RuntimeEpoch: proof.epoch, OperationID: proof.id}, status, nil
}

func (r *Registry) browserOperationTx(ctx context.Context, tx *sql.Tx, login auth.Login, permit AuthorizedOperation, completed bool) (BrowserAuthority, error) {
	authority, status, err := r.proofOperationTx(ctx, tx, login, permit)
	if err != nil {
		return BrowserAuthority{}, err
	}
	if completed && status != "complete" {
		return BrowserAuthority{}, ErrDenied
	}
	if _, err = r.ValidateGrantMethodTx(ctx, tx, login, authority.InstanceID, authority.RuntimeEpoch, authority.Method); err != nil {
		return BrowserAuthority{}, err
	}
	return authority, nil
}

// ValidateBrowserOperationTx is used inside the grant store's one-use claim transaction.
// Exported operation fields and durable initialization lookup alone carry no proof authority.
func (r *Registry) ValidateBrowserOperationTx(ctx context.Context, tx *sql.Tx, login auth.Login, permit AuthorizedOperation) (BrowserAuthority, error) {
	return r.browserOperationTx(ctx, tx, login, permit, true)
}

// ValidateGrantMethodTx checks the current owner, initialized state, epoch and method
// within the same transaction as a grant lookup or stream admission.
func (r *Registry) ValidateGrantMethodTx(ctx context.Context, tx *sql.Tx, login auth.Login, instanceID, epoch string, ref MethodRef) (string, error) {
	if instanceID != r.cfg.InstanceID {
		return "", ErrDenied
	}
	row, err := r.actorTx(ctx, tx, NewActor(login, ""))
	if err != nil {
		return "", err
	}
	if !row.Initialized || row.RuntimeEpoch != epoch {
		return "", ErrDenied
	}
	method, err := r.methodTx(ctx, tx, ref.ID)
	if err != nil {
		return "", err
	}
	if method.Status != "active" || method.MethodRef != ref {
		return "", ErrDenied
	}
	return row.owner, nil
}

// ConfirmUnlock follows the native controller's positive observation of the
// requested volume. It records completion but does not create browser access.
func (r *Registry) ConfirmUnlock(ctx context.Context, login auth.Login, permit AuthorizedOperation) error {
	if permit.proof == nil || permit.proof.operation != Unlock {
		return ErrDenied
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	authority, err := r.browserOperationTx(ctx, tx, login, permit, false)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workspacekeys_operations SET status='complete' WHERE id=? AND status='authorized'`, authority.OperationID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

// CompleteNativeTx binds a fresh initialization or unlock proof to the epoch
// positively observed by the native controller. The grant store must fence other
// operations and revoke old grants in this same transaction before publishing it.
func (r *Registry) CompleteNativeTx(ctx context.Context, tx *sql.Tx, login auth.Login, permit AuthorizedOperation, epoch string) (AuthorizedOperation, error) {
	if !identifier.MatchString(epoch) {
		return AuthorizedOperation{}, ErrInvalid
	}
	authority, status, err := r.proofOperationTx(ctx, tx, login, permit)
	if err != nil {
		return AuthorizedOperation{}, err
	}
	if status != "authorized" {
		return AuthorizedOperation{}, ErrDenied
	}
	switch permit.proof.operation {
	case Initialize:
		if err := r.finalizeInitializationTx(ctx, tx, permit); err != nil {
			return AuthorizedOperation{}, err
		}
	case Unlock:
		if _, err := r.ValidateGrantMethodTx(ctx, tx, login, authority.InstanceID, authority.RuntimeEpoch, authority.Method); err != nil {
			return AuthorizedOperation{}, err
		}
	default:
		return AuthorizedOperation{}, ErrDenied
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_operations SET status='complete',epoch=? WHERE id=?`, epoch, authority.OperationID); err != nil {
		return AuthorizedOperation{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_instance SET runtime_epoch=? WHERE id=?`, epoch, r.cfg.InstanceID); err != nil {
		return AuthorizedOperation{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspacekeys_challenges`); err != nil {
		return AuthorizedOperation{}, err
	}
	proof := *permit.proof
	proof.epoch = epoch
	permit.proof = &proof
	permit.RuntimeEpoch = epoch
	return permit, nil
}

// BrowserOperationIDsTx identifies existing browser-authorizing operations for
// an atomic grant-issuance fence. Pending initialization retains its intent ID
// for the fresh resume proof; native operation status is never rewritten here.
func (r *Registry) BrowserOperationIDsTx(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM workspacekeys_operations WHERE status IN ('complete','authorized') AND (json_extract(action,'$.operation') IN ('authorize-browser','unlock') OR (status='complete' AND json_extract(action,'$.operation')='initialize'))`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
