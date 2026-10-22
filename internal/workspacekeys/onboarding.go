package workspacekeys

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/noisefloor-oss/deck/internal/auth"
)

type EncryptionPolicy string

const (
	EncryptionRequired    EncryptionPolicy = "required"
	EncryptionOptional    EncryptionPolicy = "optional"
	EncryptionUnavailable EncryptionPolicy = "unavailable"
)

type Protection string

const (
	ProtectionUndecided Protection = "undecided"
	ProtectionEncrypted Protection = "encrypted"
	ProtectionNone      Protection = "none"
)

// OnboardingState is safe before sign-in. Completion records finished setup,
// not a running process or permission to read workspace data.
type OnboardingState struct {
	Policy        EncryptionPolicy `json:"policy"`
	Protection    Protection       `json:"protection"`
	SetupComplete bool             `json:"setupComplete"`
	Started       bool             `json:"started"`
}

// PlainStartIntent is a durable receipt for observation, never execution authority.
type PlainStartIntent struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// PlainStartPermit is issued only after the pinned owner signs in to an
// explicitly unencrypted installation. Its private binding cannot be decoded
// from a browser request or reconstructed from a durable intent.
type PlainStartPermit struct {
	registry *Registry
	id       string
	owner    string
	login    auth.Login
	expires  time.Time
}

func (p PlainStartPermit) ID() string     { return p.id }
func (PlainStartPermit) String() string   { return "workspacekeys.PlainStartPermit" }
func (PlainStartPermit) GoString() string { return "workspacekeys.PlainStartPermit" }

const onboardingSchema = `
CREATE TABLE IF NOT EXISTS workspacekeys_onboarding (
 id TEXT PRIMARY KEY, policy TEXT NOT NULL CHECK(policy IN ('required','optional','unavailable')),
 protection TEXT NOT NULL CHECK(protection IN ('undecided','encrypted','none')),
 setup_complete INTEGER NOT NULL DEFAULT 0, plain_operation TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS workspacekeys_plain_operations (
 id TEXT PRIMARY KEY, status TEXT NOT NULL CHECK(status IN ('pending','succeeded','failed'))
);`

func (r *Registry) openOnboardingTx(ctx context.Context, tx *sql.Tx, startup, fresh bool) error {
	if startup {
		if _, err := tx.ExecContext(ctx, onboardingSchema); err != nil {
			return err
		}
		// A registry predating this table always required encryption. An upgrade
		// cannot reinterpret absent policy metadata as permission to skip it.
		policy := EncryptionRequired
		if fresh {
			policy = r.cfg.EncryptionPolicy
		}
		var encrypted bool
		if err := tx.QueryRowContext(ctx, `SELECT commitment<>'' OR bootstrap_closed<>0 OR initializing_operation<>'' OR EXISTS(SELECT 1 FROM workspacekeys_methods) OR EXISTS(SELECT 1 FROM workspacekeys_operations) FROM workspacekeys_instance WHERE id=?`, r.cfg.InstanceID).Scan(&encrypted); err != nil {
			return err
		}
		protection := ProtectionUndecided
		if encrypted {
			protection = ProtectionEncrypted
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workspacekeys_onboarding(id,policy,protection) VALUES(?,?,?)`, r.cfg.InstanceID, policy, protection); err != nil {
			return err
		}
	}
	state, _, err := r.onboardingTx(ctx, tx)
	if err != nil {
		return err
	}
	if state.Policy != r.cfg.EncryptionPolicy {
		return ErrConflict
	}
	return nil
}

func (r *Registry) onboardingTx(ctx context.Context, tx *sql.Tx) (OnboardingState, string, error) {
	var state OnboardingState
	var operation string
	err := tx.QueryRowContext(ctx, `SELECT o.policy,o.protection,CASE WHEN o.protection='encrypted' THEN i.bootstrap_closed ELSE o.setup_complete END,o.plain_operation,i.owner<>'' FROM workspacekeys_onboarding o JOIN workspacekeys_instance i ON i.id=o.id WHERE o.id=?`, r.cfg.InstanceID).
		Scan(&state.Policy, &state.Protection, &state.SetupComplete, &operation, &state.Started)
	return state, operation, err
}

func (r *Registry) Onboarding(ctx context.Context) (OnboardingState, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return OnboardingState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	state, _, err := r.onboardingTx(ctx, tx)
	return state, err
}

// selectEncryptedTx is shared by explicit selection and older clients' first
// method preparation, so a concurrent unencrypted choice cannot win afterward.
func (r *Registry) selectEncryptedTx(ctx context.Context, tx *sql.Tx) error {
	state, _, err := r.onboardingTx(ctx, tx)
	if err != nil {
		return err
	}
	if state.Protection == ProtectionNone || state.Policy == EncryptionUnavailable {
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE workspacekeys_onboarding SET protection='encrypted' WHERE id=?`, r.cfg.InstanceID)
	return err
}

func (r *Registry) SelectProtection(ctx context.Context, actor Actor, protection Protection) (OnboardingState, error) {
	if protection != ProtectionEncrypted && protection != ProtectionNone {
		return OnboardingState{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return OnboardingState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := r.actorTx(ctx, tx, actor)
	if err != nil {
		return OnboardingState{}, err
	}
	if _, err := r.requireReceiptTx(ctx, tx, actor, row); err != nil {
		return OnboardingState{}, err
	}
	state, _, err := r.onboardingTx(ctx, tx)
	if err != nil {
		return OnboardingState{}, err
	}
	if state.Protection != ProtectionUndecided {
		return OnboardingState{}, ErrConflict
	}
	if protection == ProtectionEncrypted {
		err = r.selectEncryptedTx(ctx, tx)
	} else {
		var occupied bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspacekeys_methods) OR EXISTS(SELECT 1 FROM workspacekeys_operations) OR EXISTS(SELECT 1 FROM workspacekeys_challenges)`).Scan(&occupied)
		if err == nil && (state.Policy == EncryptionRequired || occupied || row.Commitment != "" || row.InitializingOperation != "" || row.BootstrapClosed) {
			err = ErrConflict
		}
		if err == nil {
			id := randomToken()
			_, err = tx.ExecContext(ctx, `INSERT INTO workspacekeys_plain_operations(id,status) VALUES(?,'pending')`, id)
			if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE workspacekeys_onboarding SET protection='none',plain_operation=? WHERE id=?`, id, r.cfg.InstanceID)
			}
			if err == nil {
				_, err = tx.ExecContext(ctx, `DELETE FROM workspacekeys_receipts`)
			}
		}
	}
	if err != nil {
		return OnboardingState{}, err
	}
	state.Protection = protection
	return state, tx.Commit()
}

func (r *Registry) plainOwnerTx(ctx context.Context, tx *sql.Tx, login auth.Login) (instance, string, error) {
	row, err := r.actorTx(ctx, tx, NewActor(login, ""))
	if err != nil {
		return row, "", err
	}
	state, id, err := r.onboardingTx(ctx, tx)
	if err == nil && (state.Policy == EncryptionRequired || state.Protection != ProtectionNone || id == "" || row.Initialized || row.Commitment != "" || row.InitializingOperation != "") {
		err = ErrDenied
	}
	return row, id, err
}

func plainIntentTx(ctx context.Context, tx *sql.Tx, id string) (PlainStartIntent, error) {
	intent := PlainStartIntent{ID: id}
	err := tx.QueryRowContext(ctx, `SELECT status FROM workspacekeys_plain_operations WHERE id=?`, id).Scan(&intent.Status)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return intent, err
}

// PlainOperation binds observation to the pinned owner, but permits inspecting
// an older operation after another start has been reserved.
func (r *Registry) PlainOperation(ctx context.Context, login auth.Login, id string) (PlainStartIntent, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PlainStartIntent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, _, err := r.plainOwnerTx(ctx, tx, login); err != nil {
		return PlainStartIntent{}, err
	}
	return plainIntentTx(ctx, tx, id)
}

// CurrentPlainStart is for trusted controller reconciliation after a restart.
// The returned public record cannot authorize starting the runtime.
func (r *Registry) CurrentPlainStart(ctx context.Context) (PlainStartIntent, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PlainStartIntent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	state, id, err := r.onboardingTx(ctx, tx)
	if err != nil {
		return PlainStartIntent{}, err
	}
	if state.Protection != ProtectionNone || id == "" {
		return PlainStartIntent{}, ErrNotFound
	}
	return plainIntentTx(ctx, tx, id)
}

func (r *Registry) AuthorizePlainStart(ctx context.Context, login auth.Login, id string) (PlainStartPermit, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PlainStartPermit{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, current, err := r.plainOwnerTx(ctx, tx, login)
	if err != nil {
		return PlainStartPermit{}, err
	}
	if current != id {
		return PlainStartPermit{}, ErrConflict
	}
	intent, err := plainIntentTx(ctx, tx, id)
	if err != nil {
		return PlainStartPermit{}, err
	}
	if intent.Status != "pending" {
		return PlainStartPermit{}, ErrConflict
	}
	return PlainStartPermit{registry: r, id: id, owner: row.owner, login: login, expires: r.cfg.Now().Add(5 * time.Minute)}, nil
}

func (r *Registry) validatePlainStartTx(ctx context.Context, tx *sql.Tx, login auth.Login, permit PlainStartPermit) error {
	if permit.registry != r || permit.login != login || !r.cfg.Now().Before(permit.expires) {
		return ErrDenied
	}
	row, current, err := r.plainOwnerTx(ctx, tx, login)
	if err != nil {
		return err
	}
	if row.owner != permit.owner || current != permit.id {
		return ErrDenied
	}
	intent, err := plainIntentTx(ctx, tx, permit.id)
	if err == nil && intent.Status != "pending" {
		err = ErrConflict
	}
	return err
}

func (r *Registry) ValidatePlainStart(ctx context.Context, login auth.Login, permit PlainStartPermit) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return r.validatePlainStartTx(ctx, tx, login, permit)
}

// ReservePlainStart is a trusted service operation: call only after native
// observation confirms stopped with no unresolved effects. Requiring the exact
// previous terminal receipt prevents concurrent starts from replacing an intent.
func (r *Registry) ReservePlainStart(ctx context.Context, login auth.Login, previousID string) (PlainStartPermit, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PlainStartPermit{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, current, err := r.plainOwnerTx(ctx, tx, login)
	if err != nil {
		return PlainStartPermit{}, err
	}
	if current != previousID {
		return PlainStartPermit{}, ErrConflict
	}
	intent, err := plainIntentTx(ctx, tx, current)
	if err != nil {
		return PlainStartPermit{}, err
	}
	if intent.Status == "pending" {
		return PlainStartPermit{}, ErrConflict
	}
	id := randomToken()
	if _, err = tx.ExecContext(ctx, `INSERT INTO workspacekeys_plain_operations(id,status) VALUES(?,'pending')`, id); err != nil {
		return PlainStartPermit{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workspacekeys_onboarding SET plain_operation=? WHERE id=?`, id, r.cfg.InstanceID); err != nil {
		return PlainStartPermit{}, err
	}
	permit := PlainStartPermit{registry: r, id: id, owner: row.owner, login: login, expires: r.cfg.Now().Add(5 * time.Minute)}
	return permit, tx.Commit()
}

// CompletePlainStart follows positive native observation of the expected
// available runtime. It records setup completion, never encrypted initialization.
func (r *Registry) CompletePlainStart(ctx context.Context, permit PlainStartPermit) error {
	return r.finishPlainStart(ctx, permit, true)
}

// FailPlainStart is allowed only after native observation establishes that no
// effects remain. An ambiguous operation stays pending and is observed, not retried.
func (r *Registry) FailPlainStart(ctx context.Context, permit PlainStartPermit) error {
	return r.finishPlainStart(ctx, permit, false)
}

func (r *Registry) finishPlainStart(ctx context.Context, permit PlainStartPermit, success bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.validatePlainStartTx(ctx, tx, permit.login, permit); err != nil {
		return err
	}
	status := "failed"
	if success {
		status = "succeeded"
		if _, err = tx.ExecContext(ctx, `UPDATE workspacekeys_onboarding SET setup_complete=1 WHERE id=?`, r.cfg.InstanceID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workspacekeys_plain_operations SET status=? WHERE id=?`, status, permit.id); err != nil {
		return err
	}
	return tx.Commit()
}
