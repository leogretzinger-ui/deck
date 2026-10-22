package lease

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// The stores share controller transitions; durable audit behavior remains
// covered by the existing Manager tests.
type contractStore interface {
	Acquire(ctx context.Context, sessionID, controllerID string, deliberate bool) (*Lease, error)
	Heartbeat(ctx context.Context, sessionID, controllerID string, token int64) (*Lease, error)
	Validate(ctx context.Context, sessionID, controllerID string, token int64) error
	Get(ctx context.Context, sessionID string) (*Lease, error)
}

// contractFactory builds one fresh store per subtest, with a clock hook so
// expiry is decided deterministically.
type contractFactory struct {
	name string
	new  func(t *testing.T) (contractStore, func(func() time.Time))
}

func contractFactories() []contractFactory {
	return []contractFactory{
		{
			name: "manager",
			new: func(t *testing.T) (contractStore, func(func() time.Time)) {
				m := newTestManager(t)
				return m, func(f func() time.Time) { m.now = f }
			},
		},
		{
			name: "local",
			new: func(t *testing.T) (contractStore, func(func() time.Time)) {
				l := NewLocal()
				return l, func(f func() time.Time) { l.now = f }
			},
		},
	}
}

func TestContractFirstAcquireAndRenewal(t *testing.T) {
	for _, f := range contractFactories() {
		t.Run(f.name, func(t *testing.T) {
			s, _ := f.new(t)
			ctx := context.Background()

			l, err := s.Acquire(ctx, sessionID, "browser-a", false)
			if err != nil {
				t.Fatalf("first acquire: %v", err)
			}
			if l.FencingToken != 1 {
				t.Fatalf("first token = %d, want 1", l.FencingToken)
			}

			again, err := s.Acquire(ctx, sessionID, "browser-a", false)
			if err != nil {
				t.Fatalf("renewal: %v", err)
			}
			if again.FencingToken != 1 {
				t.Fatalf("renewal token = %d, want 1 (unchanged)", again.FencingToken)
			}
			if again.ExpiresAt.Before(l.ExpiresAt) {
				t.Fatal("renewal must not shorten expiry")
			}
		})
	}
}

func TestContractEmptyIDsRefused(t *testing.T) {
	for _, f := range contractFactories() {
		t.Run(f.name, func(t *testing.T) {
			s, _ := f.new(t)
			ctx := context.Background()

			if _, err := s.Acquire(ctx, "", "browser-a", false); err == nil {
				t.Fatal("acquire with empty session id succeeded")
			}
			if _, err := s.Acquire(ctx, sessionID, "", false); err == nil {
				t.Fatal("acquire with empty controller id succeeded")
			}
			if _, err := s.Get(ctx, sessionID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("a refused acquire recorded a lease: %v", err)
			}
		})
	}
}

func TestContractUnknownSessionIsNotFound(t *testing.T) {
	for _, f := range contractFactories() {
		t.Run(f.name, func(t *testing.T) {
			s, _ := f.new(t)
			ctx := context.Background()

			if _, err := s.Get(ctx, "sess-unknown"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("get: err = %v, want ErrNotFound", err)
			}
			if _, err := s.Heartbeat(ctx, "sess-unknown", "browser-a", 1); !errors.Is(err, ErrNotFound) {
				t.Fatalf("heartbeat: err = %v, want ErrNotFound", err)
			}
			if err := s.Validate(ctx, "sess-unknown", "browser-a", 1); !errors.Is(err, ErrNotFound) {
				t.Fatalf("validate: err = %v, want ErrNotFound", err)
			}
		})
	}
}

// A second browser that has not asked to take over is refused — it gets a
// takeover screen, never silent control.
func TestContractSecondControllerRefused(t *testing.T) {
	for _, f := range contractFactories() {
		t.Run(f.name, func(t *testing.T) {
			s, _ := f.new(t)
			ctx := context.Background()

			if _, err := s.Acquire(ctx, sessionID, "browser-a", false); err != nil {
				t.Fatalf("acquire: %v", err)
			}
			if _, err := s.Acquire(ctx, sessionID, "browser-b", false); !errors.Is(err, ErrHeld) {
				t.Fatalf("second controller: err = %v, want ErrHeld", err)
			}
			cur, err := s.Get(ctx, sessionID)
			if err != nil || cur.ControllerID != "browser-a" || cur.FencingToken != 1 {
				t.Fatalf("holder changed after a refused acquire: %+v (%v)", cur, err)
			}
		})
	}
}

// Deliberate takeover increments the token, and the displaced controller's
// writes and heartbeats are refused from that instant.
func TestContractTakeoverFencesOldController(t *testing.T) {
	for _, f := range contractFactories() {
		t.Run(f.name, func(t *testing.T) {
			s, _ := f.new(t)
			ctx := context.Background()

			old, err := s.Acquire(ctx, sessionID, "browser-a", false)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			if err := s.Validate(ctx, sessionID, "browser-a", old.FencingToken); err != nil {
				t.Fatalf("holder should validate before takeover: %v", err)
			}

			newL, err := s.Acquire(ctx, sessionID, "browser-b", true)
			if err != nil {
				t.Fatalf("takeover: %v", err)
			}
			if newL.FencingToken != old.FencingToken+1 {
				t.Fatalf("takeover token = %d, want %d", newL.FencingToken, old.FencingToken+1)
			}
			if err := s.Validate(ctx, sessionID, "browser-a", old.FencingToken); !errors.Is(err, ErrNotHeld) {
				t.Fatalf("displaced validate: err = %v, want ErrNotHeld", err)
			}
			if _, err := s.Heartbeat(ctx, sessionID, "browser-a", old.FencingToken); !errors.Is(err, ErrNotHeld) {
				t.Fatalf("displaced heartbeat: err = %v, want ErrNotHeld", err)
			}
			if err := s.Validate(ctx, sessionID, "browser-b", newL.FencingToken); err != nil {
				t.Fatalf("new holder validate: %v", err)
			}
		})
	}
}

// A controller replaying the RIGHT identity with a STALE token is refused:
// fencing is a property of the token, not of the name.
func TestContractStaleTokenRefusedForSameController(t *testing.T) {
	for _, f := range contractFactories() {
		t.Run(f.name, func(t *testing.T) {
			s, _ := f.new(t)
			ctx := context.Background()

			first, _ := s.Acquire(ctx, sessionID, "browser-a", false)
			if _, err := s.Acquire(ctx, sessionID, "browser-b", true); err != nil {
				t.Fatalf("takeover: %v", err)
			}
			back, err := s.Acquire(ctx, sessionID, "browser-a", true)
			if err != nil {
				t.Fatalf("take back: %v", err)
			}
			if err := s.Validate(ctx, sessionID, "browser-a", first.FencingToken); !errors.Is(err, ErrStaleToken) {
				t.Fatalf("stale token for the same controller: err = %v, want ErrStaleToken", err)
			}
			if err := s.Validate(ctx, sessionID, "browser-a", back.FencingToken); err != nil {
				t.Fatalf("current token: %v", err)
			}
		})
	}
}

// An expired lease is reclaimable without a deliberate takeover, and the
// token does NOT move: nobody live was displaced. The returning holder of
// an expired lease is reclaiming, not renewing — and succeeds on the same
// terms.
func TestContractExpiredLeaseReclaimedWithoutTokenBump(t *testing.T) {
	for _, f := range contractFactories() {
		t.Run(f.name, func(t *testing.T) {
			s, setClock := f.new(t)
			ctx := context.Background()

			now := time.Now()
			setClock(func() time.Time { return now })
			old, err := s.Acquire(ctx, sessionID, "browser-a", false)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}

			now = now.Add(TTL + time.Second)
			got, err := s.Acquire(ctx, sessionID, "browser-b", false)
			if err != nil {
				t.Fatalf("reclaim expired: %v", err)
			}
			if got.FencingToken != old.FencingToken {
				t.Fatalf("reclaim token = %d, want %d unchanged", got.FencingToken, old.FencingToken)
			}

			now = now.Add(TTL + time.Second)
			back, err := s.Acquire(ctx, sessionID, "browser-b", false)
			if err != nil {
				t.Fatalf("same-controller reacquire of an expired lease: %v", err)
			}
			if back.FencingToken != old.FencingToken {
				t.Fatalf("reacquire token = %d, want %d unchanged", back.FencingToken, old.FencingToken)
			}
		})
	}
}

// An expired lease accepts no write and no heartbeat, even from its own
// holder — and the refused heartbeat must not extend anything.
func TestContractExpiredHolderCannotWriteOrHeartbeat(t *testing.T) {
	for _, f := range contractFactories() {
		t.Run(f.name, func(t *testing.T) {
			s, setClock := f.new(t)
			ctx := context.Background()

			now := time.Now()
			setClock(func() time.Time { return now })
			l, err := s.Acquire(ctx, sessionID, "browser-a", false)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}

			now = now.Add(TTL + time.Second)
			if err := s.Validate(ctx, sessionID, "browser-a", l.FencingToken); !errors.Is(err, ErrExpired) {
				t.Fatalf("expired holder validate: err = %v, want ErrExpired", err)
			}
			if _, err := s.Heartbeat(ctx, sessionID, "browser-a", l.FencingToken); !errors.Is(err, ErrExpired) {
				t.Fatalf("late heartbeat: err = %v, want ErrExpired", err)
			}
			cur, err := s.Get(ctx, sessionID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if !cur.Expired(now) {
				t.Fatal("a refused heartbeat must not have extended expiry")
			}
			if _, err := s.Acquire(ctx, sessionID, "browser-b", false); err != nil {
				t.Fatalf("lease should still be claimable: %v", err)
			}
		})
	}
}

func TestContractHeartbeatExtendsExpiry(t *testing.T) {
	for _, f := range contractFactories() {
		t.Run(f.name, func(t *testing.T) {
			s, setClock := f.new(t)
			ctx := context.Background()

			now := time.Now()
			setClock(func() time.Time { return now })
			l, _ := s.Acquire(ctx, sessionID, "browser-a", false)

			now = now.Add(HeartbeatInterval)
			hb, err := s.Heartbeat(ctx, sessionID, "browser-a", l.FencingToken)
			if err != nil {
				t.Fatalf("heartbeat: %v", err)
			}
			if !hb.ExpiresAt.After(l.ExpiresAt) {
				t.Fatalf("heartbeat did not extend expiry: %s !> %s", hb.ExpiresAt, l.ExpiresAt)
			}
			if hb.FencingToken != l.FencingToken {
				t.Fatal("heartbeat must not move the token")
			}
		})
	}
}

// Controller identity is tested before the token, and the token before
// expiry, so a lease another controller holds short-circuits before expiry
// is ever evaluated. Reverse that and a lease someone else took and then
// let go stale would report as ErrExpired — and the browser, which reclaims
// an expired lease automatically, would reclaim straight into a live
// displacement.
func TestContractDisplacementOutranksExpiry(t *testing.T) {
	for _, f := range contractFactories() {
		t.Run(f.name, func(t *testing.T) {
			s, setClock := f.new(t)
			ctx := context.Background()

			old, err := s.Acquire(ctx, sessionID, "browser-a", false)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			if _, err := s.Acquire(ctx, sessionID, "browser-b", true); err != nil {
				t.Fatalf("takeover: %v", err)
			}

			// BOTH conditions now hold: browser-a was displaced, and the
			// lease it remembers is also long past its TTL. Displacement must
			// win on both the write path and the renewal path.
			setClock(func() time.Time { return time.Now().Add(4 * TTL) })

			err = s.Validate(ctx, sessionID, old.ControllerID, old.FencingToken)
			if err == nil {
				t.Fatal("a displaced controller validated")
			}
			if errors.Is(err, ErrExpired) {
				t.Fatalf("displacement reported as expiry (%v) — the browser would reclaim into a live controller", err)
			}
			_, err = s.Heartbeat(ctx, sessionID, old.ControllerID, old.FencingToken)
			if err == nil {
				t.Fatal("a displaced controller's heartbeat succeeded")
			}
			if errors.Is(err, ErrExpired) {
				t.Fatalf("displaced heartbeat reported as expiry (%v) — the browser would reclaim into a live controller", err)
			}
		})
	}
}

// Simultaneous deliberate takeovers must leave exactly one holder, with a
// token that advanced exactly once per successful takeover — no lost update
// in either store.
func TestContractConcurrentTakeoversYieldOneHolder(t *testing.T) {
	for _, f := range contractFactories() {
		t.Run(f.name, func(t *testing.T) {
			s, _ := f.new(t)
			ctx := context.Background()

			if _, err := s.Acquire(ctx, sessionID, "browser-0", false); err != nil {
				t.Fatalf("seed acquire: %v", err)
			}

			const n = 8
			var wg sync.WaitGroup
			results := make([]*Lease, n)
			errs := make([]error, n)
			start := make(chan struct{})
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					results[i], errs[i] = s.Acquire(ctx, sessionID,
						fmt.Sprintf("browser-%d", i+1), true)
				}(i)
			}
			close(start)
			wg.Wait()

			won := 0
			for i := range results {
				if errs[i] == nil {
					won++
				}
			}
			if won == 0 {
				t.Fatalf("no takeover succeeded: %v", errs)
			}

			final, err := s.Get(ctx, sessionID)
			if err != nil {
				t.Fatalf("final get: %v", err)
			}
			if want := int64(1 + won); final.FencingToken != want {
				t.Fatalf("final token = %d, want %d (1 + %d successful takeovers)",
					final.FencingToken, want, won)
			}
			holders := 0
			for i := range results {
				if results[i] == nil {
					continue
				}
				if err := s.Validate(ctx, sessionID, results[i].ControllerID, results[i].FencingToken); err == nil {
					holders++
				}
			}
			if holders != 1 {
				t.Fatalf("%d controllers validate concurrently, want exactly 1", holders)
			}
		})
	}
}

// Stale heartbeats racing a deliberate takeover: each heartbeat either
// lands before the takeover commits (nil) or is refused as displaced
// (ErrNotHeld). What must never happen is the old token staying valid once
// the takeover is visible — the final state must be the new holder with the
// bumped token, and the old token fenced.
func TestContractConcurrentTakeoverFencesRacingStaleHeartbeats(t *testing.T) {
	for _, f := range contractFactories() {
		t.Run(f.name, func(t *testing.T) {
			s, _ := f.new(t)
			ctx := context.Background()

			old, err := s.Acquire(ctx, sessionID, "browser-a", false)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}

			const n = 8
			hbErrs := make([]error, n)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					_, hbErrs[i] = s.Heartbeat(ctx, sessionID, "browser-a", old.FencingToken)
				}(i)
			}
			wg.Add(1)
			var newL *Lease
			var takeErr error
			go func() {
				defer wg.Done()
				<-start
				newL, takeErr = s.Acquire(ctx, sessionID, "browser-b", true)
			}()
			close(start)
			wg.Wait()

			if takeErr != nil {
				t.Fatalf("takeover: %v", takeErr)
			}
			for i, err := range hbErrs {
				if err != nil && !errors.Is(err, ErrNotHeld) {
					t.Fatalf("heartbeat %d: err = %v, want nil or ErrNotHeld", i, err)
				}
			}

			final, err := s.Get(ctx, sessionID)
			if err != nil {
				t.Fatalf("final get: %v", err)
			}
			if final.ControllerID != "browser-b" || final.FencingToken != old.FencingToken+1 {
				t.Fatalf("final = %+v, want browser-b with token %d", final, old.FencingToken+1)
			}
			if err := s.Validate(ctx, sessionID, "browser-a", old.FencingToken); err == nil {
				t.Fatal("stale token validated after the takeover")
			}
			if err := s.Validate(ctx, sessionID, "browser-b", newL.FencingToken); err != nil {
				t.Fatalf("new holder validate: %v", err)
			}
		})
	}
}

// A returned lease is a view, not a handle on the recorded state: mutating
// it must not move the recorded holder, token, or expiry, or any caller
// could rewrite the fencing decision without one being made.
func TestContractReturnedLeaseIsACopy(t *testing.T) {
	for _, f := range contractFactories() {
		t.Run(f.name, func(t *testing.T) {
			s, _ := f.new(t)
			ctx := context.Background()

			l, err := s.Acquire(ctx, sessionID, "browser-a", false)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			before, err := s.Get(ctx, sessionID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}

			l.ControllerID = "mallory"
			l.FencingToken = 999
			l.ExpiresAt = time.Now().Add(time.Hour)

			after, err := s.Get(ctx, sessionID)
			if err != nil {
				t.Fatalf("get after mutation: %v", err)
			}
			if after.ControllerID != before.ControllerID ||
				after.FencingToken != before.FencingToken ||
				!after.ExpiresAt.Equal(before.ExpiresAt) {
				t.Fatalf("stored lease moved by mutating the returned value: before %+v, after %+v", before, after)
			}

			// Get hands out a copy on the same terms.
			after.ControllerID = "mallory"
			again, err := s.Get(ctx, sessionID)
			if err != nil {
				t.Fatalf("get again: %v", err)
			}
			if again.ControllerID != before.ControllerID {
				t.Fatalf("stored holder = %q after mutating a Get result, want %q",
					again.ControllerID, before.ControllerID)
			}
		})
	}
}

// A cancelled context refuses the operation and records nothing, in both
// stores.
func TestContractCancelledContextRecordsNothing(t *testing.T) {
	for _, f := range contractFactories() {
		t.Run(f.name, func(t *testing.T) {
			s, _ := f.new(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			if _, err := s.Acquire(ctx, sessionID, "browser-a", false); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled acquire: err = %v, want context.Canceled", err)
			}
			if err := s.Validate(ctx, sessionID, "browser-a", 1); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled validate: err = %v, want context.Canceled", err)
			}
			if _, err := s.Get(context.Background(), sessionID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("a cancelled acquire recorded a lease: %v", err)
			}
		})
	}
}

func TestLocalDefaults(t *testing.T) {
	l := NewLocal()
	if l.ttl != TTL {
		t.Fatalf("local ttl = %s, want %s", l.ttl, TTL)
	}
	got, err := l.Acquire(context.Background(), sessionID, "browser-a", false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if d := time.Until(got.ExpiresAt); d <= 0 || d > TTL {
		t.Fatalf("expiry %s from now, want within (0, %s]", d, TTL)
	}
}
