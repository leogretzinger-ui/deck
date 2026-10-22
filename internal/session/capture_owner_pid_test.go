package session

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/hookcapture"
)

func pidCaptureBody(id, source, cwd, pid string) string {
	body := `{"hook_event_name":"SessionStart","session_id":"` + id +
		`","cwd":"` + cwd + `","source":"` + source + `"`
	if pid != "" {
		body += `,"pid":"` + pid + `"`
	}
	return body + `}`
}

func registeredCapture(t *testing.T, repo *Repository, sessionID, provider, workDir string,
	generation int, marker string) (*hookcapture.Service, string) {
	t.Helper()
	service, err := hookcapture.NewService(repo, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.RegisterFenced(context.Background(), sessionID, provider, workDir,
		generation, marker)
	if err != nil {
		t.Fatal(err)
	}
	return service, token
}

// A capture that names a reporting process may only speak for the process
// this generation recorded as its own. An omp subagent inherits the pane's
// capture token and fires its own session_start with a fresh conversation
// id; before this gate the parent row rolled to the child's id, which never
// persisted, and the next resume was refused for an id the provider never
// saw. The pane's own process still moves the identity as before.
func TestSubagentCaptureDoesNotRollTheParentIdentity(t *testing.T) {
	repo, row, workDir := startingCaptureFixture(t)
	ctx := context.Background()
	service, token := registeredCapture(t, repo, row.ID, string(row.Provider), workDir,
		row.Generation, *row.IdentityMarker)

	const childPID = "2"
	if row.PanePID == nil || *row.PanePID <= 2 {
		t.Fatalf("fixture pane pid = %v, want a real recorded pane process", row.PanePID)
	}
	panePID := strconv.Itoa(*row.PanePID)

	// A child's session_start arrives on the pane's inherited token.
	if got := postCapture(t, service, token,
		pidCaptureBody(capturedCodexID, hookcapture.ExpectedSourceStartup, workDir, childPID)); got != http.StatusConflict {
		t.Fatalf("child capture = %d, want 409", got)
	}
	stored, err := repo.Get(ctx, row.ID)
	if err != nil || stored.NativeSessionID != nil {
		t.Fatalf("native id after child capture = %v err=%v, want the row untouched", stored.NativeSessionID, err)
	}
	if got := countAuditType(t, repo, row.ID, AuditIdentityCaptureRefused); got != 1 {
		t.Fatalf("capture-refused audits = %d, want the child refusal recorded once", got)
	}
	if !strings.Contains(strings.Join(auditDetails(t, repo, row.ID, AuditIdentityCaptureRefused), "\n"),
		"reason=reporting_process") {
		t.Fatal("the refusal does not name the reporting process")
	}

	// The pane's own process still reports and moves the identity.
	if got := postCapture(t, service, token,
		pidCaptureBody(capturedCodexID, hookcapture.ExpectedSourceStartup, workDir, panePID)); got != http.StatusNoContent {
		t.Fatalf("pane capture = %d, want 204", got)
	}
	stored, err = repo.Get(ctx, row.ID)
	if err != nil || stored.NativeSessionID == nil || *stored.NativeSessionID != capturedCodexID {
		t.Fatalf("native id after pane capture = %v err=%v, want the conversation recorded", stored.NativeSessionID, err)
	}

	// A later move is the pane speaking for itself; a child naming yet
	// another conversation still changes nothing.
	const movedID = "22345678-1234-4abc-8abc-123456789abc"
	if got := postCapture(t, service, token,
		pidCaptureBody(movedID, hookcapture.ExpectedSourceStartup, workDir, childPID)); got != http.StatusConflict {
		t.Fatalf("second child capture = %d, want 409", got)
	}
	if got := postCapture(t, service, token,
		pidCaptureBody(movedID, hookcapture.ExpectedSourceStartup, workDir, panePID)); got != http.StatusNoContent {
		t.Fatalf("pane move = %d, want 204", got)
	}
	stored, err = repo.Get(ctx, row.ID)
	if err != nil || stored.NativeSessionID == nil || *stored.NativeSessionID != movedID {
		t.Fatalf("native id after pane move = %v err=%v, want the row to follow its pane", stored.NativeSessionID, err)
	}
}

// While a recovery has a candidate in flight, the generation's owning
// process is the pid the successor slot pinned at spawn — the resumed pane
// reports through it before the candidate commits. A hook that reports no
// pid at all is judged exactly as before: the gate only ever narrows what a
// NAMED process may claim.
func TestRecoveryCandidateCaptureAcceptsTheCandidateProcess(t *testing.T) {
	mgr, repo, _, projectID := newTestEnv(t, sleeperDescriptor())

	ctx := context.Background()
	source := createInterrupted(t, mgr, projectID)
	marker := "77777777-7777-4777-8777-777777777777"
	workDir := t.TempDir()
	service, err := hookcapture.NewService(repo, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// The flight mints the grant, admits it with the successor slot, and
	// pins the spawned pane's process — exactly the order recovery uses.
	credential, grant, err := service.PrepareLaunchGrant(source.ID, string(source.Provider),
		workDir, source.Generation+1, marker)
	if err != nil {
		t.Fatalf("PrepareLaunchGrant: %v", err)
	}
	slot, err := repo.BeginSuccessorSlot(ctx, source, marker, &grant, "")
	if err != nil {
		t.Fatalf("BeginSuccessorSlot: %v", err)
	}
	const candidatePID = 424242
	const reportedID = "32345678-1234-4abc-8abc-123456789abc"
	const movedID = "22345678-1234-4abc-8abc-123456789abc"

	// The window between admission and the spawn's recording: the slot
	// exists, no candidate pid is pinned, and the row still carries the
	// previous generation's tuple. A named capture here is judged against
	// nothing — omp's hook does not retry a 4xx, and refusing would
	// permanently lose a legitimate attestation.
	if got := postCapture(t, service, credential.Token,
		pidCaptureBody(reportedID, hookcapture.ExpectedSourceStartup, workDir,
			"555555")); got != http.StatusNoContent {
		t.Fatalf("pre-record capture = %d, want 204", got)
	}

	if err := repo.RecordCandidateProcess(ctx, slot, candidatePID, candidatePID, 999999); err != nil {
		t.Fatalf("RecordCandidateProcess: %v", err)
	}
	token := credential.Token
	// The resumed pane reports through the candidate pid before commit.
	if got := postCapture(t, service, token,
		pidCaptureBody(reportedID, hookcapture.ExpectedSourceStartup, workDir,
			strconv.Itoa(candidatePID))); got != http.StatusNoContent {
		t.Fatalf("candidate capture = %d, want 204", got)
	}
	stored, err := repo.Get(ctx, source.ID)
	if err != nil || stored.NativeSessionID == nil || *stored.NativeSessionID != reportedID {
		t.Fatalf("native id after candidate capture = %v err=%v, want the conversation recorded", stored.NativeSessionID, err)
	}

	// A different named process is refused; an unnamed one is judged exactly
	// as before — the gate only ever narrows what a NAMED process may claim.
	if got := postCapture(t, service, token,
		pidCaptureBody(movedID, hookcapture.ExpectedSourceStartup, workDir, "2")); got != http.StatusConflict {
		t.Fatalf("stranger capture = %d, want 409", got)
	}
	if got := postCapture(t, service, token,
		captureBody(movedID, hookcapture.ExpectedSourceStartup, workDir)); got != http.StatusNoContent {
		t.Fatalf("unnamed capture = %d, want 204", got)
	}
	stored, err = repo.Get(ctx, source.ID)
	if err != nil || stored.NativeSessionID == nil || *stored.NativeSessionID != movedID {
		t.Fatalf("native id after unnamed move = %v err=%v, want the row to follow its pane", stored.NativeSessionID, err)
	}
}
