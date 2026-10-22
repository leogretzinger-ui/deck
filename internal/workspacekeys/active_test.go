package workspacekeys

import (
	"testing"
	"time"
)

func TestActiveMethodCheckRequiresCurrentBrowserPreparation(t *testing.T) {
	f := newFixture(t)
	day, _ := f.initialize()
	action := Action{Operation: VerifyMethod, Method: day.method.MethodRef}
	f.now = f.now.Add(time.Minute)
	f.grant.err = ErrDenied
	_, err := f.r.BeginChallenge(f.ctx, f.actor, action)
	refused(t, err)
	f.grant.err = nil
	challenge, err := f.r.BeginChallenge(f.ctx, f.actor, action)
	if err != nil {
		t.Fatal(err)
	}
	f.grant.err = ErrDenied
	_, err = f.r.ConsumeProof(f.ctx, f.actor, challenge.ID, sign(t, day.key, challenge), nil)
	refused(t, err)
	f.grant.err = nil
	permit, err := f.r.ConsumeProof(f.ctx, f.actor, challenge.ID, sign(t, day.key, challenge), nil)
	if err != nil {
		t.Fatal(err)
	}
	if permit.proof != nil || permit.execution == nil {
		t.Fatal("method check changed browser authorization")
	}
	var verified int64
	var status, storedDigest, verifiedDigest string
	if err := f.db.QueryRow(`SELECT verified_at,status,envelope_digest,verified_digest FROM workspacekeys_methods WHERE id=?`, day.method.ID).Scan(&verified, &status, &storedDigest, &verifiedDigest); err != nil {
		t.Fatal(err)
	}
	if verified != f.now.UnixMilli() || status != "active" || storedDigest != verifiedDigest {
		t.Fatal("active check did not update the tested envelope")
	}
	pending := f.store(Password)
	f.now = f.now.Add(24 * time.Hour)
	_, err = f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: VerifyMethod, Method: pending.method.MethodRef})
	refused(t, err)
}
