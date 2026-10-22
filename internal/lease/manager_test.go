package lease

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/db"
)

// Real SQLite throughout: the transactions and the ON CONFLICT upsert are
// the mechanism under test, so a fake store would test nothing.

const sessionID = "sess-1"

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	d, err := db.Open(context.Background(), db.Config{
		Path: filepath.Join(t.TempDir(), "control.db"),
	})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	err = d.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		now := db.FormatTime(time.Now())
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			VALUES ('prj', 'test', '/tmp/prj', ?, ?)`, now, now); err != nil {
			return err
		}
		// `starting` deliberately: the schema refuses `running` without the
		// full identity tuple, and a lease needs no tuple.
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
				tmux_session_name, generation, status, created_at, updated_at)
			VALUES (?, 'prj', 'claude', '/tmp/ac.sock', 'noisedeck-test', 1, 'starting', ?, ?)`,
			sessionID, now, now)
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return NewManager(d)
}

func auditCount(t *testing.T, m *Manager, eventType string) int {
	t.Helper()
	var n int
	if err := m.db.Reader().QueryRow(
		`SELECT COUNT(*) FROM audit_events WHERE event_type = ?`, eventType).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", eventType, err)
	}
	return n
}

func TestFirstAcquireAndRenewalKeepToken(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	l, err := m.Acquire(ctx, sessionID, "browser-a", false)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if l.FencingToken != 1 {
		t.Fatalf("first token = %d, want 1", l.FencingToken)
	}

	// Re-acquiring as the same controller is a renewal: the token must NOT
	// move, or the holder would fence itself out of its own session.
	again, err := m.Acquire(ctx, sessionID, "browser-a", false)
	if err != nil {
		t.Fatalf("renewal: %v", err)
	}
	if again.FencingToken != 1 {
		t.Fatalf("renewal token = %d, want 1 (unchanged)", again.FencingToken)
	}
	if !again.ExpiresAt.After(l.ExpiresAt) && !again.ExpiresAt.Equal(l.ExpiresAt) {
		t.Fatal("renewal must not shorten expiry")
	}
	if n := auditCount(t, m, AuditAcquired); n != 1 {
		t.Fatalf("acquired audits = %d, want 1 (renewals are not auditable)", n)
	}
}

// The released schema still carries the retired incarnation column. New
// acquisitions leave its migration default untouched; runtime code must not
// recreate the removed acquisition identity through an old column.
func TestAcquireLeavesRetiredIncarnationColumnInert(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Acquire(context.Background(), sessionID, "browser-a", false); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	var incarnation int64
	if err := m.db.Reader().QueryRow(
		`SELECT incarnation FROM controller_leases WHERE session_id = ?`, sessionID,
	).Scan(&incarnation); err != nil {
		t.Fatalf("read released incarnation column: %v", err)
	}
	if incarnation != 0 {
		t.Fatalf("released incarnation column = %d, want migration default 0", incarnation)
	}
}

// A second browser that has not asked to take over is refused —
// it gets a takeover screen, never silent control.
func TestSecondControllerRefusedWithoutDeliberateTakeover(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	if _, err := m.Acquire(ctx, sessionID, "browser-a", false); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := m.Acquire(ctx, sessionID, "browser-b", false); !errors.Is(err, ErrHeld) {
		t.Fatalf("second controller: err = %v, want ErrHeld", err)
	}
	// The original holder is untouched.
	cur, err := m.Get(ctx, sessionID)
	if err != nil || cur.ControllerID != "browser-a" || cur.FencingToken != 1 {
		t.Fatalf("holder changed after a refused acquire: %+v (%v)", cur, err)
	}
}

// Deliberate takeover increments the token, and the displaced
// controller's writes are refused from that instant.
func TestDeliberateTakeoverFencesOldController(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	old, err := m.Acquire(ctx, sessionID, "browser-a", false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// The old controller can write before the takeover.
	if err := m.Validate(ctx, sessionID, "browser-a", old.FencingToken); err != nil {
		t.Fatalf("holder should validate before takeover: %v", err)
	}

	newL, err := m.Acquire(ctx, sessionID, "browser-b", true)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if newL.FencingToken != old.FencingToken+1 {
		t.Fatalf("takeover token = %d, want %d", newL.FencingToken, old.FencingToken+1)
	}

	// The displaced controller is fenced: its input and heartbeat are refused.
	if err := m.Validate(ctx, sessionID, "browser-a", old.FencingToken); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("displaced validate: err = %v, want ErrNotHeld", err)
	}
	if _, err := m.Heartbeat(ctx, sessionID, "browser-a", old.FencingToken); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("displaced heartbeat: err = %v, want ErrNotHeld", err)
	}
	// And the new controller works.
	if err := m.Validate(ctx, sessionID, "browser-b", newL.FencingToken); err != nil {
		t.Fatalf("new holder validate: %v", err)
	}
	if n := auditCount(t, m, AuditTakeover); n != 1 {
		t.Fatalf("takeover audits = %d, want 1", n)
	}
}

// A controller replaying the RIGHT identity with a STALE token is refused:
// fencing is a property of the token, not of the name.
func TestStaleTokenRefusedForSameController(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	first, _ := m.Acquire(ctx, sessionID, "browser-a", false)
	if _, err := m.Acquire(ctx, sessionID, "browser-b", true); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	// browser-a takes control back; its OLD token must still be refused.
	back, err := m.Acquire(ctx, sessionID, "browser-a", true)
	if err != nil {
		t.Fatalf("take back: %v", err)
	}
	if err := m.Validate(ctx, sessionID, "browser-a", first.FencingToken); !errors.Is(err, ErrStaleToken) {
		t.Fatalf("stale token for the same controller: err = %v, want ErrStaleToken", err)
	}
	if err := m.Validate(ctx, sessionID, "browser-a", back.FencingToken); err != nil {
		t.Fatalf("current token: %v", err)
	}
}

// An expired lease is reclaimable without a deliberate takeover, and the
// token does NOT move: nobody live was displaced.
func TestExpiredLeaseReclaimedWithoutTokenBump(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	now := time.Now()
	m.now = func() time.Time { return now }
	old, err := m.Acquire(ctx, sessionID, "browser-a", false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// Past the TTL with no heartbeat (no later than 15s).
	now = now.Add(TTL + time.Second)
	got, err := m.Acquire(ctx, sessionID, "browser-b", false)
	if err != nil {
		t.Fatalf("reclaim expired: %v", err)
	}
	if got.FencingToken != old.FencingToken {
		t.Fatalf("reclaim token = %d, want %d unchanged", got.FencingToken, old.FencingToken)
	}
	if n := auditCount(t, m, AuditExpired); n != 1 {
		t.Fatalf("expired-reclaim audits = %d, want 1", n)
	}
	if n := auditCount(t, m, AuditTakeover); n != 0 {
		t.Fatalf("reclaiming an expired lease is not a takeover, got %d takeover audits", n)
	}
}

// An expired lease writes nothing even for its own holder: the bound
// on write authority would be meaningless if silent resumption were allowed.
func TestExpiredHolderCannotWrite(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	now := time.Now()
	m.now = func() time.Time { return now }

	l, err := m.Acquire(ctx, sessionID, "browser-a", false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	now = now.Add(TTL + time.Second)
	if err := m.Validate(ctx, sessionID, "browser-a", l.FencingToken); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("expired holder validate: err = %v, want ErrNotHeld", err)
	}
}

func TestHeartbeatExtendsExpiry(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	now := time.Now()
	m.now = func() time.Time { return now }

	l, _ := m.Acquire(ctx, sessionID, "browser-a", false)
	now = now.Add(HeartbeatInterval)
	hb, err := m.Heartbeat(ctx, sessionID, "browser-a", l.FencingToken)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if !hb.ExpiresAt.After(l.ExpiresAt) {
		t.Fatalf("heartbeat did not extend expiry: %s !> %s", hb.ExpiresAt, l.ExpiresAt)
	}
	if hb.FencingToken != l.FencingToken {
		t.Fatal("heartbeat must not move the token")
	}
}

// A late heartbeat must NOT revive an expired lease: the fifteen-second
// bound is a promise to everyone else that the lease became claimable, and
// retracting it silently would let a zombie controller reappear after
// someone else began reclaiming.
func TestLateHeartbeatCannotReviveExpiredLease(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	now := time.Now()
	m.now = func() time.Time { return now }

	l, err := m.Acquire(ctx, sessionID, "browser-a", false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	now = now.Add(TTL + time.Second)
	if _, err := m.Heartbeat(ctx, sessionID, "browser-a", l.FencingToken); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("late heartbeat: err = %v, want ErrNotHeld", err)
	}
	// And it did not extend anything: the lease is still claimable.
	cur, err := m.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !cur.Expired(now) {
		t.Fatal("a refused heartbeat must not have extended expiry")
	}
	if _, err := m.Acquire(ctx, sessionID, "browser-b", false); err != nil {
		t.Fatalf("lease should still be claimable: %v", err)
	}
}

// The returning holder of an EXPIRED lease is reclaiming, not renewing —
// recorded as such even though the controller id is unchanged.
func TestSameControllerReacquiringExpiredLeaseIsAReclaim(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	now := time.Now()
	m.now = func() time.Time { return now }

	if _, err := m.Acquire(ctx, sessionID, "browser-a", false); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	now = now.Add(TTL + time.Second)
	if _, err := m.Acquire(ctx, sessionID, "browser-a", false); err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if n := auditCount(t, m, AuditExpired); n != 1 {
		t.Fatalf("expired-reclaim audits = %d, want 1 — a returning holder is not renewing", n)
	}
}

// The invariant that matters under load: simultaneous deliberate takeovers
// must leave exactly one holder, with a token that advanced exactly once
// per successful takeover.
func TestConcurrentTakeoversYieldOneHolder(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	if _, err := m.Acquire(ctx, sessionID, "browser-0", false); err != nil {
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
			results[i], errs[i] = m.Acquire(ctx, sessionID,
				"browser-"+string(rune('a'+i)), true)
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

	// Exactly one controller holds the lease, and the token advanced once
	// per winner — no lost update.
	final, err := m.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("final get: %v", err)
	}
	if want := int64(1 + won); final.FencingToken != want {
		t.Fatalf("final token = %d, want %d (1 + %d successful takeovers)",
			final.FencingToken, want, won)
	}
	// Every loser must be fenced: only the recorded holder validates.
	holders := 0
	for i := range results {
		if results[i] == nil {
			continue
		}
		if err := m.Validate(ctx, sessionID, results[i].ControllerID, results[i].FencingToken); err == nil {
			holders++
		}
	}
	if holders != 1 {
		t.Fatalf("%d controllers validate concurrently, want exactly 1", holders)
	}
}

// A lease lost to the TTL is distinguishable from one taken by another
// controller. The proxy tells the browser which happened, and the two
// mean opposite things: an expired lease was taken by nobody, so
// reclaiming it displaces no one, while a fenced controller must stop.
//
// ErrExpired wraps ErrNotHeld so every existing check keeps treating it
// as loss of the lease. That wrapping is the compatibility contract, and
// it is asserted here rather than assumed.
func TestExpiryIsDistinguishableFromDisplacement(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	l, err := m.Acquire(ctx, sessionID, "controller-a", false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// Displacement: a different controller takes it deliberately.
	if _, err := m.Acquire(ctx, sessionID, "controller-b", true); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	err = m.Validate(ctx, sessionID, l.ControllerID, l.FencingToken)
	if errors.Is(err, ErrExpired) {
		t.Fatalf("displacement reported as expiry: %v", err)
	}
	if err == nil {
		t.Fatal("a displaced controller still validated")
	}

	// Expiry: the holder simply goes quiet past the TTL.
	l2, err := m.Acquire(ctx, sessionID, "controller-c", true)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	m.now = func() time.Time { return time.Now().Add(2 * TTL) }
	err = m.Validate(ctx, sessionID, l2.ControllerID, l2.FencingToken)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expiry = %v, want ErrExpired — a timed-out lease must not read as a takeover", err)
	}
	// Everything that already treated expiry as loss of the lease must
	// keep working.
	if !errors.Is(err, ErrNotHeld) {
		t.Fatalf("ErrExpired no longer wraps ErrNotHeld: %v", err)
	}
}

// The order of the guards is what makes lease_expired safe to act on.
//
// Controller identity is tested before the token, and the token before
// expiry, so a lease another controller holds short-circuits before
// expiry is ever evaluated. Reverse that and a lease someone else took
// and then let go stale would report as ErrExpired — and the browser,
// which reclaims an expired lease automatically, would reclaim straight
// into a live displacement.
func TestDisplacementOutranksExpiry(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	old, err := m.Acquire(ctx, sessionID, "browser-a", false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := m.Acquire(ctx, sessionID, "browser-b", true); err != nil {
		t.Fatalf("takeover: %v", err)
	}

	// BOTH conditions now hold: browser-a was displaced, and the lease it
	// remembers is also long past its TTL. Displacement must win.
	m.now = func() time.Time { return time.Now().Add(4 * TTL) }

	err = m.Validate(ctx, sessionID, old.ControllerID, old.FencingToken)
	if err == nil {
		t.Fatal("a displaced controller validated")
	}
	if errors.Is(err, ErrExpired) {
		t.Fatalf("displacement reported as expiry (%v) — the browser would reclaim into a live controller", err)
	}
}

// The heartbeat path carries the same distinction as Validate, and it is
// the path this matters most on: a hidden tab's throttled timer produces
// exactly this — a late heartbeat against a lease that has lapsed.
//
// TestLateHeartbeatCannotReviveExpiredLease cannot see it. It asserts
// errors.Is(err, ErrNotHeld), which holds either way BECAUSE ErrExpired
// wraps ErrNotHeld — the wrap that buys compatibility is what makes that
// assertion blind. This pins the distinction itself, in both directions.
func TestHeartbeatDistinguishesExpiryFromDisplacement(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	l, err := m.Acquire(ctx, sessionID, "browser-a", false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// Went quiet: nobody took the session.
	m.now = func() time.Time { return time.Now().Add(4 * TTL) }
	_, err = m.Heartbeat(ctx, sessionID, l.ControllerID, l.FencingToken)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("lapsed heartbeat = %v, want ErrExpired — this is the throttled-tab case", err)
	}

	// Displaced: another controller holds it. Even with the lease also
	// long past its TTL, displacement must win, or the browser would
	// reclaim automatically into a live controller.
	m.now = time.Now
	l2, err := m.Acquire(ctx, sessionID, "browser-b", true)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	m.now = func() time.Time { return time.Now().Add(4 * TTL) }
	_, err = m.Heartbeat(ctx, sessionID, l.ControllerID, l.FencingToken)
	if err == nil {
		t.Fatal("a displaced controller's heartbeat succeeded")
	}
	if errors.Is(err, ErrExpired) {
		t.Fatalf("displacement reported as expiry (%v) — the browser would reclaim into a live controller", err)
	}
	_ = l2
}
