package lease

import (
	"errors"
	"fmt"
	"time"
)

// transition is a lease write and its optional audit event.
// Both stores use these decisions; Manager commits the event with the write.
type transition struct {
	next      Lease
	auditType string
	details   string
}

func freshLease(sessionID, controllerID string, token int64, now time.Time, ttl time.Duration) Lease {
	return Lease{
		SessionID:     sessionID,
		ControllerID:  controllerID,
		FencingToken:  token,
		LastHeartbeat: now,
		ExpiresAt:     now.Add(ttl),
	}
}

// decideAcquire checks expiry before ownership so an expired holder reclaims
// its lease rather than silently renewing it.
func decideAcquire(cur *Lease, sessionID, controllerID string, deliberate bool, now time.Time, ttl time.Duration) (transition, error) {
	if sessionID == "" || controllerID == "" {
		return transition{}, errors.New("lease: session and controller ids are required")
	}
	switch {
	case cur == nil:
		return transition{
			next:      freshLease(sessionID, controllerID, 1, now, ttl),
			auditType: AuditAcquired,
			details:   "first controller",
		}, nil
	case cur.Expired(now):
		// Expiry is tested FIRST, even for the same controller: once a lease
		// has expired it is gone, and a returning holder is reclaiming it
		// rather than renewing — a distinction that must be recorded, not
		// silently smoothed over. No live controller is displaced, so the
		// token stands.
		return transition{
			next:      freshLease(sessionID, controllerID, cur.FencingToken, now, ttl),
			auditType: AuditExpired,
			details:   fmt.Sprintf("reclaimed from %s", cur.ControllerID),
		}, nil
	case cur.ControllerID == controllerID:
		// A renewal keeps the token: the holder was never displaced, and
		// bumping it here would fence the holder out of its own session.
		return transition{
			next: freshLease(sessionID, controllerID, cur.FencingToken, now, ttl),
		}, nil
	case deliberate:
		// The displacing act: new holder and new token in one decision, so
		// they cannot commit separately in any store.
		return transition{
			next:      freshLease(sessionID, controllerID, cur.FencingToken+1, now, ttl),
			auditType: AuditTakeover,
			details:   fmt.Sprintf("took control from %s", cur.ControllerID),
		}, nil
	default:
		return transition{}, ErrHeld
	}
}

// decideHeartbeat refuses expired leases instead of reviving them.
func decideHeartbeat(cur *Lease, controllerID string, token int64, now time.Time, ttl time.Duration) (transition, error) {
	switch {
	case cur == nil:
		return transition{}, ErrNotFound
	case cur.ControllerID != controllerID:
		return transition{}, ErrNotHeld
	case cur.FencingToken != token:
		return transition{}, ErrStaleToken
	case cur.Expired(now):
		return transition{}, ErrExpired
	}
	return transition{next: freshLease(cur.SessionID, controllerID, cur.FencingToken, now, ttl)}, nil
}

// decideValidate checks displacement before expiry; otherwise the browser
// could mistake a takeover for an expired lease it may reclaim.
func decideValidate(cur *Lease, controllerID string, token int64, now time.Time) error {
	switch {
	case cur == nil:
		return ErrNotFound
	case cur.ControllerID != controllerID:
		return ErrNotHeld
	case cur.FencingToken != token:
		return ErrStaleToken
	case cur.Expired(now):
		return ErrExpired
	}
	return nil
}
