package workspacehost

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
)

// plainAccess uses the pinned Google identity only after the owner explicitly
// completed setup without Deck encryption. It never supplies an unlock grant.
type plainAccess struct {
	db        *sql.DB
	authorize func(context.Context, auth.Login) error
	mu        sync.Mutex
	closed    bool
	ready     bool
	revoked   map[string]bool
	next      uint64
	streams   map[uint64]*plainStream
}

type plainStream struct {
	login string
	timer *time.Timer
	close func()
}

func newPlainAccess(ctx context.Context, db *sql.DB, authorize func(context.Context, auth.Login) error) (*plainAccess, error) {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS workspace_plain_logouts (login_id TEXT PRIMARY KEY)`); err != nil {
		return nil, err
	}
	return &plainAccess{db: db, authorize: authorize, revoked: make(map[string]bool), streams: make(map[uint64]*plainStream)}, nil
}

func (a *plainAccess) require(ctx context.Context, login auth.Login) error {
	if err := a.authorize(ctx, login); err != nil {
		if !errors.Is(err, workspacekeys.ErrDenied) && !canceledRead(ctx, err) {
			a.Close()
		}
		return ErrUnavailable
	}
	return a.admitLogin(ctx, login)
}

func (a *plainAccess) admitLogin(ctx context.Context, login auth.Login) error {
	expires, ok := auth.SessionExpires(ctx)
	if !ok || !expires.After(time.Now()) {
		return ErrUnavailable
	}
	var revoked int
	if err := a.db.QueryRowContext(ctx, `SELECT count(*) FROM workspace_plain_logouts WHERE login_id=?`, login.ID).Scan(&revoked); err != nil {
		if !canceledRead(ctx, err) {
			a.Close()
		}
		return ErrUnavailable
	}
	if revoked != 0 {
		return ErrUnavailable
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.revoked[login.ID] {
		return ErrUnavailable
	}
	return nil
}

func (a *plainAccess) Require(ctx context.Context, login auth.Login) error {
	if err := a.require(ctx, login); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || !a.ready || a.revoked[login.ID] {
		return ErrUnavailable
	}
	return nil
}

func (a *plainAccess) Attach(ctx context.Context, login auth.Login, closeStream func()) (func(), error) {
	if closeStream == nil {
		return nil, ErrUnavailable
	}
	if err := a.require(ctx, login); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	expires, _ := auth.SessionExpires(ctx)
	if a.closed || !a.ready || a.revoked[login.ID] || !expires.After(time.Now()) {
		return nil, ErrUnavailable
	}
	count := 0
	for _, stream := range a.streams {
		if stream.login == login.ID {
			count++
		}
	}
	if count >= 256 {
		return nil, ErrUnavailable
	}
	a.next++
	id := a.next
	stream := &plainStream{login: login.ID, close: closeStream}
	a.streams[id] = stream
	stream.timer = time.AfterFunc(time.Until(expires), func() { a.expire(id) })
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			if stream := a.streams[id]; stream != nil {
				stream.timer.Stop()
				delete(a.streams, id)
			}
		})
	}, nil
}

func (a *plainAccess) expire(id uint64) {
	a.mu.Lock()
	stream := a.streams[id]
	delete(a.streams, id)
	a.mu.Unlock()
	if stream != nil {
		stream.close()
	}
}

func (a *plainAccess) detach(login string) []func() {
	var callbacks []func()
	for id, stream := range a.streams {
		if login == "" || stream.login == login {
			stream.timer.Stop()
			callbacks = append(callbacks, stream.close)
			delete(a.streams, id)
		}
	}
	return callbacks
}

func (a *plainAccess) Logout(ctx context.Context, login auth.Login) error {
	a.mu.Lock()
	a.revoked[login.ID] = true
	callbacks := a.detach(login.ID)
	a.mu.Unlock()
	for _, closeStream := range callbacks {
		closeStream()
	}
	_, err := a.db.ExecContext(ctx, `INSERT OR IGNORE INTO workspace_plain_logouts(login_id) VALUES(?)`, login.ID)
	if err != nil {
		a.Close()
	}
	return err
}

func (a *plainAccess) reconcile(ready, changed bool) {
	a.mu.Lock()
	a.ready = ready
	var callbacks []func()
	if !ready || changed {
		callbacks = a.detach("")
	}
	a.mu.Unlock()
	for _, closeStream := range callbacks {
		closeStream()
	}
}

func (a *plainAccess) Close() {
	a.mu.Lock()
	a.closed = true
	callbacks := a.detach("")
	a.mu.Unlock()
	for _, closeStream := range callbacks {
		closeStream()
	}
}
