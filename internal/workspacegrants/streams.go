package workspacegrants

import (
	"context"
	"sync"
	"time"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
)

type streamGroup struct {
	owner, login string
	method       workspacekeys.MethodRef
	expires      time.Time
	timer        *time.Timer
	callbacks    map[uint64]func()
}

// Attach admits a stream against current database authority. closeStream must
// promptly close its transport or cancel its context; it must not await this call.
// The returned release removes an ordinarily closed stream without closing it again.
func (s *Store) Attach(ctx context.Context, login auth.Login, closeStream func()) (func(), error) {
	if closeStream == nil {
		return nil, ErrDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrDenied
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.grantTx(ctx, tx, login)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() || s.fault.Load() || !current.expires.After(s.cfg.Now()) {
		return nil, ErrDenied
	}
	group := s.streams[current.hash]
	if group == nil {
		group = &streamGroup{owner: current.owner, login: current.login, method: current.method, expires: current.expires, callbacks: make(map[uint64]func())}
		s.streams[current.hash] = group
		group.timer = time.AfterFunc(current.expires.Sub(s.cfg.Now()), func() { s.expire(current.hash) })
	}
	if len(group.callbacks) >= 256 {
		return nil, ErrDenied
	}
	s.next++
	id := s.next
	group.callbacks[id] = closeStream
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if group := s.streams[current.hash]; group != nil {
				delete(group.callbacks, id)
				if len(group.callbacks) == 0 {
					group.timer.Stop()
					delete(s.streams, current.hash)
				}
			}
		})
	}, nil
}

func closeStreams(callbacks []func()) {
	for _, closeStream := range callbacks {
		closeStream()
	}
}
func (s *Store) detachLocked(match func(*streamGroup) bool) []func() {
	var callbacks []func()
	for hash, group := range s.streams {
		if match(group) {
			group.timer.Stop()
			for _, closeStream := range group.callbacks {
				callbacks = append(callbacks, closeStream)
			}
			delete(s.streams, hash)
		}
	}
	return callbacks
}
func (s *Store) failClosed() {
	s.fault.Store(true)
	s.mu.Lock()
	callbacks := s.detachLocked(func(*streamGroup) bool { return true })
	s.mu.Unlock()
	closeStreams(callbacks)
}

func (s *Store) expire(hash string) {
	s.mu.Lock()
	group := s.streams[hash]
	if group == nil {
		s.mu.Unlock()
		return
	}
	if delay := group.expires.Sub(s.cfg.Now()); delay > 0 {
		group.timer = time.AfterFunc(delay, func() { s.expire(hash) })
		s.mu.Unlock()
		return
	}
	group.timer.Stop()
	var callbacks []func()
	for _, closeStream := range group.callbacks {
		callbacks = append(callbacks, closeStream)
	}
	delete(s.streams, hash)
	s.mu.Unlock()
	// Expiry is already authoritative; a busy database cannot extend a stream.
	closeStreams(callbacks)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM workspacegrants_grants WHERE hash=? AND instance=? AND expires<=?`, hash, s.cfg.InstanceID, s.cfg.Now().UnixMilli()); err != nil {
		s.failClosed()
	}
}

// Sweep prunes expired bearers; attached streams also have independent deadlines,
// so expiry does not depend on another HTTP request or a periodic caller.
func (s *Store) Sweep(ctx context.Context) error {
	s.mu.Lock()
	callbacks := s.detachLocked(func(g *streamGroup) bool { return !g.expires.After(s.cfg.Now()) })
	s.mu.Unlock()
	closeStreams(callbacks)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM workspacegrants_grants WHERE instance=? AND expires<=?`, s.cfg.InstanceID, s.cfg.Now().UnixMilli()); err != nil {
		s.failClosed()
		return err
	}
	return nil
}
