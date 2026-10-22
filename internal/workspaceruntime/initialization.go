package workspaceruntime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
)

// Initialization observes the original initialized backing and its latest
// recorded reopen. It never initializes, mounts or starts anything.
func (c *Controller) Initialization(ctx context.Context, id string) (Operation, error) {
	if err := c.acquire(ctx); err != nil {
		return Operation{}, err
	}
	defer c.release()
	return c.initialization(ctx, id)
}

func (c *Controller) initialization(ctx context.Context, id string) (Operation, error) {
	if !identifier.MatchString(id) {
		return Operation{}, ErrPrecondition
	}
	var kind, status, stage, digest string
	err := c.db.QueryRowContext(ctx, `SELECT kind,status,stage,config_digest FROM operations WHERE id=?`, id).Scan(&kind, &status, &stage, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, ErrJournal
	}
	if kind != "initialize" || status != "succeeded" || stage != "complete" || digest == "" || digest != c.configDigest() {
		return Operation{}, ErrUnsafeStorage
	}
	var child string
	err = c.db.QueryRowContext(ctx, `SELECT reopen_id FROM initialization_reopens WHERE initialization_id=?`, id).Scan(&child)
	if errors.Is(err, sql.ErrNoRows) {
		return c.operation(ctx, id)
	}
	if err != nil || !identifier.MatchString(child) || child == id {
		return Operation{}, ErrJournal
	}
	var childKind, childDigest string
	err = c.db.QueryRowContext(ctx, `SELECT kind,config_digest FROM operations WHERE id=?`, child).Scan(&childKind, &childDigest)
	if errors.Is(err, sql.ErrNoRows) {
		// The relation commits before the unlock journal. Its absence proves no
		// command was dispatched under that child ID; a fresh resume may do so.
		state, err := c.observe(ctx)
		return Operation{ID: child, Kind: "unlock", Status: "uncertain", Stage: "prepared", State: state}, err
	}
	if err != nil || childKind != "unlock" || childDigest != digest {
		return Operation{}, ErrUnsafeStorage
	}
	return c.operation(ctx, child)
}

// ReopenInitialization requires fresh resume authority from the operation
// service. It can only unlock the exact backing pinned by a completed native
// initialization. Reusing a child ID observes uncertain effects without replay.
func (c *Controller) ReopenInitialization(ctx context.Context, id string, secret []byte) (Operation, error) {
	if err := c.acquire(ctx); err != nil {
		return Operation{}, err
	}
	defer c.release()
	if len(secret) != 32 {
		return Operation{}, ErrPrecondition
	}
	mount, err := c.initialization(ctx, id)
	if err != nil {
		return mount, err
	}
	if mount.State.Storage == "open" {
		return mount, nil
	}
	if !mount.State.Locked || mount.State.Epoch == "" {
		return mount, ErrPrecondition
	}
	digest := sha256.Sum256([]byte("deck.runtime.reopen.v1\x00" + id + "\x00" + mount.State.Epoch))
	child := "reopen_" + base64.RawURLEncoding.EncodeToString(digest[:])
	if _, err := c.db.ExecContext(ctx, `INSERT INTO initialization_reopens(initialization_id,reopen_id) VALUES(?,?) ON CONFLICT(initialization_id) DO UPDATE SET reopen_id=excluded.reopen_id`, id, child); err != nil {
		return Operation{}, ErrJournal
	}
	return c.executeLocked(ctx, child, "unlock", secret)
}
