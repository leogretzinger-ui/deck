package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCachedCheckCoalescesConcurrentAndRepeatedCalls(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	check := NewCachedCheck("provider-test", time.Minute, time.Second,
		func(context.Context) error {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-release
			return nil
		})

	const callers = 64
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- check.Probe(context.Background())
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("coalesced probe: %v", err)
		}
	}
	for range 100 {
		if err := check.Probe(context.Background()); err != nil {
			t.Fatalf("cached probe: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
}

func TestCachedCheckRefreshesStaleSuccessAndFailure(t *testing.T) {
	now := time.Unix(100, 0)
	wantErr := errors.New("provider unavailable")
	results := []error{wantErr, nil}
	var calls atomic.Int32
	c := newCachedValue(time.Minute, time.Second, func(context.Context) (struct{}, error) {
		result := results[calls.Add(1)-1]
		return struct{}{}, result
	})
	c.now = func() time.Time { return now }

	if _, err := c.get(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("initial result = %v, want %v", err, wantErr)
	}
	if _, err := c.get(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("fresh failure = %v, want cached %v", err, wantErr)
	}
	if calls.Load() != 1 {
		t.Fatalf("fresh failure reran probe: calls = %d", calls.Load())
	}
	// A stale result is served while one recheck runs in the background: the
	// caller never waits on the probe, and the recheck still lands.
	now = now.Add(time.Minute)
	if _, err := c.get(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("stale failure = %v, want cached %v while revalidating", err, wantErr)
	}
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != 2 {
		t.Fatalf("stale result did not revalidate in the background: calls = %d, want 2", calls.Load())
	}
	if _, err := c.get(context.Background()); err != nil {
		t.Fatalf("revalidated result = %v, want success", err)
	}
}

func TestCachedCheckTimesOutAndClearsInflightRun(t *testing.T) {
	now := time.Unix(100, 0)
	var calls atomic.Int32
	c := newCachedValue(time.Minute, 20*time.Millisecond, func(ctx context.Context) (struct{}, error) {
		calls.Add(1)
		<-ctx.Done()
		return struct{}{}, ctx.Err()
	})
	c.now = func() time.Time { return now }

	if _, err := c.get(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung probe = %v, want deadline exceeded", err)
	}
	c.mu.Lock()
	inFlight := c.inFlight
	c.mu.Unlock()
	if inFlight != nil {
		t.Fatal("timed-out probe retained in-flight state")
	}
	now = now.Add(time.Minute)
	if _, err := c.get(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stale hung probe = %v, want cached deadline exceeded while revalidating", err)
	}
	// The stale read kicked one background recheck; the hung probe times out
	// again on its own budget.
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("probe calls after stale retry = %d, want 2", got)
	}
}

func TestCachedCheckSurvivesCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	check := NewCachedCheck("provider-test", time.Minute, time.Second,
		func(context.Context) error {
			calls.Add(1)
			close(started)
			<-release
			return nil
		})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- check.Probe(ctx) }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller = %v, want context canceled", err)
	}
	close(release)
	if err := check.Probe(context.Background()); err != nil {
		t.Fatalf("shared probe after caller cancellation: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("caller cancellation restarted probe: calls = %d", got)
	}
}

func TestGatedCachedValueQueueWaitDoesNotConsumeProbeBudget(t *testing.T) {
	gate := make(chan struct{}, 1)
	holderStarted := make(chan struct{})
	releaseHolder := make(chan struct{})
	holder := newGatedCachedValue(time.Minute, 10*time.Second, gate,
		func(context.Context) (struct{}, error) {
			close(holderStarted)
			<-releaseHolder
			return struct{}{}, nil
		})
	holderDone := make(chan error, 1)
	go func() {
		_, err := holder.get(context.Background())
		holderDone <- err
	}()
	<-holderStarted

	var queuedStarted time.Time
	var queuedDeadline time.Time
	queued := newGatedCachedValue(time.Minute, 10*time.Second, gate,
		func(ctx context.Context) (struct{}, error) {
			queuedStarted = time.Now()
			queuedDeadline, _ = ctx.Deadline()
			return struct{}{}, nil
		})
	queuedDone := make(chan error, 1)
	go func() {
		_, err := queued.get(context.Background())
		queuedDone <- err
	}()

	// Hold the slot well past the point where a shared budget would
	// noticeably shrink the queued probe's timeout.
	time.Sleep(time.Second)
	close(releaseHolder)
	if err := <-holderDone; err != nil {
		t.Fatalf("slot holder: %v", err)
	}
	if err := <-queuedDone; err != nil {
		t.Fatalf("queued probe: %v", err)
	}
	if remaining := queuedDeadline.Sub(queuedStarted); remaining < 9*time.Second {
		t.Fatalf("queued probe budget = %s, want the full 10s after the queue wait", remaining)
	}
}

func TestCachedCheckServesStaleSuccessAndFlipsOnCompletedRecheck(t *testing.T) {
	now := time.Unix(100, 0)
	recheckStarted := make(chan struct{})
	releaseRecheck := make(chan struct{})
	var calls atomic.Int32
	c := newCachedValue(time.Minute, 10*time.Second, func(context.Context) (struct{}, error) {
		if calls.Add(1) == 1 {
			return struct{}{}, nil
		}
		close(recheckStarted)
		<-releaseRecheck
		return struct{}{}, errors.New("provider gone")
	})
	c.now = func() time.Time { return now }

	if _, err := c.get(context.Background()); err != nil {
		t.Fatalf("initial probe: %v", err)
	}

	// The provider breaks after the cache ages out: callers keep getting the
	// last completed success while exactly one recheck runs.
	now = now.Add(2 * time.Minute)
	if _, err := c.get(context.Background()); err != nil {
		t.Fatalf("stale success = %v, want last completed result while revalidating", err)
	}
	<-recheckStarted
	for range 10 {
		if _, err := c.get(context.Background()); err != nil {
			t.Fatalf("stale success during recheck = %v, want last completed result", err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("probe calls during recheck = %d, want one coalesced run", got)
	}

	// The completed recheck flips the served answer: broken is caught within
	// freshness + one probe duration, never silently.
	close(releaseRecheck)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := c.get(context.Background())
		if err != nil && strings.Contains(err.Error(), "provider gone") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("completed recheck did not flip the served result: err = %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
