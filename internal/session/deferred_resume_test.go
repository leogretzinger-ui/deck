package session

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/hookcapture"
	"github.com/noisefloor-oss/deck/internal/provider"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

func deferredResumeEnv(t *testing.T) (*Manager, *Repository, *attestedResumeDescriptor, string) {
	t.Helper()
	mgr, repo, desc, projectID := newAttestedResumeEnv(t)
	desc.hook = &provider.IdentityHook{Timing: provider.IdentityHookAfterFirstSubmission}
	mgr.resumeAttestationBound = 10 * time.Millisecond
	return mgr, repo, desc, projectID
}

func TestDeferredResumeCommitsAndAcceptsLateHook(t *testing.T) {
	mgr, repo, desc, projectID := deferredResumeEnv(t)
	ctx := context.Background()
	workDir := t.TempDir()
	created, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: provider.Codex, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	crashAndObserve(t, mgr, created)
	source, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := mgr.resume(ctx, source.ID, workDir)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != StatusRunning || resumed.Generation != source.Generation+1 || !resumed.GenerationResumed {
		t.Fatalf("resume did not commit live exact generation: %+v", resumed)
	}
	if slot, err := repo.SuccessorSlot(ctx, source.ID); err != nil || slot != nil {
		t.Fatalf("committed resume left slot: %+v err=%v", slot, err)
	}
	token := readLaunchToken(t, desc.tokenFile)
	// The first user turn may come long after the eager attestation bound.
	time.Sleep(3 * mgr.resumeAttestationBound)
	pending, err := repo.IdentityCapturePending(ctx, resumed.ID, resumed.Generation, *resumed.IdentityMarker, time.Now())
	if err != nil || !pending {
		t.Fatalf("late grant pending=%v err=%v", pending, err)
	}
	if got := postCapture(t, mgr.captures, "unknown-token", captureBody(*source.NativeSessionID, "resume", workDir)); got == http.StatusNoContent {
		t.Fatal("unknown token accepted")
	}
	if got := postCapture(t, mgr.captures, token, captureBody(*source.NativeSessionID, "resume", t.TempDir())); got != http.StatusConflict {
		t.Fatalf("wrong cwd status=%d", got)
	}
	if got := postCapture(t, mgr.captures, token, captureBody("not-a-native-id", "resume", workDir)); got != http.StatusConflict {
		t.Fatalf("invalid native id status=%d", got)
	}
	if got := postCapture(t, mgr.captures, token, captureBody(*source.NativeSessionID, "resume", workDir)); got != http.StatusNoContent {
		t.Fatalf("late exact resume status=%d", got)
	}
	if countAuditType(t, repo, source.ID, AuditIdentityConfirmed) != 1 {
		t.Fatal("missing exact resume confirmation")
	}
	// Source is provenance, not authority: a repeated fact stays a no-op
	// even when the provider labels its delivery differently.
	if got := postCapture(t, mgr.captures, token, captureBody(*source.NativeSessionID, "startup", workDir)); got != http.StatusNoContent || countAuditType(t, repo, source.ID, AuditIdentityConfirmed) != 1 {
		t.Fatalf("same identity with another source was not an idempotent replay: status=%d", got)
	}
	const rotated = "7c9e2f14-3b8a-4d55-9f61-2a0e5b7c1d38"
	stop := strings.Replace(captureBody(rotated, "current", workDir), "SessionStart", "Stop", 1)
	if got := postCapture(t, mgr.captures, token, stop); got != http.StatusNoContent {
		t.Fatalf("legitimate Stop rotation status=%d", got)
	}
	current, err := repo.Get(ctx, source.ID)
	if err != nil || current.NativeSessionID == nil || *current.NativeSessionID != rotated {
		t.Fatalf("rotation not recorded: %+v err=%v", current, err)
	}
	// A grant cannot follow a subsequently replaced generation or marker.
	if err := repo.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET generation = generation + 1, identity_marker = 'replaced' WHERE id = ?`, source.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := postCapture(t, mgr.captures, token, captureBody(*source.NativeSessionID, "resume", workDir)); got != http.StatusConflict {
		t.Fatalf("stale generation status=%d", got)
	}
}

func TestDeferredResumeConvergesBeforeFirstHook(t *testing.T) {
	mgr, repo, desc, projectID := deferredResumeEnv(t)
	ctx := context.Background()
	workDir := t.TempDir()
	created, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: provider.Codex, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	crashAndObserve(t, mgr, created)
	source, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	mgr.lifecycleHooks = &lifecycleTestHooks{resumeSpawned: func(*tmux.PaneInfo) { cancel() }}
	if _, err := mgr.resume(runCtx, source.ID, workDir); err == nil {
		t.Fatal("canceled recovery committed")
	}
	mgr.lifecycleHooks = nil
	token := readLaunchToken(t, desc.tokenFile)
	slot, err := repo.SuccessorSlot(ctx, source.ID)
	if err != nil || slot == nil || slot.CaptureState == nil || *slot.CaptureState != hookcapture.GrantPending {
		t.Fatalf("pre-convergence slot=%+v err=%v", slot, err)
	}
	if err := mgr.ConvergeRecoveryCandidates(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := repo.Get(ctx, source.ID)
	if err != nil || current.Status != StatusRunning || current.Generation != slot.CandidateGeneration {
		t.Fatalf("pending deferred candidate not committed: %+v err=%v", current, err)
	}
	if remaining, err := repo.SuccessorSlot(ctx, source.ID); err != nil || remaining != nil {
		t.Fatalf("remaining slot=%+v err=%v", remaining, err)
	}
	if got := postCapture(t, mgr.captures, token, captureBody(*source.NativeSessionID, "resume", workDir)); got != http.StatusNoContent {
		t.Fatalf("post-convergence hook status=%d", got)
	}
}

func TestEagerResumeStillRevokesMissingAttestation(t *testing.T) {
	mgr, repo, desc, projectID := deferredResumeEnv(t)
	desc.hook.Timing = provider.IdentityHookAtProcessLaunch
	ctx := context.Background()
	workDir := t.TempDir()
	created, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: provider.Codex, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	crashAndObserve(t, mgr, created)
	source, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := mgr.resume(ctx, source.ID, workDir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := repo.IdentityCapturePending(ctx, resumed.ID, resumed.Generation, *resumed.IdentityMarker, time.Now())
	if err != nil || pending {
		t.Fatalf("eager missing attestation stayed pending=%v err=%v", pending, err)
	}
	token := readLaunchToken(t, desc.tokenFile)
	if got := postCapture(t, mgr.captures, token, captureBody(*source.NativeSessionID, "resume", workDir)); got == http.StatusNoContent {
		t.Fatal("eager timed-out grant accepted late delivery")
	}
}

func TestResumeAttestationTimingIsExplicit(t *testing.T) {
	_, _, desc, _ := newAttestedResumeEnv(t)
	if resumeHookAfterFirstSubmission(desc) {
		t.Fatal("unknown timing became deferred")
	}
	desc.hook = &provider.IdentityHook{Timing: provider.IdentityHookAtProcessLaunch}
	if resumeHookAfterFirstSubmission(desc) {
		t.Fatal("eager timing became deferred")
	}
	desc.hook.Timing = provider.IdentityHookAfterFirstSubmission
	if !resumeHookAfterFirstSubmission(desc) {
		t.Fatal("declared deferred resume was not recognized")
	}
	if resumeHookAfterFirstSubmission(desc.fakeDescriptor) {
		t.Fatal("timing alone declared resume attestation")
	}
}

func TestDeferredResumeConvergenceRequiresExactProcessTuple(t *testing.T) {
	mgr, repo, desc, projectID := deferredResumeEnv(t)
	ctx := context.Background()
	workDir := t.TempDir()
	created, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: provider.Codex, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	crashAndObserve(t, mgr, created)
	source, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	mgr.lifecycleHooks = &lifecycleTestHooks{resumeSpawned: func(*tmux.PaneInfo) { cancel() }}
	if _, err := mgr.resume(runCtx, source.ID, workDir); err == nil {
		t.Fatal("canceled recovery committed")
	}
	mgr.lifecycleHooks = nil
	_ = readLaunchToken(t, desc.tokenFile)
	slot, err := repo.SuccessorSlot(ctx, source.ID)
	if err != nil || slot == nil || slot.CandidateStartTicks == nil {
		t.Fatalf("missing process tuple: %+v err=%v", slot, err)
	}
	if err := repo.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE session_successor_slots SET candidate_start_ticks = candidate_start_ticks + 1 WHERE session_id = ?`, source.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ConvergeRecoveryCandidates(ctx); err != nil {
		t.Fatal(err)
	}
	remaining, err := repo.SuccessorSlot(ctx, source.ID)
	if err != nil || remaining == nil || remaining.CaptureState == nil || *remaining.CaptureState != hookcapture.GrantPending {
		t.Fatalf("mismatched process was committed or revoked: %+v err=%v", remaining, err)
	}
	current, err := repo.Get(ctx, source.ID)
	if err != nil || current.Status == StatusRunning {
		t.Fatalf("mismatched process became running: %+v err=%v", current, err)
	}
	_, info, err := mgr.findCandidatePane(ctx, remaining)
	if err != nil || info.Dead {
		t.Fatalf("mismatched process was destroyed: %+v err=%v", info, err)
	}
}
