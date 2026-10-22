package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Transaction bounds mirror the research-harness values: five
// minutes to complete a login, at most 256 concurrent attempts, thirty
// seconds of clock-skew tolerance.
const (
	txnTTL      = 5 * time.Minute
	txnMax      = 256
	txnSkew     = 30 * time.Second
	txnCookie   = "noisedeck_oidc_txn"
	txnHostName = "__Host-noisedeck_oidc_txn"
)

type transaction struct {
	state    string
	nonce    string
	verifier string
	created  time.Time
}

// txnStore holds in-flight login transactions: bounded, short-lived,
// consumed exactly once at callback entry. A web-process restart forgets
// in-flight logins only; the user starts a new one.
type txnStore struct {
	mu   sync.Mutex
	byID map[string]transaction
	now  func() time.Time
}

func newTxnStore(now func() time.Time) *txnStore {
	return &txnStore{byID: make(map[string]transaction), now: now}
}

// put stores a new transaction under a fresh random id, evicting the oldest
// entries beyond the cap so an unauthenticated client cannot grow the store.
func (s *txnStore) put(t transaction) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("auth: transaction id: %w", err)
	}
	id := hex.EncodeToString(b[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	// Evict the genuinely oldest entry, chosen unconditionally rather than
	// against a now-derived bound: a clock rollback would leave every entry
	// "not older than now", select nothing, and spin this loop forever.
	for len(s.byID) >= txnMax {
		oldestID := ""
		var oldest time.Time
		for k, v := range s.byID {
			if oldestID == "" || v.created.Before(oldest) {
				oldestID, oldest = k, v.created
			}
		}
		if oldestID == "" {
			break // unreachable while len > 0; a guard, not a policy
		}
		delete(s.byID, oldestID)
	}
	t.created = s.now()
	s.byID[id] = t
	return id, nil
}

// take consumes a transaction exactly once, refusing expired or unknown
// ids. A second callback with the same id — replayed or raced — finds
// nothing.
func (s *txnStore) take(id string) (transaction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return transaction{}, false
	}
	delete(s.byID, id)
	age := s.now().Sub(t.created)
	if age < -txnSkew || age > txnTTL {
		return transaction{}, false
	}
	return t, true
}
