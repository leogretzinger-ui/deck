package workspacekeys

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/noisefloor-oss/deck/internal/auth"
)

type Registry struct {
	db         *sql.DB
	cfg        Config
	webauthn   *webauthn.WebAuthn
	grantsMu   sync.Mutex
	grantsSink GrantRevocationSink
}

const schema = `
CREATE TABLE IF NOT EXISTS workspacekeys_instance (
 id TEXT PRIMARY KEY, version INTEGER NOT NULL, rpid TEXT NOT NULL, origin TEXT NOT NULL,
 owner TEXT NOT NULL DEFAULT '', user_handle BLOB NOT NULL, commitment TEXT NOT NULL DEFAULT '',
 enrollment_revision INTEGER NOT NULL DEFAULT 0, bootstrap_closed INTEGER NOT NULL DEFAULT 0,
 runtime_epoch TEXT NOT NULL, initializing_operation TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS workspacekeys_receipts (
 hash TEXT PRIMARY KEY, owner TEXT NOT NULL, login_id TEXT NOT NULL, revision INTEGER NOT NULL, expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS workspacekeys_methods (
 id TEXT PRIMARY KEY, revision INTEGER NOT NULL, kind TEXT NOT NULL, label TEXT NOT NULL,
 status TEXT NOT NULL, salt TEXT NOT NULL, envelope BLOB, envelope_digest TEXT NOT NULL DEFAULT '',
 verified_digest TEXT NOT NULL DEFAULT '', verified_at INTEGER NOT NULL DEFAULT 0,
 credential BLOB, registration BLOB, creator_login TEXT NOT NULL, expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS workspacekeys_challenges (
 id TEXT PRIMARY KEY, owner TEXT NOT NULL, login_id TEXT NOT NULL, receipt_hash TEXT NOT NULL,
 enrollment_revision INTEGER NOT NULL, epoch TEXT NOT NULL, expires INTEGER NOT NULL,
 action BLOB NOT NULL, payload_digest TEXT NOT NULL, method_digest TEXT NOT NULL,
 target_digest TEXT NOT NULL, recovery_digest TEXT NOT NULL, assertion BLOB
);
CREATE TABLE IF NOT EXISTS workspacekeys_operations (
 id TEXT PRIMARY KEY, owner TEXT NOT NULL, login_id TEXT NOT NULL, epoch TEXT NOT NULL,
 action BLOB NOT NULL, status TEXT NOT NULL, enrollment_revision INTEGER NOT NULL,
 method_digest TEXT NOT NULL, recovery_digest TEXT NOT NULL
);`

func New(ctx context.Context, db *sql.DB, cfg Config) (*Registry, error) {
	return openRegistry(ctx, db, cfg, true)
}

// OpenExisting lets the local invitation command inspect the installation
// without invalidating ceremonies in the running gateway. It neither creates
// an installation nor supplies browser preparation authority.
func OpenExisting(ctx context.Context, db *sql.DB, cfg Config) (*Registry, error) {
	return openRegistry(ctx, db, cfg, false)
}

func openRegistry(ctx context.Context, db *sql.DB, cfg Config, startup bool) (*Registry, error) {
	origin, err := url.Parse(cfg.Origin)
	if err != nil || origin.Scheme != "https" || origin.Hostname() != cfg.RPID || origin.User != nil ||
		origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || cfg.RPID == "" ||
		!identifier.MatchString(cfg.InstanceID) || db == nil {
		return nil, ErrInvalid
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.EncryptionPolicy == "" {
		cfg.EncryptionPolicy = EncryptionRequired
	}
	if cfg.EncryptionPolicy != EncryptionRequired && cfg.EncryptionPolicy != EncryptionOptional && cfg.EncryptionPolicy != EncryptionUnavailable {
		return nil, ErrInvalid
	}
	w, err := webauthn.New(&webauthn.Config{RPID: cfg.RPID, RPDisplayName: "Deck", RPOrigins: []string{cfg.Origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{UserVerification: protocol.VerificationRequired,
			ResidentKey: protocol.ResidentKeyRequirementRequired}, AttestationPreference: protocol.PreferNoAttestation})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	r := &Registry{db: db, cfg: cfg, webauthn: w}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	fresh := false
	if startup {
		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workspacekeys_instance(id,version,rpid,origin,user_handle,runtime_epoch) VALUES(?,1,?,?,?,?)`,
			cfg.InstanceID, cfg.RPID, cfg.Origin, randomBytes(), randomToken())
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		fresh = affected == 1
	}
	var id, rpid, storedOrigin string
	var version, count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspacekeys_instance`).Scan(&count); err != nil {
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT id,version,rpid,origin FROM workspacekeys_instance`).Scan(&id, &version, &rpid, &storedOrigin); err != nil {
		return nil, err
	}
	if count != 1 || id != cfg.InstanceID || version != 1 || rpid != cfg.RPID || storedOrigin != cfg.Origin {
		return nil, ErrConflict
	}
	if err := r.openOnboardingTx(ctx, tx, startup, fresh); err != nil {
		return nil, err
	}
	if startup {
		if _, err := tx.ExecContext(ctx, `DELETE FROM workspacekeys_challenges`); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_methods SET registration=NULL WHERE status='pending'`); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r, nil
}

func randomBytes() []byte { value := make([]byte, 32); _, _ = rand.Read(value); return value }
func randomToken() string { return base64.RawURLEncoding.EncodeToString(randomBytes()) }

func owner(login auth.Login) (string, error) {
	id, err := hex.DecodeString(login.ID)
	if err != nil || len(id) != 32 || hex.EncodeToString(id) != login.ID {
		return "", ErrDenied
	}
	for _, value := range []string{login.Issuer, login.Subject, login.Email} {
		if value == "" || len(value) > 1024 || !utf8.ValidString(value) {
			return "", ErrDenied
		}
	}
	encoded, _ := json.Marshal([]string{login.Issuer, login.Subject})
	return digest(encoded), nil
}

type instance struct {
	State
	owner      string
	userHandle []byte
}

func (r *Registry) stateTx(ctx context.Context, tx *sql.Tx) (instance, error) {
	var row instance
	err := tx.QueryRowContext(ctx, `SELECT owner,user_handle,commitment,enrollment_revision,bootstrap_closed,runtime_epoch,initializing_operation FROM workspacekeys_instance WHERE id=?`,
		r.cfg.InstanceID).Scan(&row.owner, &row.userHandle, &row.Commitment, &row.EnrollmentRevision, &row.BootstrapClosed, &row.RuntimeEpoch, &row.InitializingOperation)
	row.OwnerBound = row.owner != ""
	row.Initialized = row.BootstrapClosed
	if err == nil {
		row.Onboarding, row.PlainOperationID, err = r.onboardingTx(ctx, tx)
	}
	return row, err
}

func (r *Registry) actorTx(ctx context.Context, tx *sql.Tx, actor Actor) (instance, error) {
	identity, err := owner(actor.Login)
	if err != nil {
		return instance{}, err
	}
	row, err := r.stateTx(ctx, tx)
	if err != nil {
		return row, err
	}
	if row.owner != identity {
		return row, ErrDenied
	}
	return row, nil
}

func (r *Registry) State(ctx context.Context, login auth.Login) (State, error) {
	identity, err := owner(login)
	if err != nil {
		return State{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return State{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := r.stateTx(ctx, tx)
	if err == nil && row.OwnerBound && row.owner != identity {
		return State{}, ErrDenied
	}
	return row.State, err
}

func (r *Registry) OwnerMatches(ctx context.Context, login auth.Login) (bool, error) {
	state, err := r.State(ctx, login)
	if errors.Is(err, ErrDenied) {
		return false, nil
	}
	return state.OwnerBound, err
}

// CheckConfiguration prevents a gateway from serving ceremonies for another registry origin.
func (r *Registry) CheckConfiguration(instanceID, rpID, origin string) bool {
	return r.cfg.InstanceID == instanceID && r.cfg.RPID == rpID && r.cfg.Origin == origin
}

// CanEnrollTx shares the invitation transaction, so an already bound workspace cannot issue a useful invitation.
func (r *Registry) CanEnrollTx(ctx context.Context, tx *sql.Tx) error {
	row, err := r.stateTx(ctx, tx)
	if err != nil {
		return err
	}
	if row.OwnerBound || row.BootstrapClosed || row.Commitment != "" || row.InitializingOperation != "" || row.EnrollmentRevision != 0 {
		return ErrConflict
	}
	return nil
}

// CreateEnrollmentTx shares the gateway transaction that consumes the one-use invitation.
func (r *Registry) CreateEnrollmentTx(ctx context.Context, tx *sql.Tx, login auth.Login) (EnrollmentReceipt, error) {
	identity, err := owner(login)
	if err != nil {
		return EnrollmentReceipt{}, err
	}
	if err := r.CanEnrollTx(ctx, tx); err != nil {
		return EnrollmentReceipt{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workspacekeys_instance SET owner=?,enrollment_revision=1 WHERE id=? AND owner='' AND bootstrap_closed=0`, identity, r.cfg.InstanceID)
	if err != nil {
		return EnrollmentReceipt{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return EnrollmentReceipt{}, ErrConflict
	}
	return r.receiptTx(ctx, tx, identity, login.ID, 1)
}

func (r *Registry) receiptTx(ctx context.Context, tx *sql.Tx, identity, login string, revision uint32) (EnrollmentReceipt, error) {
	receipt := EnrollmentReceipt{Revision: revision, ExpiresAt: r.cfg.Now().Add(12 * time.Hour), token: randomToken()}
	decoded, _ := decodeBytes(receipt.token, 32, 32)
	_, err := tx.ExecContext(ctx, `INSERT INTO workspacekeys_receipts(hash,owner,login_id,revision,expires) VALUES(?,?,?,?,?)`,
		digest(decoded), identity, login, revision, receipt.ExpiresAt.UnixMilli())
	return receipt, err
}

func (r *Registry) requireReceiptTx(ctx context.Context, tx *sql.Tx, actor Actor, row instance) (string, error) {
	if row.BootstrapClosed || row.Onboarding.Protection == ProtectionNone {
		return "", ErrDenied
	}
	decoded, err := decodeBytes(actor.enrollmentToken, 32, 32)
	if err != nil {
		return "", ErrDenied
	}
	hash := digest(decoded)
	var count int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM workspacekeys_receipts WHERE hash=? AND owner=? AND login_id=? AND revision=? AND expires>?`,
		hash, row.owner, actor.Login.ID, row.EnrollmentRevision, r.cfg.Now().UnixMilli()).Scan(&count)
	if err != nil {
		return "", err
	}
	if count != 1 {
		return "", ErrDenied
	}
	return hash, nil
}

func (r *Registry) preparationTx(ctx context.Context, tx *sql.Tx, actor Actor, row instance) error {
	if !row.BootstrapClosed {
		_, err := r.requireReceiptTx(ctx, tx, actor, row)
		return err
	}
	if r.cfg.Preparation == nil {
		return ErrDenied
	}
	ref, err := r.cfg.Preparation.RequirePreparationTx(ctx, tx, actor.Login, r.cfg.InstanceID, row.RuntimeEpoch)
	if err != nil {
		return ErrDenied
	}
	method, err := r.methodTx(ctx, tx, ref.ID)
	if err != nil || method.Status != "active" || method.Revision != ref.Revision {
		return ErrDenied
	}
	return nil
}

func (r *Registry) RevokeEnrollment(ctx context.Context, login auth.Login) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := r.actorTx(ctx, tx, NewActor(login, ""))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspacekeys_receipts WHERE owner=? AND login_id=?`, row.owner, login.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspacekeys_challenges WHERE owner=? AND login_id=?`, row.owner, login.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetRuntimeEpoch is called with a controller observation, never a browser parameter.
func (r *Registry) SetRuntimeEpoch(ctx context.Context, epoch string) (resultErr error) {
	if !identifier.MatchString(epoch) {
		return ErrInvalid
	}
	defer func() {
		if resultErr != nil {
			r.notifyGrants(GrantRevocation{StorageFailed: true})
		}
	}()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := r.stateTx(ctx, tx)
	if err != nil {
		return err
	}
	if row.RuntimeEpoch == epoch {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspacekeys_instance SET runtime_epoch=? WHERE id=?`, epoch, r.cfg.InstanceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspacekeys_challenges`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	r.notifyGrants(GrantRevocation{})
	return nil
}

func validLabel(label string) bool {
	return utf8.ValidString(label) && strings.TrimSpace(label) != "" && len(label) <= 128 && !strings.ContainsAny(label, "\x00\r\n")
}
