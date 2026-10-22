package workspacekeys

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

type methodRecord struct {
	Method
	credential     *webauthn.Credential
	registration   *webauthn.SessionData
	verifiedDigest string
	creatorLogin   string
	expires        int64
}

func (r *Registry) methodTx(ctx context.Context, tx *sql.Tx, id string) (methodRecord, error) {
	var row methodRecord
	var envelope, credential, registration []byte
	var verified int64
	err := tx.QueryRowContext(ctx, `SELECT id,revision,kind,label,status,salt,envelope,envelope_digest,verified_digest,verified_at,credential,registration,creator_login,expires FROM workspacekeys_methods WHERE id=?`, id).
		Scan(&row.ID, &row.Revision, &row.Kind, &row.Label, &row.Status, &row.Salt, &envelope, &row.EnvelopeDigest, &row.verifiedDigest, &verified, &credential, &registration, &row.creatorLogin, &row.expires)
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	if err != nil {
		return row, err
	}
	if len(envelope) > 0 {
		value, err := DecodeEnvelope(envelope)
		if err != nil {
			return row, err
		}
		row.Envelope = &value
	}
	if len(credential) > 0 {
		row.credential = &webauthn.Credential{}
		if err := json.Unmarshal(credential, row.credential); err != nil {
			return row, err
		}
		row.CredentialID = base64.RawURLEncoding.EncodeToString(row.credential.ID)
	}
	if len(registration) > 0 {
		row.registration = &webauthn.SessionData{}
		if err := json.Unmarshal(registration, row.registration); err != nil {
			return row, err
		}
	}
	if verified > 0 {
		at := time.UnixMilli(verified)
		row.VerifiedAt = &at
	}
	return row, nil
}

type webUser struct {
	id          []byte
	name        string
	credentials []webauthn.Credential
}

func (u webUser) WebAuthnID() []byte                         { return u.id }
func (u webUser) WebAuthnName() string                       { return u.name }
func (u webUser) WebAuthnDisplayName() string                { return "Deck workspace" }
func (u webUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func (r *Registry) userTx(ctx context.Context, tx *sql.Tx, row instance, actor Actor) (webUser, error) {
	u := webUser{id: row.userHandle, name: actor.Login.Email}
	rows, err := tx.QueryContext(ctx, `SELECT credential FROM workspacekeys_methods WHERE credential IS NOT NULL`)
	if err != nil {
		return u, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return u, err
		}
		var credential webauthn.Credential
		if err := json.Unmarshal(data, &credential); err != nil {
			return u, err
		}
		u.credentials = append(u.credentials, credential)
	}
	return u, rows.Err()
}

func (r *Registry) PrepareMethod(ctx context.Context, actor Actor, kind Kind, label string) (PreparedMethod, error) {
	if !kind.valid() || !validLabel(label) {
		return PreparedMethod{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PreparedMethod{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := r.actorTx(ctx, tx, actor)
	if err != nil {
		return PreparedMethod{}, err
	}
	if row.InitializingOperation != "" {
		return PreparedMethod{}, ErrConflict
	}
	if err := r.preparationTx(ctx, tx, actor, row); err != nil {
		return PreparedMethod{}, err
	}
	if err := r.selectEncryptedTx(ctx, tx); err != nil {
		return PreparedMethod{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_methods SET status='removed',registration=NULL WHERE status='pending' AND expires<=? AND (envelope IS NULL OR ?=1)`, r.cfg.Now().UnixMilli(), row.BootstrapClosed); err != nil {
		return PreparedMethod{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspacekeys_methods WHERE status='pending'`).Scan(&count); err != nil {
		return PreparedMethod{}, err
	}
	if count >= 16 {
		return PreparedMethod{}, ErrConflict
	}
	method := Method{MethodRef: MethodRef{ID: randomToken(), Revision: 1}, Kind: kind, Label: label, Status: "pending", Salt: randomToken()}
	prepared := PreparedMethod{Method: method}
	var registration []byte
	if kind == Passkey {
		user, err := r.userTx(ctx, tx, row, actor)
		if err != nil {
			return prepared, err
		}
		var session *webauthn.SessionData
		prepared.Registration, session, err = r.webauthn.BeginRegistration(user, webauthn.WithRegistrationOrigin(r.cfg.Origin),
			webauthn.WithExtensions(webauthn.WithExtensionPRFSupport(), webauthn.WithExtensionCredProps()))
		if err != nil {
			return prepared, err
		}
		session.Expires = r.cfg.Now().Add(5 * time.Minute)
		registration, err = json.Marshal(session)
		if err != nil {
			return prepared, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workspacekeys_methods(id,revision,kind,label,status,salt,registration,creator_login,expires) VALUES(?,1,?,?,'pending',?,?,?,?)`,
		method.ID, kind, label, method.Salt, registration, actor.Login.ID, r.cfg.Now().Add(24*time.Hour).UnixMilli())
	if err != nil {
		return PreparedMethod{}, err
	}
	if err := tx.Commit(); err != nil {
		return PreparedMethod{}, err
	}
	return prepared, nil
}

// LabelPending changes only preparation metadata. Renaming an active method
// still requires its separately bound management proof.
func (r *Registry) LabelPending(ctx context.Context, actor Actor, methodID, label string) error {
	if !identifier.MatchString(methodID) || !validLabel(label) {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := r.actorTx(ctx, tx, actor)
	if err != nil {
		return err
	}
	if row.InitializingOperation != "" {
		return ErrConflict
	}
	if err := r.preparationTx(ctx, tx, actor, row); err != nil {
		return err
	}
	method, err := r.methodTx(ctx, tx, methodID)
	if err != nil {
		return err
	}
	if method.Status != "pending" || (method.expires <= r.cfg.Now().UnixMilli() && (row.BootstrapClosed || method.Envelope == nil)) {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_methods SET label=? WHERE id=? AND status='pending'`, label, methodID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Registry) StoreEnvelope(ctx context.Context, actor Actor, methodID string, envelopeJSON, registrationJSON []byte, commitment string) error {
	envelope, err := DecodeEnvelope(envelopeJSON)
	if err != nil {
		return err
	}
	if _, err := decodeBytes(commitment, 32, 32); err != nil {
		return err
	}
	if envelope.InstanceID != r.cfg.InstanceID || envelope.MethodID != methodID {
		return ErrInvalid
	}
	if envelope.Kind == Passkey {
		if err := publicWebAuthnJSON(registrationJSON, true); err != nil {
			return err
		}
	} else if len(registrationJSON) != 0 {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := r.actorTx(ctx, tx, actor)
	if err != nil {
		return err
	}
	if row.InitializingOperation != "" {
		return ErrConflict
	}
	if err := r.preparationTx(ctx, tx, actor, row); err != nil {
		return err
	}
	method, err := r.methodTx(ctx, tx, methodID)
	if err != nil {
		return err
	}
	if method.Status != "pending" || method.Revision != envelope.Revision || method.Kind != envelope.Kind || method.Salt != envelope.Salt {
		return ErrConflict
	}
	if row.Commitment != "" && row.Commitment != commitment {
		return ErrConflict
	}
	if method.Envelope != nil {
		if method.EnvelopeDigest == digest(envelopeJSON) {
			return nil
		}
		return ErrConflict
	}
	if method.creatorLogin != actor.Login.ID || method.expires <= r.cfg.Now().UnixMilli() {
		return ErrDenied
	}
	var credentialJSON []byte
	if method.Kind == Passkey {
		if method.registration == nil || !method.registration.Expires.After(r.cfg.Now()) {
			return ErrDenied
		}
		user, err := r.userTx(ctx, tx, row, actor)
		if err != nil {
			return err
		}
		parsed, err := protocol.ParseCredentialCreationResponseBytes(registrationJSON)
		if err != nil {
			return ErrDenied
		}
		credential, err := r.webauthn.CreateCredential(user, *method.registration, parsed)
		if err != nil {
			return ErrDenied
		}
		if credential.Extensions.PRFEnabled == nil || !*credential.Extensions.PRFEnabled {
			return ErrDenied
		}
		for _, existing := range user.credentials {
			if base64.RawURLEncoding.EncodeToString(existing.ID) == base64.RawURLEncoding.EncodeToString(credential.ID) {
				return ErrConflict
			}
		}
		credentialJSON, err = json.Marshal(credential)
		if err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT envelope FROM workspacekeys_methods WHERE envelope IS NOT NULL`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return err
		}
		other, err := DecodeEnvelope(data)
		if err != nil {
			return err
		}
		if other.ProofPublicKey == envelope.ProofPublicKey {
			return ErrConflict
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if row.Commitment == "" {
		if row.BootstrapClosed {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_instance SET commitment=? WHERE id=? AND commitment=''`, commitment, r.cfg.InstanceID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_methods SET envelope=?,envelope_digest=?,credential=?,registration=NULL WHERE id=?`, envelopeJSON, digest(envelopeJSON), credentialJSON, methodID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Registry) Method(ctx context.Context, actor Actor, id string) (Method, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Method{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := r.actorTx(ctx, tx, actor); err != nil {
		return Method{}, err
	}
	row, err := r.methodTx(ctx, tx, id)
	if err != nil {
		return Method{}, err
	}
	if row.Status == "removed" {
		return Method{}, ErrNotFound
	}
	return row.Method, nil
}

func (r *Registry) Methods(ctx context.Context, actor Actor) ([]Method, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := r.actorTx(ctx, tx, actor); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM workspacekeys_methods WHERE status!='removed' ORDER BY id`)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]Method, 0, len(ids))
	for _, id := range ids {
		row, err := r.methodTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, row.Method)
	}
	return result, nil
}

func publicWebAuthnJSON(data []byte, registration bool) error {
	if len(data) == 0 || len(data) > 131072 || rejectDuplicateFields(data) != nil {
		return ErrInvalid
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return ErrInvalid
	}
	allowed := map[string]bool{"id": true, "rawId": true, "type": true, "authenticatorAttachment": true, "response": true, "clientExtensionResults": true}
	for key := range top {
		if !allowed[key] {
			return ErrInvalid
		}
	}
	var extensions map[string]json.RawMessage
	if value, ok := top["clientExtensionResults"]; ok {
		if rejectDuplicateFields(value) != nil || json.Unmarshal(value, &extensions) != nil {
			return ErrInvalid
		}
		for name, value := range extensions {
			var fields map[string]json.RawMessage
			if rejectDuplicateFields(value) != nil || json.Unmarshal(value, &fields) != nil {
				return ErrInvalid
			}
			if name != "prf" && name != "credProps" {
				return ErrInvalid
			}
			for field, flag := range fields {
				if (name == "prf" && field != "enabled") || (name == "credProps" && field != "rk") {
					return ErrInvalid
				}
				var boolean bool
				if string(flag) == "null" || json.Unmarshal(flag, &boolean) != nil {
					return ErrInvalid
				}
			}
		}
	}
	var response map[string]json.RawMessage
	if rejectDuplicateFields(top["response"]) != nil || json.Unmarshal(top["response"], &response) != nil {
		return ErrInvalid
	}
	allowedResponse := map[string]bool{"clientDataJSON": true}
	if registration {
		allowedResponse["attestationObject"] = true
		allowedResponse["transports"] = true
	} else {
		allowedResponse["authenticatorData"] = true
		allowedResponse["signature"] = true
		allowedResponse["userHandle"] = true
	}
	for field := range response {
		if !allowedResponse[field] {
			return ErrInvalid
		}
	}
	return nil
}
