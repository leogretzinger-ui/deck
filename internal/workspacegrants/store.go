// Package workspacegrants owns opaque browser access and the streams admitted by it.
package workspacegrants

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
)

var ErrDenied = errors.New("workspace browser access refused")

const Lifetime = 12 * time.Hour

type Config struct {
	InstanceID string
	Now        func() time.Time
}
type Store struct {
	db      *sql.DB
	cfg     Config
	keys    atomic.Pointer[workspacekeys.Registry]
	closed  atomic.Bool
	fault   atomic.Bool
	mu      sync.Mutex
	streams map[string]*streamGroup
	next    uint64
}

type Delivery struct {
	ExpiresAt time.Time
	token     string
}

func (d Delivery) Token() string  { return d.token }
func (Delivery) String() string   { return "workspacegrants.Delivery" }
func (Delivery) GoString() string { return "workspacegrants.Delivery" }
func (Delivery) MarshalJSON() ([]byte, error) {
	return nil, errors.New("browser bearer cannot be serialized")
}

const schema = `CREATE TABLE IF NOT EXISTS workspacegrants_grants (
 hash TEXT PRIMARY KEY, instance TEXT NOT NULL, owner TEXT NOT NULL, login_id TEXT NOT NULL,
 method_id TEXT NOT NULL, method_revision INTEGER NOT NULL, epoch TEXT NOT NULL, expires INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS workspacegrants_login ON workspacegrants_grants(instance,owner,login_id);
CREATE TABLE IF NOT EXISTS workspacegrants_claims (
 operation_id TEXT PRIMARY KEY, instance TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS workspacegrants_logouts (
 instance TEXT NOT NULL, login_id TEXT NOT NULL, PRIMARY KEY(instance,login_id)
);`

// New starts a gateway generation: previous browser bearers are discarded.
// Registry.New separately discards pending challenges. Neither stops workloads.
func New(ctx context.Context, db *sql.DB, cfg Config) (*Store, error) {
	if db == nil || cfg.InstanceID == "" {
		return nil, ErrDenied
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, schema); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM workspacegrants_grants WHERE instance=?`, cfg.InstanceID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &Store{db: db, cfg: cfg, streams: make(map[string]*streamGroup)}, nil
}

// BindRegistry connects the same database and instance before handlers start.
func (s *Store) BindRegistry(keys *workspacekeys.Registry) error {
	if keys == nil || s.keys.Load() != nil || s.closed.Load() {
		return ErrDenied
	}
	if err := keys.BindGrantRevocations(s.db, s.cfg.InstanceID, s); err != nil {
		return err
	}
	s.keys.Store(keys)
	return nil
}

func hashToken(token string) string {
	if len(token) != 43 {
		return ""
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		clear(raw)
		return ""
	}
	hash := sha256.Sum256(raw)
	clear(raw)
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

type grant struct {
	hash, instance, owner, login, epoch string
	method                              workspacekeys.MethodRef
	expires                             time.Time
}

func (s *Store) grantTx(ctx context.Context, tx *sql.Tx, login auth.Login) (grant, error) {
	keys := s.keys.Load()
	if keys == nil || s.closed.Load() || s.fault.Load() {
		return grant{}, ErrDenied
	}
	hash := contextHash(ctx)
	if hash == "" {
		return grant{}, ErrDenied
	}
	result := grant{hash: hash}
	var expires int64
	err := tx.QueryRowContext(ctx, `SELECT instance,owner,login_id,method_id,method_revision,epoch,expires FROM workspacegrants_grants WHERE hash=?`, hash).Scan(&result.instance, &result.owner, &result.login, &result.method.ID, &result.method.Revision, &result.epoch, &expires)
	if err != nil {
		return grant{}, ErrDenied
	}
	result.expires = time.UnixMilli(expires)
	if result.instance != s.cfg.InstanceID || result.login != login.ID || !result.expires.After(s.cfg.Now()) {
		return grant{}, ErrDenied
	}
	owner, err := keys.ValidateGrantMethodTx(ctx, tx, login, result.instance, result.epoch, result.method)
	if err != nil || owner != result.owner {
		return grant{}, ErrDenied
	}
	return result, nil
}

// Issue requires a fresh registry proof whose native operation, if any, completed.
// The calling service must separately observe the runtime running before issuance.
func (s *Store) Issue(ctx context.Context, login auth.Login, permit workspacekeys.AuthorizedOperation) (Delivery, error) {
	keys := s.keys.Load()
	if keys == nil || s.closed.Load() || s.fault.Load() {
		return Delivery{}, ErrDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Delivery{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var revoked int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM workspacegrants_logouts WHERE instance=? AND login_id=?`, s.cfg.InstanceID, login.ID).Scan(&revoked); err != nil || revoked != 0 {
		return Delivery{}, ErrDenied
	}
	authority, err := keys.ValidateBrowserOperationTx(ctx, tx, login, permit)
	if err != nil || authority.InstanceID != s.cfg.InstanceID {
		return Delivery{}, ErrDenied
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workspacegrants_claims(operation_id,instance) VALUES(?,?)`, authority.OperationID, s.cfg.InstanceID); err != nil {
		return Delivery{}, ErrDenied
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM workspacegrants_grants WHERE instance=? AND owner=? AND login_id=?`, s.cfg.InstanceID, authority.Owner, authority.LoginID); err != nil {
		return Delivery{}, err
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	clear(raw)
	expires := s.cfg.Now().Add(Lifetime).Truncate(time.Millisecond)
	if _, err = tx.ExecContext(ctx, `INSERT INTO workspacegrants_grants(hash,instance,owner,login_id,method_id,method_revision,epoch,expires) VALUES(?,?,?,?,?,?,?,?)`, hashToken(token), s.cfg.InstanceID, authority.Owner, authority.LoginID, authority.Method.ID, authority.Method.Revision, authority.RuntimeEpoch, expires.UnixMilli()); err != nil {
		return Delivery{}, err
	}
	// DB transactions precede the stream mutex everywhere, including registry preparation.
	s.mu.Lock()
	if s.closed.Load() || s.fault.Load() {
		s.mu.Unlock()
		return Delivery{}, ErrDenied
	}
	if err = tx.Commit(); err != nil {
		s.mu.Unlock()
		return Delivery{}, err
	}
	callbacks := s.detachLocked(func(g *streamGroup) bool { return g.owner == authority.Owner && g.login == authority.LoginID })
	s.mu.Unlock()
	closeStreams(callbacks)
	return Delivery{ExpiresAt: expires, token: token}, nil
}

func (s *Store) Require(ctx context.Context, login auth.Login) error {
	if s.closed.Load() || s.fault.Load() || s.keys.Load() == nil {
		return ErrDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrDenied
	}
	defer func() { _ = tx.Rollback() }()
	_, err = s.grantTx(ctx, tx, login)
	return err
}

// RequirePreparationTx implements workspacekeys.PreparationAuthorizer without
// reading browser-supplied identities or accepting a boolean authorization flag.
func (s *Store) RequirePreparationTx(ctx context.Context, tx *sql.Tx, login auth.Login, instanceID, epoch string) (workspacekeys.MethodRef, error) {
	current, err := s.grantTx(ctx, tx, login)
	if err != nil || current.instance != instanceID || current.epoch != epoch {
		return workspacekeys.MethodRef{}, ErrDenied
	}
	return current.method, nil
}

func (s *Store) Logout(ctx context.Context, login auth.Login) error {
	keys := s.keys.Load()
	if keys == nil {
		return ErrDenied
	}
	bound, err := keys.OwnerMatches(ctx, login)
	if err != nil || !bound {
		return ErrDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.failClosed()
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO workspacegrants_logouts(instance,login_id) VALUES(?,?)`, s.cfg.InstanceID, login.ID); err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM workspacegrants_grants WHERE instance=? AND login_id=?`, s.cfg.InstanceID, login.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		_ = tx.Rollback()
		s.failClosed()
		return err
	}
	s.mu.Lock()
	callbacks := s.detachLocked(func(g *streamGroup) bool { return g.login == login.ID })
	s.mu.Unlock()
	closeStreams(callbacks)
	return nil
}

// RevokeRegistryGrants runs synchronously after a registry mutation commits.
// A storage failure closes every stream and refuses new grants until restart.
func (s *Store) RevokeRegistryGrants(event workspacekeys.GrantRevocation) {
	if event.StorageFailed {
		s.failClosed()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.failClosed()
		return
	}
	defer func() { _ = tx.Rollback() }()
	if event.Method.ID == "" {
		err = s.fenceOperationsTx(ctx, tx, "")
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM workspacegrants_grants WHERE instance=? AND method_id=? AND method_revision=?`, s.cfg.InstanceID, event.Method.ID, event.Method.Revision)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		_ = tx.Rollback()
		s.failClosed()
		return
	}
	s.mu.Lock()
	callbacks := s.detachLocked(func(g *streamGroup) bool { return event.Method.ID == "" || g.method == event.Method })
	s.mu.Unlock()
	closeStreams(callbacks)
}

func (s *Store) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	s.mu.Lock()
	callbacks := s.detachLocked(func(*streamGroup) bool { return true })
	s.mu.Unlock()
	closeStreams(callbacks)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `DELETE FROM workspacegrants_grants WHERE instance=?`, s.cfg.InstanceID)
	return err
}
