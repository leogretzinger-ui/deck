// Package workspacegateway serves workspace security independently of the encrypted runtime.
package workspacegateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
)

var ErrInvitation = errors.New("workspace invitation unavailable")

// OpenControl opens the unencrypted control store. Workspace contents and raw
// encryption credentials never belong here; it must remain usable while locked.
func OpenControl(path string) (*sql.DB, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("workspace control store requires an absolute clean path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create workspace control directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open workspace control store: %w", err)
	}
	info, statErr := f.Stat()
	closeErr := f.Close()
	if statErr != nil || closeErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("workspace control store must be an owner-only regular file")
	}
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{"_foreign_keys": {"on"}, "_journal_mode": {"WAL"}, "_synchronous": {"FULL"}, "_busy_timeout": {"5000"}, "_txlock": {"immediate"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, errors.New("workspace control store unavailable")
	}
	return db, nil
}

type Invitations struct {
	db    *sql.DB
	keys  *workspacekeys.Registry
	email string
	now   func() time.Time
}

func NewInvitations(ctx context.Context, db *sql.DB, keys *workspacekeys.Registry, email string, now func() time.Time) (*Invitations, error) {
	if db == nil || keys == nil || !validEmail(email) {
		return nil, errors.New("invalid workspace invitation configuration")
	}
	if now == nil {
		now = time.Now
	}
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS workspace_invitation (
		id INTEGER PRIMARY KEY CHECK(id=1), hash BLOB NOT NULL, email TEXT NOT NULL,
		expires INTEGER NOT NULL, consumed INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		return nil, err
	}
	return &Invitations{db: db, keys: keys, email: email, now: now}, nil
}

func validEmail(email string) bool {
	return len(email) <= 254 && strings.Count(email, "@") == 1 &&
		!strings.ContainsAny(email, " \t\r\n\x00") && !strings.HasPrefix(email, "@") && !strings.HasSuffix(email, "@")
}

// Issue is a local admin action. A new invitation supersedes the previous
// unused link, but can never reset a bound owner or initialized workspace.
func (s *Invitations) Issue(ctx context.Context) (string, time.Time, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.keys.CanEnrollTx(ctx, tx); err != nil {
		return "", time.Time{}, ErrInvitation
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", time.Time{}, errors.New("invitation generation failed")
	}
	defer clear(secret)
	hash := sha256.Sum256(secret)
	expires := s.now().Add(72 * time.Hour)
	_, err = tx.ExecContext(ctx, `INSERT INTO workspace_invitation(id,hash,email,expires,consumed)
		VALUES(1,?,?,?,0) ON CONFLICT(id) DO UPDATE SET hash=excluded.hash,email=excluded.email,
		expires=excluded.expires,consumed=0`, hash[:], s.email, expires.UnixMilli())
	if err != nil {
		return "", time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, err
	}
	return base64.RawURLEncoding.EncodeToString(secret), expires, nil
}

func (s *Invitations) Redeem(ctx context.Context, login auth.Login, token string) (workspacekeys.EnrollmentReceipt, error) {
	if login.Issuer != "https://accounts.google.com" || !strings.EqualFold(login.Email, s.email) {
		return workspacekeys.EnrollmentReceipt{}, ErrInvitation
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(secret) != 32 || base64.RawURLEncoding.EncodeToString(secret) != token {
		return workspacekeys.EnrollmentReceipt{}, ErrInvitation
	}
	defer clear(secret)
	hash := sha256.Sum256(secret)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workspacekeys.EnrollmentReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE workspace_invitation SET consumed=1
		WHERE id=1 AND hash=? AND lower(email)=lower(?) AND expires>? AND consumed=0`, hash[:], login.Email, s.now().UnixMilli())
	if err != nil {
		return workspacekeys.EnrollmentReceipt{}, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return workspacekeys.EnrollmentReceipt{}, ErrInvitation
	}
	receipt, err := s.keys.CreateEnrollmentTx(ctx, tx, login)
	if err != nil {
		return workspacekeys.EnrollmentReceipt{}, ErrInvitation
	}
	if err := tx.Commit(); err != nil {
		return workspacekeys.EnrollmentReceipt{}, err
	}
	return receipt, nil
}
