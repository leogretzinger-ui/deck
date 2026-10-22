package session

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/hookcapture"
	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/provider"
)

const rolledClaudeID = "9f3d2c1b-7a65-4e21-8b09-5d4c3b2a1908"

// trackedAssignedDescriptor is a claude-shaped fake: assigned identity plus
// the structured SessionStart tracking hook and a Deck-provisioned settings
// document. The fake CLI exposes its capture token so tests can post the
// documented hook payloads the real receiver would.
type trackedAssignedDescriptor struct {
	*fakeDescriptor
	tokenFile  string
	settings   []byte
	lastLaunch provider.LaunchSpec
	lastResume provider.ResumeSpec
}

func (d *trackedAssignedDescriptor) IdentityCaptureMechanisms() []provider.IdentityCaptureMechanism {
	return []provider.IdentityCaptureMechanism{
		provider.IdentityCaptureAssignedAtLaunch,
		provider.IdentityCaptureStructuredNativeHook,
	}
}

func (d *trackedAssignedDescriptor) HookSettingsDocument() []byte { return d.settings }
func (d *trackedAssignedDescriptor) HookSettingsFileName() string { return "hook-settings.json" }

func (d *trackedAssignedDescriptor) NewSessionArgs(spec provider.LaunchSpec) ([]string, error) {
	d.lastLaunch = spec
	script := `printf '%s' "$NOISEDECK_SESSION_HOOK_TOKEN" > "$1"; sleep 300`
	return []string{d.executable, "-c", script, "launch", d.tokenFile}, nil
}

func (d *trackedAssignedDescriptor) ResumeSessionArgs(spec provider.ResumeSpec) ([]string, error) {
	d.lastResume = spec
	script := `printf '%s' "$NOISEDECK_SESSION_HOOK_TOKEN" > "$1"; sleep 300`
	return []string{d.executable, "-c", script, "resume", d.tokenFile}, nil
}

func newTrackedAssignedEnv(t *testing.T) (*Manager, *Repository,
	*trackedAssignedDescriptor, *hookcapture.Service, string) {
	t.Helper()
	desc := &trackedAssignedDescriptor{
		fakeDescriptor: sleeperDescriptor(),
		tokenFile:      filepath.Join(t.TempDir(), "token"),
		settings:       []byte(`{"hooks":{"SessionStart":[]}}`),
	}
	mgr, repo, _, projectID := newTestEnv(t, desc)
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))
	captures, err := hookcapture.NewService(repo, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetHookCaptureDeps(captures, "http://127.0.0.1:8837")
	return mgr, repo, desc, captures, projectID
}

func claudeCaptureBody(id, source, cwd string) string {
	return captureBody(id, source, cwd)
}

func auditDetails(t *testing.T, repo *Repository, sessionID, eventType string) []string {
	t.Helper()
	rows, err := repo.db.Reader().Query(`SELECT details FROM audit_events
		WHERE session_id = ? AND event_type = ? ORDER BY id`, sessionID, eventType)
	if err != nil {
		t.Fatalf("query audit details: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatalf("scan audit details: %v", err)
		}
		details = append(details, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return details
}

// A pane reporting the id the row already holds changes nothing; a pane
// reporting a different one moves the row, whatever entry source carried the
// report, and the move is audited old to new.
func TestClaudeCaptureFollowsWhateverThePaneReports(t *testing.T) {
	mgr, repo, desc, captures, projectID := newTrackedAssignedEnv(t)
	ctx := context.Background()
	workDir := t.TempDir()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: workDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.NativeSessionID == nil || *s.NativeSessionID != s.ID {
		t.Fatalf("assigned birth identity = %v, want %s", s.NativeSessionID, s.ID)
	}
	token := readLaunchToken(t, desc.tokenFile)

	if got := postCapture(t, captures, token,
		claudeCaptureBody(s.ID, hookcapture.ExpectedSourceStartup, workDir)); got != http.StatusNoContent {
		t.Fatalf("confirming startup capture = %d, want 204", got)
	}
	confirmed, err := repo.Get(ctx, s.ID)
	if err != nil || confirmed.NativeSessionID == nil || *confirmed.NativeSessionID != s.ID {
		t.Fatalf("confirmation moved identity = %v err=%v", confirmed.NativeSessionID, err)
	}
	if got := countAuditType(t, repo, s.ID, AuditIdentityConfirmed); got != 1 {
		t.Fatalf("identity-confirmed audits = %d, want 1", got)
	}

	if got := postCapture(t, captures, token,
		claudeCaptureBody(rolledClaudeID, "fork", workDir)); got != http.StatusNoContent {
		t.Fatalf("fork-source capture = %d, want 204", got)
	}
	rolled, err := repo.Get(ctx, s.ID)
	if err != nil || rolled.NativeSessionID == nil || *rolled.NativeSessionID != rolledClaudeID {
		t.Fatalf("fork capture did not follow identity: %v err=%v", rolled.NativeSessionID, err)
	}
	details := auditDetails(t, repo, s.ID, AuditIdentityCaptured)
	if len(details) != 1 {
		t.Fatalf("identity-captured audits = %d, want 1", len(details))
	}
	want := "from_native_id_sha256=" + hookcapture.NativeIDHash(s.ID) +
		" to_native_id_sha256=" + hookcapture.NativeIDHash(rolledClaudeID)
	if !strings.Contains(details[0], want) {
		t.Fatalf("move audit = %q, want it to state %q", details[0], want)
	}
	if strings.Contains(details[0], rolledClaudeID) {
		t.Fatalf("move audit leaked the raw native id: %q", details[0])
	}

	// The same fork event replayed is idempotent; a NEW clear-source report
	// moves the identity again on the same long-lived grant.
	if got := postCapture(t, captures, token,
		claudeCaptureBody(rolledClaudeID, "fork", workDir)); got != http.StatusNoContent {
		t.Fatalf("fork replay = %d, want 204", got)
	}
	if got := countAuditType(t, repo, s.ID, AuditIdentityCaptured); got != 1 {
		t.Fatal("the replayed fork wrote a second identity-captured audit")
	}
	const clearedID = "1b2a3c4d-5e6f-4a1b-9c8d-7e6f5a4b3c2d"
	if got := postCapture(t, captures, token,
		claudeCaptureBody(clearedID, "clear", workDir)); got != http.StatusNoContent {
		t.Fatalf("clear-source capture = %d, want 204", got)
	}
	cleared, err := repo.Get(ctx, s.ID)
	if err != nil || cleared.NativeSessionID == nil || *cleared.NativeSessionID != clearedID {
		t.Fatalf("clear capture did not follow identity: %v err=%v", cleared.NativeSessionID, err)
	}
}

// Fencing decides WHICH row an event belongs to: session, generation,
// identity marker, provider, and the durable token. A grant minted for a
// superseded generation matches no row, so it can never overwrite the live
// generation's identity — it is refused, and the refusal says why.
func TestClaudeStaleGenerationCaptureCannotOverwriteSuccessor(t *testing.T) {
	mgr, repo, desc, captures, projectID := newTrackedAssignedEnv(t)
	ctx := context.Background()
	workDir := t.TempDir()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: workDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	token := readLaunchToken(t, desc.tokenFile)
	if got := postCapture(t, captures, token,
		claudeCaptureBody(s.ID, hookcapture.ExpectedSourceStartup, workDir)); got != http.StatusNoContent {
		t.Fatalf("confirming startup capture = %d, want 204", got)
	}

	// Simulate a committed successor: the row moved on to a new generation and
	// marker while the old pane still holds its original grant credential.
	if err := repo.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE sessions
			SET generation = generation + 1, identity_marker = 'successor-marker'
			WHERE id = ?`, s.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if got := postCapture(t, captures, token,
		claudeCaptureBody(rolledClaudeID, "fork", workDir)); got != http.StatusConflict {
		t.Fatalf("stale-generation report = %d, want 409", got)
	}
	current, err := repo.Get(ctx, s.ID)
	if err != nil || current.NativeSessionID == nil || *current.NativeSessionID != s.ID {
		t.Fatalf("stale grant moved the successor's identity: %v err=%v",
			current.NativeSessionID, err)
	}
	if got := countAuditType(t, repo, s.ID, AuditIdentityCaptured); got != 0 {
		t.Fatalf("stale report wrote %d identity-captured audits, want 0", got)
	}
	refusals := auditDetails(t, repo, s.ID, AuditIdentityCaptureRefused)
	if len(refusals) != 1 || !strings.Contains(refusals[0], "reason=stale_generation") {
		t.Fatalf("refusal audits = %q, want the stale generation named", refusals)
	}
}

// After a move, recovery resumes the conversation the pane last reported —
// the moved id, not the birth uuid.
func TestClaudeResumeUsesTheLastReportedIdentity(t *testing.T) {
	mgr, repo, desc, captures, projectID := newTrackedAssignedEnv(t)
	ctx := context.Background()
	workDir := t.TempDir()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: workDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	token := readLaunchToken(t, desc.tokenFile)
	if got := postCapture(t, captures, token,
		claudeCaptureBody(s.ID, hookcapture.ExpectedSourceStartup, workDir)); got != http.StatusNoContent {
		t.Fatalf("confirming startup capture = %d, want 204", got)
	}
	if got := postCapture(t, captures, token,
		claudeCaptureBody(rolledClaudeID, "fork", workDir)); got != http.StatusNoContent {
		t.Fatalf("fork-source capture = %d, want 204", got)
	}

	created, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	crashAndObserve(t, mgr, created)
	recovered, err := mgr.resume(ctx, s.ID, t.TempDir())
	if err != nil {
		t.Fatalf("resume after roll: %v", err)
	}
	if recovered.Generation != s.Generation+1 {
		t.Fatalf("recovered generation = %d, want %d", recovered.Generation, s.Generation+1)
	}
	if desc.lastResume.NativeSessionID != rolledClaudeID {
		t.Fatalf("resume argv identity = %q, want the last reported %q",
			desc.lastResume.NativeSessionID, rolledClaudeID)
	}
}

// The launch provisions the descriptor's hook settings document inside the
// generation's Deck-owned directory and hands the path to argv construction;
// recovery does the same for the successor generation.
func TestProvisionedHookSettingsAreWrittenPerGeneration(t *testing.T) {
	mgr, repo, desc, _, projectID := newTrackedAssignedEnv(t)
	if err := mgr.SetTemporaryRoot(t.TempDir()); err != nil {
		t.Fatalf("SetTemporaryRoot: %v", err)
	}
	ctx := context.Background()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	launchPath := desc.lastLaunch.HookSettingsPath
	if launchPath == "" || filepath.Base(launchPath) != "hook-settings.json" {
		t.Fatalf("launch hook settings path = %q", launchPath)
	}
	raw, err := os.ReadFile(launchPath)
	if err != nil || !slices.Equal(raw, desc.settings) {
		t.Fatalf("provisioned settings content = %q err=%v", raw, err)
	}
	info, err := os.Stat(launchPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("provisioned settings mode = %v err=%v", info.Mode(), err)
	}

	created, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	crashAndObserve(t, mgr, created)
	if _, err := mgr.resume(ctx, s.ID, t.TempDir()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	resumePath := desc.lastResume.HookSettingsPath
	if resumePath == "" || resumePath == launchPath {
		t.Fatalf("resume hook settings path = %q (launch was %q)", resumePath, launchPath)
	}
	raw, err = os.ReadFile(resumePath)
	if err != nil || !slices.Equal(raw, desc.settings) {
		t.Fatalf("successor settings content = %q err=%v", raw, err)
	}
}

// The user opens a session, then switches it to another conversation
// with claude's in-TUI /resume. Claude announces the move as SessionStart
// source "resume" carrying the other conversation's id; Deck must follow it,
// or a restart resumes the conversation the pane was launched with and the
// user's work appears lost.
func TestClaudeTUIResumeCaptureFollowsIdentity(t *testing.T) {
	mgr, repo, desc, captures, projectID := newTrackedAssignedEnv(t)
	ctx := context.Background()
	workDir := t.TempDir()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: workDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	token := readLaunchToken(t, desc.tokenFile)
	if got := postCapture(t, captures, token,
		claudeCaptureBody(s.ID, hookcapture.ExpectedSourceStartup, workDir)); got != http.StatusNoContent {
		t.Fatalf("confirming startup capture = %d, want 204", got)
	}

	if got := postCapture(t, captures, token,
		claudeCaptureBody(rolledClaudeID, hookcapture.ExpectedSourceResume, workDir)); got != http.StatusNoContent {
		t.Fatalf("in-TUI resume capture = %d, want 204", got)
	}
	moved, err := repo.Get(ctx, s.ID)
	if err != nil || moved.NativeSessionID == nil || *moved.NativeSessionID != rolledClaudeID {
		t.Fatalf("resume capture did not follow identity: %v err=%v", moved.NativeSessionID, err)
	}

	// A resume-source event carrying the id the pane already holds is the
	// CLI flag's own report, and re-attests rather than moving anything.
	if got := postCapture(t, captures, token,
		claudeCaptureBody(rolledClaudeID, hookcapture.ExpectedSourceResume, workDir)); got != http.StatusNoContent {
		t.Fatalf("re-attesting resume capture = %d, want 204", got)
	}
	held, err := repo.Get(ctx, s.ID)
	if err != nil || held.NativeSessionID == nil || *held.NativeSessionID != rolledClaudeID {
		t.Fatalf("re-attestation disturbed identity: %v err=%v", held.NativeSessionID, err)
	}
}
