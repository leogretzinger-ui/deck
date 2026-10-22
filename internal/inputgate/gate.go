// Package inputgate serializes the single writer that puts bytes into a
// session's terminal: raw input, paste, and terminal-emulator
// replies all share ONE serialized writer to tmux. The terminal proxy
// holds the session gate across [revalidate lease -> write bytes], so a
// fenced or superseded controller cannot slip a write past the fencing
// after another writer has already committed.
//
// A process-local mutex is sufficient because exactly one backend may run
// (the exclusive lock); there is no second process to serialize
// against.
package inputgate

import (
	"context"
	"errors"
	"sync"
)

// Gate hands out one State per session.
type Gate struct {
	mu      sync.Mutex
	states  map[string]*State
	fenced  bool
	active  int
	drained chan struct{}
}

// State is one session's write lock.
type State struct {
	mu sync.Mutex
}

// New builds an empty gate.
func New() *Gate { return &Gate{states: make(map[string]*State)} }

// Session returns the state for one session, creating it on first use.
// Entries are never removed: a session's lifetime spans the process, and a
// mutex is negligible — eviction would only add a way to hand two callers
// different locks for the same session.
func (g *Gate) Session(id string) *State {
	g.mu.Lock()
	defer g.mu.Unlock()
	s, ok := g.states[id]
	if !ok {
		s = &State{}
		g.states[id] = s
	}
	return s
}

// Lock takes the session's write gate. Every writer that puts bytes into
// the terminal must hold it across [revalidate lease -> write bytes], and
// must take it before any database write (one lock order everywhere:
// gate, then database).
func (s *State) Lock() { s.mu.Lock() }

// Unlock releases the write gate.
func (s *State) Unlock() { s.mu.Unlock() }

// ErrWorkspaceLocking refuses new terminal writes and session launches once
// explicit workspace locking begins. The fence lasts for this backend lifetime.
var ErrWorkspaceLocking = errors.New("workspace is locking")

// Admit reserves one mutation before its session gate is acquired. The caller
// must release it after the native effect and its recorded outcome settle.
func (g *Gate) Admit() (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fenced {
		return nil, ErrWorkspaceLocking
	}
	g.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.active--
			if g.fenced && g.active == 0 {
				close(g.drained)
			}
		})
	}, nil
}

// FenceAndDrain closes admission before waiting for admitted mutations. A
// timeout leaves admission closed; it never turns an interrupted lock into
// permission to launch another CLI or write to an existing terminal.
func (g *Gate) FenceAndDrain(ctx context.Context) error {
	g.mu.Lock()
	if !g.fenced {
		g.fenced = true
		g.drained = make(chan struct{})
		if g.active == 0 {
			close(g.drained)
		}
	}
	drained := g.drained
	g.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-drained:
		return ctx.Err()
	}
}
