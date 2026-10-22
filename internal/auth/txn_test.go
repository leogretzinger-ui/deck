package auth

import (
	"testing"
	"time"
)

// The store is bounded and must stay bounded even when the clock moves
// backwards: eviction picks the genuinely oldest entry, never one selected
// against a now-derived bound (which a rollback leaves empty, spinning the
// loop forever).
func TestPutEvictsUnderClockRollback(t *testing.T) {
	now := time.Now()
	s := newTxnStore(func() time.Time { return now })

	for i := 0; i < txnMax; i++ {
		if _, err := s.put(transaction{state: "s", nonce: "n", verifier: "v"}); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
		now = now.Add(time.Millisecond)
	}
	if len(s.byID) != txnMax {
		t.Fatalf("store holds %d, want %d", len(s.byID), txnMax)
	}

	// The clock rolls back a minute; the next put must still evict and
	// return promptly.
	now = now.Add(-time.Minute)
	done := make(chan error, 1)
	go func() {
		_, err := s.put(transaction{state: "s2", nonce: "n2", verifier: "v2"})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("put after rollback: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("put spun after a clock rollback")
	}
	if len(s.byID) > txnMax {
		t.Fatalf("store grew past the cap: %d", len(s.byID))
	}
}

// take consumes exactly once and refuses expired entries.
func TestTakeConsumesOnceAndExpires(t *testing.T) {
	now := time.Now()
	s := newTxnStore(func() time.Time { return now })

	id, err := s.put(transaction{state: "s", nonce: "n", verifier: "v"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, ok := s.take(id); !ok {
		t.Fatal("first take should succeed")
	}
	if _, ok := s.take(id); ok {
		t.Fatal("second take must find nothing")
	}

	id2, _ := s.put(transaction{state: "s", nonce: "n", verifier: "v"})
	now = now.Add(txnTTL + time.Second)
	if _, ok := s.take(id2); ok {
		t.Fatal("expired transaction must not be taken")
	}
}
