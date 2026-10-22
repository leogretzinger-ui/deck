package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/hookcapture"
	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/project"
	"github.com/noisefloor-oss/deck/internal/provider"
)

func storeCaptureBody(id, source, cwd, transcript string) string {
	return `{"hook_event_name":"SessionStart","session_id":"` + id +
		`","cwd":"` + cwd + `","source":"` + source +
		`","transcript_path":"` + transcript + `"}`
}

func claudeLedger(root, id string) string {
	return filepath.Join(root, "projects", "-workspace-app", id+".jsonl")
}

// Every accepted claude capture records the store root derived from the
// reported ledger, and a move rewrites it with the identity it belongs to.
func TestClaudeCaptureRecordsAndMovesTheConversationStore(t *testing.T) {
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

	birthLedger := claudeLedger("/home/op/.claude", s.ID)
	if got := postCapture(t, captures, token, storeCaptureBody(
		s.ID, hookcapture.ExpectedSourceStartup, workDir, birthLedger)); got != http.StatusNoContent {
		t.Fatalf("confirming startup capture = %d, want 204", got)
	}
	confirmed, err := repo.Get(ctx, s.ID)
	if err != nil || confirmed.NativeStoreRoot == nil {
		t.Fatalf("confirmed capture recorded no store: %+v err=%v", confirmed, err)
	}
	if *confirmed.NativeStoreRoot != "/home/op/.claude" {
		t.Fatalf("store root = %q, want the root of the reported ledger",
			*confirmed.NativeStoreRoot)
	}

	rolledLedger := claudeLedger("/srv/claude-store", rolledClaudeID)
	if got := postCapture(t, captures, token, storeCaptureBody(
		rolledClaudeID, "fork", workDir, rolledLedger)); got != http.StatusNoContent {
		t.Fatalf("fork-source capture = %d, want 204", got)
	}
	rolled, err := repo.Get(ctx, s.ID)
	if err != nil || rolled.NativeSessionID == nil || *rolled.NativeSessionID != rolledClaudeID {
		t.Fatalf("fork capture did not follow identity: %+v err=%v", rolled.NativeSessionID, err)
	}
	if rolled.NativeStoreRoot == nil || *rolled.NativeStoreRoot != "/srv/claude-store" {
		t.Fatalf("the move did not rewrite the store record: %v", rolled.NativeStoreRoot)
	}
}

// storePinDescriptor is a store-pinning fake: an assigned-identity provider
// whose resume argv writes the CLAUDE_CONFIG_DIR it was launched with, so the
// tests read exactly what the pane's environment carried.
type storePinDescriptor struct {
	*fakeDescriptor
	currentRoot string
	envFile     string
	resumes     int
}

func (d *storePinDescriptor) CurrentStoreRoot() (string, error) { return d.currentRoot, nil }
func (d *storePinDescriptor) StorePinEnvironment(root string) []string {
	return []string{"CLAUDE_CONFIG_DIR=" + root}
}

func (d *storePinDescriptor) NewSessionArgs(spec provider.LaunchSpec) ([]string, error) {
	return append([]string{d.executable}, d.args...), nil
}

func (d *storePinDescriptor) ResumeSessionArgs(provider.ResumeSpec) ([]string, error) {
	d.resumes++
	script := `printf '%s' "${CLAUDE_CONFIG_DIR-}" > "$1"; sleep 300`
	return []string{d.executable, "-c", script, "resume", d.envFile}, nil
}

func newStorePinEnv(t *testing.T) (*Manager, *Repository, *storePinDescriptor, string) {
	t.Helper()
	desc := &storePinDescriptor{
		fakeDescriptor: sleeperDescriptor(),
		currentRoot:    "/srv/current-store",
		envFile:        filepath.Join(t.TempDir(), "pane-env"),
	}
	mgr, repo, _, projectID := newTestEnv(t, desc)
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))
	return mgr, repo, desc, projectID
}

func recordStore(t *testing.T, repo *Repository, id, root string) {
	t.Helper()
	if err := repo.db.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE sessions
			SET native_store_root = ? WHERE id = ?`, root, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// A recorded store that matches the current resolution pins nothing: the
// divergence gate fires only on real divergence.
func TestResumeDoesNotPinWhenTheStoreMatches(t *testing.T) {
	mgr, repo, desc, projectID := newStorePinEnv(t)
	ctx := context.Background()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	crashAndObserve(t, mgr, s)
	recordStore(t, repo, s.ID, desc.currentRoot)

	if _, err := mgr.resume(ctx, s.ID, t.TempDir()); err != nil {
		t.Fatalf("resume with a matching store: %v", err)
	}
	if got := readTestFile(t, desc.envFile); got != "" {
		t.Fatalf("matching store was pinned anyway: CLAUDE_CONFIG_DIR=%q", got)
	}
	for _, detail := range auditDetails(t, repo, s.ID, AuditResumeAttempt) {
		if strings.Contains(detail, "resume pinned") {
			t.Fatalf("a matching store wrote a pin audit: %q", detail)
		}
	}
}

// A recorded store that still exists but diverges from the current resolution
// pins the resume pane's environment to the recorded root, audited.
func TestResumePinsTheDivergedRecordedStore(t *testing.T) {
	mgr, repo, desc, projectID := newStorePinEnv(t)
	ctx := context.Background()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	crashAndObserve(t, mgr, s)
	recordedRoot := t.TempDir()
	recordStore(t, repo, s.ID, recordedRoot)

	if _, err := mgr.resume(ctx, s.ID, t.TempDir()); err != nil {
		t.Fatalf("resume with a diverged store: %v", err)
	}
	if got := readTestFile(t, desc.envFile); got != recordedRoot {
		t.Fatalf("pane CLAUDE_CONFIG_DIR = %q, want the recorded root %q", got, recordedRoot)
	}
	pinned := false
	for _, detail := range auditDetails(t, repo, s.ID, AuditResumeAttempt) {
		if strings.Contains(detail, "resume pinned to the conversation's recorded store "+recordedRoot) {
			pinned = true
		}
	}
	if !pinned {
		t.Fatal("the pin left no audit record")
	}
}

// A store that is gone by name cannot answer for the conversation, so nothing
// is launched: the resume is refused with the store named, and the row keeps
// its identity and its store record for the day the volume comes back. The
// sweep stops asking; a fresh conversation is the user's explicit choice.
func TestStoreGoneRefusesTheResumeAndKeepsTheRecord(t *testing.T) {
	mgr, repo, desc, projectID := newStorePinEnv(t)
	ctx := context.Background()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	birthID := *s.NativeSessionID
	crashAndObserve(t, mgr, s)
	goneRoot := filepath.Join(t.TempDir(), "moved-away")
	recordStore(t, repo, s.ID, goneRoot)

	_, _, err = mgr.Recover(ctx, s.ID)
	var gone *ConversationStoreGoneError
	if !errors.Is(err, ErrResumeRefusedByProvider) || !errors.As(err, &gone) || gone.Path != goneRoot {
		t.Fatalf("Recover over a gone store = %v, want the refusal naming %s", err, goneRoot)
	}
	if desc.resumes != 0 {
		t.Fatalf("a conversation whose store is gone was resumed %d times", desc.resumes)
	}
	row := requireKeptIdentity(t, repo, s.ID, birthID)
	if row.NativeStoreRoot == nil || *row.NativeStoreRoot != goneRoot {
		t.Fatalf("store record = %v, want %s kept", row.NativeStoreRoot, goneRoot)
	}
	if row.Status != StatusExited || row.Generation != s.Generation || row.ResumeAttempts != 1 {
		t.Fatalf("row = status %s gen %d attempts %d, want the same generation with the sweep stopped",
			row.Status, row.Generation, row.ResumeAttempts)
	}
	named := false
	for _, detail := range auditDetails(t, repo, s.ID, AuditResumeAttempt) {
		if strings.Contains(detail, "recorded store "+goneRoot+" is gone") {
			named = true
		}
	}
	if !named {
		t.Fatal("the gone store was not named in the audit")
	}
	// The row is presented for the user to decide, with the store named
	// as what stands in the way, not hidden as ended.
	presented, err := mgr.Present(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if presented.PresentationState != PresentationResumeEnded ||
		presented.RecoveryRefusal != RecoveryRefusalStoreGone {
		t.Fatalf("presentation = %s refusal = %q, want resume_ended by the gone store",
			presented.PresentationState, presented.RecoveryRefusal)
	}
}

// The refusal names the gone store in the response sentence — a specific
// dead end the user can act on, not a generic one.
func TestStoreGoneSentenceReachesTheRefusalEnvelope(t *testing.T) {
	mgr, repo, _, projectID := newStorePinEnv(t)
	h := NewHandler(mgr, repo, project.NewRepository(repo.db), zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	ctx := context.Background()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	crashAndObserve(t, mgr, s)
	goneRoot := filepath.Join(t.TempDir(), "moved-away")
	recordStore(t, repo, s.ID, goneRoot)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/sessions/"+s.ID+"/recover", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("refused recover = %d body %s, want 409", rec.Code, rec.Body.String())
	}
	var refusal struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
		t.Fatalf("decode refusal envelope: %v", err)
	}
	if refusal.Error != "resume_refused" ||
		!strings.Contains(refusal.Message, "the store this conversation lived in ("+goneRoot+") is gone") {
		t.Fatalf("refusal = %+v, want resume_refused naming the gone store", refusal)
	}
}
