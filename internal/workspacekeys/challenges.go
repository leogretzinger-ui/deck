package workspacekeys

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

type checkedAction struct {
	method                        methodRecord
	target, replacement, recovery *methodRecord
}

func (r *Registry) actionTx(ctx context.Context, tx *sql.Tx, row instance, action Action) (checkedAction, error) {
	var checked checkedAction
	if row.Onboarding.Protection != ProtectionEncrypted {
		return checked, ErrDenied
	}
	load := func(ref MethodRef) (methodRecord, error) {
		if !identifier.MatchString(ref.ID) || ref.Revision == 0 {
			return methodRecord{}, ErrInvalid
		}
		method, err := r.methodTx(ctx, tx, ref.ID)
		if err != nil {
			return method, err
		}
		if method.Revision != ref.Revision || method.Status == "removed" || method.Envelope == nil {
			return method, ErrConflict
		}
		return method, nil
	}
	var err error
	checked.method, err = load(action.Method)
	if err != nil {
		return checked, err
	}
	for _, item := range []struct {
		ref *MethodRef
		out **methodRecord
	}{{action.Target, &checked.target}, {action.Replacement, &checked.replacement}, {action.Recovery, &checked.recovery}} {
		if item.ref != nil {
			method, err := load(*item.ref)
			if err != nil {
				return checked, err
			}
			*item.out = &method
		}
	}
	plain := action.Target == nil && action.Replacement == nil && action.Recovery == nil && action.Label == ""
	pending := func(method *methodRecord) bool {
		return method != nil && method.Status == "pending" && method.verifiedDigest != "" && method.verifiedDigest == method.EnvelopeDigest && (!row.BootstrapClosed || method.expires > r.cfg.Now().UnixMilli())
	}
	if !row.BootstrapClosed {
		if checked.method.Status != "pending" {
			return checked, ErrDenied
		}
		if row.InitializingOperation != "" {
			if action.Operation != ResumeInitialization || !plain {
				return checked, ErrDenied
			}
			pending, err := r.initializationTx(ctx, tx, row)
			if err != nil {
				return checked, err
			}
			if pending.Action.Method != action.Method {
				return checked, ErrDenied
			}
			return checked, nil
		}
		switch action.Operation {
		case VerifyMethod, ResumeEnrollment:
			if !plain {
				return checked, ErrInvalid
			}
		case Initialize:
			if action.Target != nil || action.Replacement != nil || action.Label != "" || checked.method.Kind == Recovery ||
				!pending(&checked.method) || !pending(checked.recovery) || checked.recovery.Kind != Recovery || checked.recovery.ID == checked.method.ID {
				return checked, ErrDenied
			}
		default:
			return checked, ErrDenied
		}
		return checked, nil
	}
	if action.Operation == VerifyMethod {
		if !plain || (checked.method.Status != "active" && (checked.method.Status != "pending" || checked.method.expires <= r.cfg.Now().UnixMilli())) {
			return checked, ErrDenied
		}
		return checked, nil
	}
	if checked.method.Status != "active" {
		return checked, ErrDenied
	}
	switch action.Operation {
	case AuthorizeBrowser, Unlock, Lock:
		if !plain {
			return checked, ErrInvalid
		}
	case AddMethod:
		if !pending(checked.target) || action.Replacement != nil || action.Recovery != nil || action.Label != "" {
			return checked, ErrDenied
		}
	case ReplaceMethod:
		if !pending(checked.target) || checked.replacement == nil || checked.replacement.Status != "active" || action.Recovery != nil || action.Label != "" {
			return checked, ErrDenied
		}
	case RemoveMethod:
		if checked.target == nil || checked.target.Status != "active" || action.Replacement != nil || action.Recovery != nil || action.Label != "" {
			return checked, ErrDenied
		}
	case RenameMethod:
		if checked.target == nil || checked.target.Status != "active" || action.Replacement != nil || action.Recovery != nil || !validLabel(action.Label) {
			return checked, ErrDenied
		}
	default:
		return checked, ErrDenied
	}
	return checked, nil
}

func (r *Registry) BeginChallenge(ctx context.Context, actor Actor, action Action) (Challenge, error) {
	payload, err := json.Marshal(action)
	if err != nil {
		return Challenge{}, ErrInvalid
	}
	// Keep the stored decision independent of the caller's pointer fields.
	if err := json.Unmarshal(payload, &action); err != nil {
		return Challenge{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Challenge{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := r.actorTx(ctx, tx, actor)
	if err != nil {
		return Challenge{}, err
	}
	checked, err := r.actionTx(ctx, tx, row, action)
	if err != nil {
		return Challenge{}, err
	}
	receiptHash := ""
	if !row.BootstrapClosed && action.Operation != ResumeEnrollment && action.Operation != ResumeInitialization {
		receiptHash, err = r.requireReceiptTx(ctx, tx, actor, row)
		if err != nil {
			return Challenge{}, err
		}
	} else if row.BootstrapClosed && action.Operation == VerifyMethod {
		if err := r.preparationTx(ctx, tx, actor, row); err != nil {
			return Challenge{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspacekeys_challenges WHERE expires<=?`, r.cfg.Now().UnixMilli()); err != nil {
		return Challenge{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspacekeys_challenges WHERE login_id=?`, actor.Login.ID).Scan(&count); err != nil {
		return Challenge{}, err
	}
	if count >= 32 {
		return Challenge{}, ErrConflict
	}
	id := randomToken()
	result := Challenge{ID: id, OperationID: id, Challenge: id, ExpiresAt: r.cfg.Now().Add(5 * time.Minute)}
	if action.Operation == ResumeInitialization {
		result.OperationID = row.InitializingOperation
	}
	var assertionJSON []byte
	if checked.method.Kind == Passkey {
		if checked.method.credential == nil {
			return Challenge{}, ErrDenied
		}
		user, err := r.userTx(ctx, tx, row, actor)
		if err != nil {
			return Challenge{}, err
		}
		challenge, _ := decodeBytes(id, 32, 32)
		salt, _ := decodeBytes(checked.method.Salt, 32, 32)
		var session *webauthn.SessionData
		result.Assertion, session, err = r.webauthn.BeginLogin(user, webauthn.WithChallenge(challenge), webauthn.WithLoginOrigin(r.cfg.Origin),
			webauthn.WithAllowedCredentials([]protocol.CredentialDescriptor{checked.method.credential.Descriptor()}), webauthn.WithUserVerification(protocol.VerificationRequired),
			webauthn.WithAssertionExtensions(webauthn.WithExtensionPRF(protocol.PRFValues{First: salt})))
		if err != nil {
			return Challenge{}, err
		}
		session.Expires = result.ExpiresAt
		assertionJSON, err = json.Marshal(session)
		if err != nil {
			return Challenge{}, err
		}
	}
	targetDigest, recoveryDigest := "", ""
	if checked.target != nil {
		targetDigest = checked.target.EnvelopeDigest
	}
	if checked.recovery != nil {
		recoveryDigest = checked.recovery.EnvelopeDigest
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workspacekeys_challenges(id,owner,login_id,receipt_hash,enrollment_revision,epoch,expires,action,payload_digest,method_digest,target_digest,recovery_digest,assertion) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, row.owner, actor.Login.ID, receiptHash, row.EnrollmentRevision, row.RuntimeEpoch, result.ExpiresAt.UnixMilli(), payload, digest(payload), checked.method.EnvelopeDigest, targetDigest, recoveryDigest, assertionJSON)
	if err != nil {
		return Challenge{}, err
	}
	if err := tx.Commit(); err != nil {
		return Challenge{}, err
	}
	return result, nil
}

type challengeRecord struct {
	owner, login, receipt, epoch, payloadDigest, methodDigest, targetDigest, recoveryDigest string
	revision                                                                                uint32
	expires                                                                                 int64
	payload, assertion                                                                      []byte
}

// ChallengeAction is a non-consuming preflight for transient native-controller input.
// ConsumeProof repeats every authority check before authorizing any mutation.
func (r *Registry) ChallengeAction(ctx context.Context, actor Actor, id string) (Action, error) {
	if _, err := decodeBytes(id, 32, 32); err != nil {
		return Action{}, ErrDenied
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Action{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := r.actorTx(ctx, tx, actor)
	if err != nil {
		return Action{}, err
	}
	var stored challengeRecord
	err = tx.QueryRowContext(ctx, `SELECT owner,login_id,receipt_hash,enrollment_revision,epoch,expires,action,payload_digest FROM workspacekeys_challenges WHERE id=?`, id).Scan(&stored.owner, &stored.login, &stored.receipt, &stored.revision, &stored.epoch, &stored.expires, &stored.payload, &stored.payloadDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return Action{}, ErrDenied
	}
	if err != nil {
		return Action{}, err
	}
	if stored.owner != row.owner || stored.login != actor.Login.ID || stored.epoch != row.RuntimeEpoch || stored.revision != row.EnrollmentRevision || stored.expires <= r.cfg.Now().UnixMilli() || digest(stored.payload) != stored.payloadDigest {
		return Action{}, ErrDenied
	}
	var action Action
	if json.Unmarshal(stored.payload, &action) != nil {
		return Action{}, ErrDenied
	}
	if _, err := r.actionTx(ctx, tx, row, action); err != nil {
		return Action{}, err
	}
	if !row.BootstrapClosed && action.Operation != ResumeEnrollment && action.Operation != ResumeInitialization {
		hash, err := r.requireReceiptTx(ctx, tx, actor, row)
		if err != nil || hash != stored.receipt {
			return Action{}, ErrDenied
		}
	} else if row.BootstrapClosed && action.Operation == VerifyMethod {
		if err := r.preparationTx(ctx, tx, actor, row); err != nil {
			return Action{}, err
		}
	}
	return action, nil
}

func (r *Registry) ConsumeProof(ctx context.Context, actor Actor, id, signature string, assertionJSON []byte) (AuthorizedOperation, error) {
	challengeBytes, err := decodeBytes(id, 32, 32)
	if err != nil {
		return AuthorizedOperation{}, ErrDenied
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return AuthorizedOperation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := r.actorTx(ctx, tx, actor)
	if err != nil {
		return AuthorizedOperation{}, err
	}
	var stored challengeRecord
	err = tx.QueryRowContext(ctx, `SELECT owner,login_id,receipt_hash,enrollment_revision,epoch,expires,action,payload_digest,method_digest,target_digest,recovery_digest,assertion FROM workspacekeys_challenges WHERE id=?`, id).
		Scan(&stored.owner, &stored.login, &stored.receipt, &stored.revision, &stored.epoch, &stored.expires, &stored.payload, &stored.payloadDigest, &stored.methodDigest, &stored.targetDigest, &stored.recoveryDigest, &stored.assertion)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizedOperation{}, ErrDenied
	}
	if err != nil {
		return AuthorizedOperation{}, err
	}
	if stored.owner != row.owner || stored.login != actor.Login.ID || stored.epoch != row.RuntimeEpoch || stored.revision != row.EnrollmentRevision ||
		stored.expires <= r.cfg.Now().UnixMilli() || digest(stored.payload) != stored.payloadDigest {
		return AuthorizedOperation{}, ErrDenied
	}
	var action Action
	if err := json.Unmarshal(stored.payload, &action); err != nil {
		return AuthorizedOperation{}, ErrDenied
	}
	checked, err := r.actionTx(ctx, tx, row, action)
	if err != nil {
		return AuthorizedOperation{}, err
	}
	if checked.method.EnvelopeDigest != stored.methodDigest || checked.target != nil && checked.target.EnvelopeDigest != stored.targetDigest || checked.recovery != nil && checked.recovery.EnvelopeDigest != stored.recoveryDigest {
		return AuthorizedOperation{}, ErrConflict
	}
	if !row.BootstrapClosed && action.Operation != ResumeEnrollment && action.Operation != ResumeInitialization {
		hash, err := r.requireReceiptTx(ctx, tx, actor, row)
		if err != nil || hash != stored.receipt {
			return AuthorizedOperation{}, ErrDenied
		}
	} else if row.BootstrapClosed && action.Operation == VerifyMethod {
		if err := r.preparationTx(ctx, tx, actor, row); err != nil {
			return AuthorizedOperation{}, err
		}
	}
	if err := VerifyProof(checked.method.Envelope.ProofPublicKey, challengeBytes, signature); err != nil {
		return AuthorizedOperation{}, ErrDenied
	}
	if checked.method.Kind == Passkey {
		if checked.method.credential == nil || len(stored.assertion) == 0 || publicWebAuthnJSON(assertionJSON, false) != nil {
			return AuthorizedOperation{}, ErrDenied
		}
		var session webauthn.SessionData
		if json.Unmarshal(stored.assertion, &session) != nil {
			return AuthorizedOperation{}, ErrDenied
		}
		parsed, err := protocol.ParseCredentialRequestResponseBytes(assertionJSON)
		if err != nil {
			return AuthorizedOperation{}, ErrDenied
		}
		user, err := r.userTx(ctx, tx, row, actor)
		if err != nil {
			return AuthorizedOperation{}, err
		}
		credential, err := r.webauthn.ValidateLogin(user, session, parsed)
		if err != nil {
			return AuthorizedOperation{}, ErrDenied
		}
		if credential.Authenticator.CloneWarning && !credential.Flags.BackupEligible {
			return AuthorizedOperation{}, ErrDenied
		}
		updated, err := json.Marshal(credential)
		if err != nil {
			return AuthorizedOperation{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_methods SET credential=? WHERE id=?`, updated, checked.method.ID); err != nil {
			return AuthorizedOperation{}, err
		}
	} else if len(assertionJSON) != 0 {
		return AuthorizedOperation{}, ErrInvalid
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspacekeys_challenges WHERE id=?`, id); err != nil {
		return AuthorizedOperation{}, err
	}
	if action.Operation == ResumeInitialization {
		result, err := r.initializationTx(ctx, tx, row)
		if err != nil {
			return AuthorizedOperation{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_operations SET epoch=?,login_id=? WHERE id=? AND status='authorized'`, row.RuntimeEpoch, actor.Login.ID, result.ID); err != nil {
			return AuthorizedOperation{}, err
		}
		result.RuntimeEpoch = row.RuntimeEpoch
		r.markProofReceipts(&result, row, actor)
		result.execution.resume = true
		if err := tx.Commit(); err != nil {
			return AuthorizedOperation{}, err
		}
		return result, nil
	}
	result := AuthorizedOperation{ID: id, Action: action, Payload: append(json.RawMessage(nil), stored.payload...), RuntimeEpoch: row.RuntimeEpoch, registry: r}
	if err := r.applyTx(ctx, tx, actor, row, checked, &result); err != nil {
		return AuthorizedOperation{}, err
	}
	status := "complete"
	if action.Operation == Initialize || action.Operation == Unlock || action.Operation == Lock {
		status = "authorized"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workspacekeys_operations(id,owner,login_id,epoch,action,status,enrollment_revision,method_digest,recovery_digest) VALUES(?,?,?,?,?,?,?,?,?)`,
		result.ID, row.owner, actor.Login.ID, row.RuntimeEpoch, stored.payload, status, row.EnrollmentRevision, stored.methodDigest, stored.recoveryDigest)
	if err != nil {
		return AuthorizedOperation{}, err
	}
	r.markProofReceipts(&result, row, actor)
	if err := tx.Commit(); err != nil {
		return AuthorizedOperation{}, err
	}
	switch action.Operation {
	case RemoveMethod:
		r.notifyGrants(GrantRevocation{Method: *action.Target})
	case ReplaceMethod:
		r.notifyGrants(GrantRevocation{Method: *action.Replacement})
	case Lock:
		r.notifyGrants(GrantRevocation{})
	}
	return result, nil
}

func (r *Registry) applyTx(ctx context.Context, tx *sql.Tx, actor Actor, row instance, checked checkedAction, result *AuthorizedOperation) error {
	action := result.Action
	switch action.Operation {
	case VerifyMethod:
		_, err := tx.ExecContext(ctx, `UPDATE workspacekeys_methods SET verified_digest=envelope_digest,verified_at=? WHERE id=?`, r.cfg.Now().UnixMilli(), checked.method.ID)
		return err
	case ResumeEnrollment:
		if row.EnrollmentRevision == ^uint32(0) {
			return ErrConflict
		}
		revision := row.EnrollmentRevision + 1
		if _, err := tx.ExecContext(ctx, `DELETE FROM workspacekeys_receipts`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM workspacekeys_challenges`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_instance SET enrollment_revision=? WHERE id=?`, revision, r.cfg.InstanceID); err != nil {
			return err
		}
		receipt, err := r.receiptTx(ctx, tx, row.owner, actor.Login.ID, revision)
		if err != nil {
			return err
		}
		result.Receipt = &receipt
	case Initialize:
		_, err := tx.ExecContext(ctx, `UPDATE workspacekeys_instance SET initializing_operation=? WHERE id=?`, result.ID, r.cfg.InstanceID)
		return err
	case AddMethod, ReplaceMethod:
		if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_methods SET status='active' WHERE id=?`, checked.target.ID); err != nil {
			return err
		}
		if action.Operation == ReplaceMethod {
			if err := r.removeTx(ctx, tx, checked.replacement.ID); err != nil {
				return err
			}
		}
	case RemoveMethod:
		return r.removeTx(ctx, tx, checked.target.ID)
	case RenameMethod:
		_, err := tx.ExecContext(ctx, `UPDATE workspacekeys_methods SET label=? WHERE id=?`, action.Label, checked.target.ID)
		return err
	case Lock:
		_, err := tx.ExecContext(ctx, `DELETE FROM workspacekeys_challenges`)
		return err
	}
	return nil
}

func (r *Registry) removeTx(ctx context.Context, tx *sql.Tx, id string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_methods SET status='removed' WHERE id=? AND status='active'`, id); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspacekeys_methods WHERE status='active' AND kind!='recovery'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrDenied
	}
	return nil
}

// PendingInitialization exposes a durable intent only to the trusted native reconciler.
// It does not authorize a browser or permit replay of an uncertain native effect.
func (r *Registry) PendingInitialization(ctx context.Context) (AuthorizedOperation, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return AuthorizedOperation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := r.stateTx(ctx, tx)
	if err != nil {
		return AuthorizedOperation{}, err
	}
	return r.initializationTx(ctx, tx, row)
}

func (r *Registry) initializationTx(ctx context.Context, tx *sql.Tx, row instance) (AuthorizedOperation, error) {
	if row.BootstrapClosed || row.InitializingOperation == "" {
		return AuthorizedOperation{}, ErrNotFound
	}
	var payload []byte
	var identity, epoch, status, methodDigest, recoveryDigest string
	var revision uint32
	err := tx.QueryRowContext(ctx, `SELECT owner,action,epoch,status,enrollment_revision,method_digest,recovery_digest FROM workspacekeys_operations WHERE id=?`, row.InitializingOperation).Scan(&identity, &payload, &epoch, &status, &revision, &methodDigest, &recoveryDigest)
	if err != nil {
		return AuthorizedOperation{}, err
	}
	var action Action
	if identity != row.owner || status != "authorized" || revision != row.EnrollmentRevision || json.Unmarshal(payload, &action) != nil || action.Operation != Initialize || action.Recovery == nil {
		return AuthorizedOperation{}, ErrConflict
	}
	method, err := r.methodTx(ctx, tx, action.Method.ID)
	if err != nil {
		return AuthorizedOperation{}, err
	}
	recovery, err := r.methodTx(ctx, tx, action.Recovery.ID)
	if err != nil {
		return AuthorizedOperation{}, err
	}
	if method.MethodRef != action.Method || recovery.MethodRef != *action.Recovery || method.Status != "pending" || recovery.Status != "pending" || method.EnvelopeDigest != methodDigest || method.verifiedDigest != methodDigest || recovery.EnvelopeDigest != recoveryDigest || recovery.verifiedDigest != recoveryDigest {
		return AuthorizedOperation{}, ErrConflict
	}
	return AuthorizedOperation{ID: row.InitializingOperation, Action: action, Payload: payload, RuntimeEpoch: epoch, registry: r}, nil
}

// FinalizeInitialization follows a confirmed native initialization/mount, never a browser success flag.
func (r *Registry) FinalizeInitialization(ctx context.Context, permit AuthorizedOperation) error {
	if permit.registry != r {
		return ErrDenied
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.finalizeInitializationTx(ctx, tx, permit); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Registry) finalizeInitializationTx(ctx context.Context, tx *sql.Tx, permit AuthorizedOperation) error {
	row, err := r.stateTx(ctx, tx)
	if err != nil {
		return err
	}
	pending, err := r.initializationTx(ctx, tx, row)
	if err != nil {
		return err
	}
	if pending.ID != permit.ID || pending.RuntimeEpoch != row.RuntimeEpoch || permit.RuntimeEpoch != pending.RuntimeEpoch {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_methods SET status='active' WHERE id IN (?,?)`, pending.Action.Method.ID, pending.Action.Recovery.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_instance SET bootstrap_closed=1,initializing_operation='' WHERE id=?`, r.cfg.InstanceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspacekeys_receipts`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspacekeys_challenges`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_operations SET status='complete' WHERE id=?`, permit.ID); err != nil {
		return err
	}
	return nil
}
