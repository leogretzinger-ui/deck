package session

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/hookcapture"
	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/provider"
)

// ompShapedDescriptor models omp's measured recovery surface: a
// retrieved-identity provider whose resumed process fires the startup
// callback a fresh launch fires and redelivers the resumed id, with no
// resume-shaped attestation anywhere.
type ompShapedDescriptor struct {
	*fakeDescriptor
	tokenFile string
	resumes   int
}

func (d *ompShapedDescriptor) IdentityCaptureMechanisms() []provider.IdentityCaptureMechanism {
	return []provider.IdentityCaptureMechanism{provider.IdentityCaptureStructuredNativeHook}
}

func (d *ompShapedDescriptor) ResumeCaptureHookSource() string { return "startup" }

func (d *ompShapedDescriptor) NewSessionArgs(provider.LaunchSpec) ([]string, error) {
	script := `printf '%s' "$NOISEDECK_SESSION_HOOK_TOKEN" > "$1"; sleep 300`
	return []string{d.executable, "-c", script, "launch", d.tokenFile}, nil
}

func (d *ompShapedDescriptor) ResumeSessionArgs(spec provider.ResumeSpec) ([]string, error) {
	if spec.WorkDir != "" {
		d.resumes++
	}
	script := `printf '%s' "$NOISEDECK_SESSION_HOOK_TOKEN" > "$1"; sleep 300`
	return []string{d.executable, "-c", script, "resume", d.tokenFile}, nil
}

func latestGrantHash(t *testing.T, repo *Repository, sessionID string, generation int) string {
	t.Helper()
	var hash string
	err := repo.db.Reader().QueryRow(
		`SELECT token_sha256 FROM provider_identity_capture_grants
		 WHERE session_id = ? AND generation = ?`, sessionID, generation).Scan(&hash)
	if err != nil {
		t.Fatalf("read capture grant for generation %d: %v", generation, err)
	}
	return hash
}

// A resumed omp pane must actually deliver identity: the resume flight
// carries a generation-bound startup grant and the hook environment, so the
// startup callback the resumed process fires consumes that grant and
// confirms the recorded id. An argv change alone would leave the capture
// channel dead, which is the failure this test exists to catch.
func TestOmpShapedResumeDeliversIdentityThroughStartupCapture(t *testing.T) {
	desc := &ompShapedDescriptor{
		fakeDescriptor: sleeperDescriptor(),
		tokenFile:      filepath.Join(t.TempDir(), "token"),
	}
	desc.contract = provider.IdentityRetrieved
	desc.hook = &provider.IdentityHook{
		Event: "SessionStart", SessionIDField: "session_id",
		ReceiverPath: provider.HookReceiverPath,
		Timing:       provider.IdentityHookAtProcessLaunch,
	}
	mgr, repo, _, projectID := newTestEnv(t, desc)
	workDir := t.TempDir()
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(),
		func(context.Context, string) (string, error) { return workDir, nil })
	captures, err := hookcapture.NewService(repo, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetHookCaptureDeps(captures, "http://127.0.0.1:8837")

	ctx := context.Background()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: workDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The launch's startup capture gives the row the recorded id the same
	// way the real hook would.
	const nativeID = "01a0ffc5-f023-7000-be0b-bc1d8f7510a9"
	if err := repo.AcceptIdentityCapture(ctx, latestGrantHash(t, repo, s.ID, s.Generation),
		hookcapture.Event{HookEventName: "SessionStart", SessionID: nativeID,
			CWD: workDir, Source: hookcapture.ExpectedSourceStartup}, time.Now()); err != nil {
		t.Fatalf("launch identity delivery: %v", err)
	}
	crashAndObserve(t, mgr, s)

	type recoverResult struct {
		session *Session
		outcome RecoveryOutcome
		err     error
	}
	res := make(chan recoverResult, 1)
	go func() {
		session, outcome, err := mgr.Recover(ctx, s.ID)
		res <- recoverResult{session, outcome, err}
	}()

	// The resumed pane fires its startup callback and redelivers the same
	// id; deliver it against the resume generation's own grant. The grant
	// row lands with the admission, which also bumps the row's generation,
	// so the poll pins the source generation up front.
	deadline := time.Now().Add(10 * time.Second)
	var resumeHash string
	var candidatePID sql.NullInt64
	for {
		if err := repo.db.Reader().QueryRow(
			`SELECT grant.token_sha256, slot.candidate_pane_pid
			 FROM provider_identity_capture_grants grant
			 JOIN session_successor_slots slot ON slot.session_id = grant.session_id
			 WHERE grant.session_id = ? AND grant.generation = ?`,
			s.ID, s.Generation+1).Scan(&resumeHash, &candidatePID); err == nil &&
			candidatePID.Valid && candidatePID.Int64 > 1 {
			break
		}
		if time.Now().After(deadline) {
			select {
			case got := <-res:
				t.Fatalf("the resume flight never prepared its capture grant; Recover returned err=%v outcome=%v", got.err, got.outcome)
			default:
			}
			t.Fatal("the resume flight never prepared its capture grant")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !candidatePID.Valid || candidatePID.Int64 <= 1 {
		t.Fatal("the resume flight never pinned its candidate process")
	}
	// The real hook reports the pane's own process id; the delivery must be
	// accepted through the candidate the slot pinned.
	if err := repo.AcceptIdentityCapture(ctx, resumeHash,
		hookcapture.Event{HookEventName: "SessionStart", SessionID: nativeID,
			CWD: workDir, Source: hookcapture.ExpectedSourceStartup,
			PID: strconv.FormatInt(candidatePID.Int64, 10)}, time.Now()); err != nil {
		t.Fatalf("resume identity delivery: %v", err)
	}

	got := <-res
	if got.err != nil {
		t.Fatalf("Recover: %v", got.err)
	}
	if got.outcome != RecoveryOutcomeResumed {
		t.Fatalf("outcome = %v, want a resumed conversation", got.outcome)
	}
	row, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != StatusRunning || row.Generation != s.Generation+1 || !row.GenerationResumed {
		t.Fatalf("row = status %s gen %d resumed=%v, want the resumed generation running",
			row.Status, row.Generation, row.GenerationResumed)
	}
	if row.NativeSessionID == nil || *row.NativeSessionID != nativeID {
		t.Fatalf("native id = %v, want the redelivered id kept current", row.NativeSessionID)
	}
	// The redelivery is confirmed, not re-captured: the row already carried
	// this id, and the audit trail names the confirmation.
	found := false
	for _, typ := range auditTypes(t, repo, s.ID) {
		if typ == AuditIdentityConfirmed {
			found = true
		}
	}
	if !found {
		t.Fatal("the redelivered identity was not confirmed in the audit trail")
	}
}
