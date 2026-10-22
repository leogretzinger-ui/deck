package session

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func activitySession(t *testing.T) (*Manager, *Repository, *Session) {
	t.Helper()
	mgr, repo, _, projectID := newTestEnv(t, sleeperDescriptor())
	// Identity commitment at launch is a transition tmux publishes
	// asynchronously: an observation that catches the leader mid-exit is
	// conservatively rejected as a replacement (ErrIdentityMismatch), which
	// for a launch is a retry, not a defect. Repeat the launch like a real
	// caller would — a dozen straight rejections still fail, and any other
	// create error fails immediately.
	var (
		s   *Session
		err error
	)
	for attempt := 0; attempt < 12; attempt++ {
		s, err = mgr.Create(context.Background(), CreateSpec{
			ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir(),
		})
		if err == nil || !errors.Is(err, ErrIdentityMismatch) {
			break
		}
	}
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return mgr, repo, s
}

func TestObservedActivityUsesTmuxIdentityWithoutReadingOutput(t *testing.T) {
	mgr, _, session := activitySession(t)
	activities, err := mgr.ObservedActivity(context.Background())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if len(activities) != 1 {
		t.Fatalf("activities = %d, want 1", len(activities))
	}
	got := activities[0]
	if got.SessionID != session.ID || got.Generation != session.Generation || got.At.IsZero() {
		t.Fatalf("activity = %+v, want exact live session identity and timestamp", got)
	}
}

func TestObservedActivityRejectsStaleGeneration(t *testing.T) {
	mgr, repo, session := activitySession(t)
	if err := repo.db.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `UPDATE sessions SET generation = generation + 1 WHERE id = ? RETURNING generation`, session.ID).Scan(&session.Generation)
	}); err != nil {
		t.Fatal(err)
	}
	activities, err := mgr.ObservedActivity(context.Background())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if len(activities) != 0 {
		t.Fatalf("stale generation produced %+v", activities)
	}
}

func TestSampleActivityMovesForwardAndDoesNotRewriteUnchangedStamp(t *testing.T) {
	mgr, repo, session := activitySession(t)
	ctx := context.Background()
	if err := mgr.SampleActivity(ctx); err != nil {
		t.Fatalf("sample: %v", err)
	}
	first, err := repo.Get(ctx, session.ID)
	if err != nil || first.LastOutputAt == nil {
		t.Fatalf("first stamp = %v, err = %v", first.LastOutputAt, err)
	}
	before := *first.LastOutputAt
	if err := mgr.SampleActivity(ctx); err != nil {
		t.Fatalf("repeat sample: %v", err)
	}
	after, err := repo.Get(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastOutputAt == nil || !after.LastOutputAt.Equal(before) {
		t.Fatalf("unchanged tmux stamp moved from %v to %v", before, after.LastOutputAt)
	}
	if after.LastOutputAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("activity stamp is in the future: %v", after.LastOutputAt)
	}
}
