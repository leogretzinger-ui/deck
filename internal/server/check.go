package server

import (
	"context"
	"sync"
	"time"
)

// NewCachedCheck bounds an expensive readiness probe to one in-flight run and
// one run per freshness window. Successes and failures are cached alike, and a
// stale result is served while a single recheck runs in the background:
// readiness never waits on a provider CLI inside its request budget, and a
// genuinely broken provider still flips readiness within one completed recheck
// — fail-closed means never inventing a result, not blocking on a new one. The
// probe has its own timeout so a client disconnect cannot cancel shared work
// or poison the cached result for every other readiness caller.
func NewCachedCheck(name string, freshness, timeout time.Duration,
	probe func(context.Context) error) Check {
	c := newCachedValue(freshness, timeout, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, probe(ctx)
	})
	return Check{Name: name, Probe: func(ctx context.Context) error {
		_, err := c.get(ctx)
		return err
	}}
}

type cachedValue[T any] struct {
	mu        sync.Mutex
	freshness time.Duration
	timeout   time.Duration
	load      func(context.Context) (T, error)
	now       func() time.Time
	// gate bounds how many cached values probe at once. Nil means ungated.
	gate chan struct{}

	checkedAt time.Time
	value     T
	err       error
	hasResult bool
	inFlight  chan struct{}
}

func newCachedValue[T any](freshness, timeout time.Duration,
	load func(context.Context) (T, error)) *cachedValue[T] {
	return newGatedCachedValue(freshness, timeout, nil, load)
}

func newGatedCachedValue[T any](freshness, timeout time.Duration, gate chan struct{},
	load func(context.Context) (T, error)) *cachedValue[T] {
	return &cachedValue[T]{
		freshness: freshness,
		timeout:   timeout,
		load:      load,
		now:       time.Now,
		gate:      gate,
	}
}

func (c *cachedValue[T]) get(ctx context.Context) (T, error) {
	c.mu.Lock()
	now := c.now()
	if c.hasResult && now.Sub(c.checkedAt) < c.freshness {
		value, err := c.value, c.err
		c.mu.Unlock()
		return value, err
	}
	done := c.inFlight
	if done == nil {
		done = make(chan struct{})
		c.inFlight = done
		go c.execute(done)
	}
	if c.hasResult {
		// Stale-while-revalidate: serve the last completed result — failure
		// included — while one coalesced recheck runs on its own budget. A
		// caller's context can never outwait a provider CLI, and the recheck
		// still lands within freshness + one probe duration.
		value, err := c.value, c.err
		c.mu.Unlock()
		return value, err
	}
	c.mu.Unlock()

	// No completed result exists yet (cold start before priming lands): the
	// only honest answer comes from the in-flight probe, bounded by the
	// caller's context.
	select {
	case <-done:
		c.mu.Lock()
		value, err := c.value, c.err
		c.mu.Unlock()
		return value, err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

func (c *cachedValue[T]) execute(done chan struct{}) {
	// A queued probe waits for its slot outside the timeout budget: the
	// budget bounds the probe's own commands. Charging the fleet's queue
	// against it would let slow slot holders turn an unstarted probe into a
	// cached deadline failure.
	if c.gate != nil {
		c.gate <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	value, err := c.load(ctx)
	cancel()
	if c.gate != nil {
		<-c.gate
	}

	c.mu.Lock()
	c.value = value
	c.err = err
	c.checkedAt = c.now()
	c.hasResult = true
	c.inFlight = nil
	close(done)
	c.mu.Unlock()
}
