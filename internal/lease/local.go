package lease

import (
	"context"
	"sync"
	"time"
)

// Local keeps leases in memory for terminal targets without a session row.
// It uses the same transitions as Manager; a restart forgets these leases.
type Local struct {
	mu     sync.Mutex
	ttl    time.Duration
	now    func() time.Time
	leases map[string]Lease
}

// NewLocal returns an empty ephemeral store with the standard TTL.
func NewLocal() *Local {
	return &Local{ttl: TTL, now: time.Now, leases: make(map[string]Lease)}
}

// Acquire follows Manager.Acquire without persisting the lease.
func (s *Local) Acquire(ctx context.Context, sessionID, controllerID string, deliberate bool) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t, err := decideAcquire(s.current(sessionID), sessionID, controllerID, deliberate, s.now(), s.ttl)
	if err != nil {
		return nil, err
	}
	s.leases[sessionID] = t.next
	return s.copyOf(sessionID), nil
}

// Heartbeat follows Manager.Heartbeat without persisting the lease.
func (s *Local) Heartbeat(ctx context.Context, sessionID, controllerID string, token int64) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t, err := decideHeartbeat(s.current(sessionID), controllerID, token, s.now(), s.ttl)
	if err != nil {
		return nil, err
	}
	s.leases[sessionID] = t.next
	return s.copyOf(sessionID), nil
}

// Validate checks the current holder, token, and expiry.
func (s *Local) Validate(ctx context.Context, sessionID, controllerID string, token int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return decideValidate(s.current(sessionID), controllerID, token, s.now())
}

// Get reads the current lease.
func (s *Local) Get(ctx context.Context, sessionID string) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.current(sessionID) == nil {
		return nil, ErrNotFound
	}
	return s.copyOf(sessionID), nil
}

// current returns the recorded lease for sessionID, or nil when none is
// recorded. The caller holds the mutex.
func (s *Local) current(sessionID string) *Lease {
	l, ok := s.leases[sessionID]
	if !ok {
		return nil
	}
	return &l
}

// copyOf returns a value copy so callers cannot mutate the stored lease.
// The caller holds the mutex.
func (s *Local) copyOf(sessionID string) *Lease {
	l := s.leases[sessionID]
	return &l
}
