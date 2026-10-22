package session

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/hookcapture"
	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/provider"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

type attestedResumeDescriptor struct {
	*fakeDescriptor
	tokenFile string
	idFile    string
}

func (d *attestedResumeDescriptor) IdentityCaptureMechanisms() []provider.IdentityCaptureMechanism {
	return []provider.IdentityCaptureMechanism{provider.IdentityCaptureStructuredNativeHook}
}

func (d *attestedResumeDescriptor) ExactResumeHookSource() string {
	return hookcapture.ExpectedSourceResume
}

func (d *attestedResumeDescriptor) ResumeSessionArgs(spec provider.ResumeSpec) ([]string, error) {
	if spec.NativeSessionID == "" {
		return nil, errors.New("missing exact native identity")
	}
	script := `printf '%s' "$NOISEDECK_SESSION_HOOK_TOKEN" > "$1"; ` +
		`printf '%s' "$2" > "$3"; sleep 300`
	return []string{d.executable, "-c", script, "resume", d.tokenFile,
		spec.NativeSessionID, d.idFile}, nil
}

func newAttestedResumeEnv(t *testing.T) (*Manager, *Repository,
	*attestedResumeDescriptor, string) {
	t.Helper()
	base := sleeperDescriptor()
	base.name = provider.Codex
	desc := &attestedResumeDescriptor{
		fakeDescriptor: base,
		tokenFile:      t.TempDir() + "/token",
		idFile:         t.TempDir() + "/native-id",
	}
	mgr, repo, _, projectID := newTestEnv(t, desc)
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))
	captures, err := hookcapture.NewService(repo, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetHookCaptureDeps(captures, "http://127.0.0.1:8837")
	return mgr, repo, desc, projectID
}

func TestCaptureGrantCommitFailurePreventsSuccessorSpawn(t *testing.T) {
	mgr, repo, desc, projectID := newAttestedResumeEnv(t)
	ctx := context.Background()
	created, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Codex, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	crashAndObserve(t, mgr, created)
	source, _ := repo.Get(ctx, created.ID)
	if err := repo.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER fail_capture_grant
			BEFORE INSERT ON provider_identity_capture_grants
			BEGIN SELECT RAISE(ABORT, 'injected grant commit failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.resume(ctx, source.ID, t.TempDir()); err == nil {
		t.Fatal("recovery succeeded despite grant commit failure")
	}
	current, _ := repo.Get(ctx, source.ID)
	if current.Generation != source.Generation || current.ResumeAttempts != 0 {
		t.Fatalf("failed grant mutated source: generation=%d attempts=%d",
			current.Generation, current.ResumeAttempts)
	}
	if slot, err := repo.SuccessorSlot(ctx, source.ID); err != nil || slot != nil {
		t.Fatalf("failed grant left slot=%+v err=%v", slot, err)
	}
	if _, err := os.Stat(desc.tokenFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider spawned before grant commit: token file stat=%v", err)
	}
	sessions, err := mgr.tmux.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		if strings.Contains(session.Name, "-r-") {
			t.Fatalf("successor %q spawned before durable grant", session.Name)
		}
	}
}

func TestProvisionalResumeSurvivesServiceRestartThenCommits(t *testing.T) {
	mgr, repo, desc, projectID := newAttestedResumeEnv(t)
	ctx := context.Background()
	created, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Codex, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	crashAndObserve(t, mgr, created)
	source, _ := repo.Get(ctx, created.ID)

	runCtx, cancel := context.WithCancel(ctx)
	mgr.lifecycleHooks = &lifecycleTestHooks{resumeSpawned: func(*tmux.PaneInfo) { cancel() }}
	if _, err := mgr.resume(runCtx, source.ID, t.TempDir()); err == nil {
		t.Fatal("canceled pre-hook recovery unexpectedly committed")
	}
	mgr.lifecycleHooks = nil
	token := readLaunchToken(t, desc.tokenFile)
	if got := strings.TrimSpace(readTestFile(t, desc.idFile)); got != *source.NativeSessionID {
		t.Fatalf("resume argv native id = %q, want exact %q", got, *source.NativeSessionID)
	}
	slot, err := repo.SuccessorSlot(ctx, source.ID)
	if err != nil || slot == nil || slot.CaptureState == nil ||
		*slot.CaptureState != hookcapture.GrantPending {
		t.Fatalf("restart boundary slot = %+v err=%v", slot, err)
	}

	// Reconstruct both stateless layers as a Go service restart would. No raw
	// token is recovered from memory; the provider retries the one it retained.
	captures2, err := hookcapture.NewService(repo, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mgr2 := NewManager(repo, mgr.tmux,
		map[provider.Name]provider.Descriptor{provider.Codex: desc}, mgr.prefix)
	mgr2.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))
	mgr2.SetHookCaptureDeps(captures2, "http://127.0.0.1:8837")
	// The grant is bound to the workdir supplied to resume, not the token file.
	var canonicalWorkDir string
	if err := repo.db.Reader().QueryRow(`SELECT canonical_work_dir
		FROM provider_identity_capture_grants WHERE token_sha256 = ?`,
		*slot.CaptureTokenHash).Scan(&canonicalWorkDir); err != nil {
		t.Fatal(err)
	}
	body := captureBody(*source.NativeSessionID, hookcapture.ExpectedSourceResume,
		canonicalWorkDir)
	if got := postCapture(t, captures2, token, body); got != 204 {
		t.Fatalf("capture after restart = %d, want 204", got)
	}
	if err := mgr2.ConvergeRecoveryCandidates(ctx); err != nil {
		t.Fatal(err)
	}
	committed, err := repo.Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Status != StatusRunning || committed.Generation != source.Generation+1 ||
		committed.NativeSessionID == nil || *committed.NativeSessionID != *source.NativeSessionID {
		t.Fatalf("committed successor = status=%s generation=%d native=%v",
			committed.Status, committed.Generation, committed.NativeSessionID)
	}
	if remaining, err := repo.SuccessorSlot(ctx, source.ID); err != nil || remaining != nil {
		t.Fatalf("committed slot = %+v err=%v, want retired", remaining, err)
	}
	// The provider reported the conversation the row already named, so the
	// capture confirms rather than moves it.
	if countAuditType(t, repo, source.ID, AuditIdentityConfirmed) != 1 ||
		countAuditType(t, repo, source.ID, AuditResumed) != 1 {
		t.Fatal("restart convergence did not produce exactly one capture and resume audit")
	}
}

func TestSuccessorConvergenceRetiresSlotWhenTmuxServerIsGone(t *testing.T) {
	mgr, repo, _, projectID := newAttestedResumeEnv(t)
	ctx := context.Background()
	created, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Codex, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	crashAndObserve(t, mgr, created)
	source, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	marker := freshMarker(t)
	_, grant, err := mgr.captures.PrepareResumeGrant(source.ID, source.Provider,
		t.TempDir(), source.Generation+1, marker, *source.NativeSessionID)
	if err != nil {
		t.Fatal(err)
	}
	slot, err := repo.BeginSuccessorSlot(ctx, source, marker, &grant, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.tmux.KillServer(ctx); err != nil {
		t.Fatal(err)
	}

	if err := mgr.ConvergeRecoveryCandidates(ctx); err != nil {
		t.Fatalf("converge with absent tmux server: %v", err)
	}
	if remaining, err := repo.SuccessorSlot(ctx, source.ID); err != nil || remaining != nil {
		t.Fatalf("successor slot after convergence = %+v, err=%v", remaining, err)
	}
	var state string
	if err := repo.db.Reader().QueryRowContext(ctx,
		`SELECT state FROM provider_identity_capture_grants WHERE token_sha256 = ?`,
		slot.CaptureTokenHash).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(hookcapture.GrantRevoked) {
		t.Fatalf("capture state = %q, want revoked", state)
	}
	if _, err := mgr.tmux.ListSessions(ctx); !errors.Is(err, tmux.ErrNoServer) {
		t.Fatalf("convergence started or retained a tmux server: %v", err)
	}
}

func TestLegacyConvergenceFinishesAttemptWhenTmuxServerIsGone(t *testing.T) {
	mgr, repo, projectID, client, _ := newResumeEnv(t, []string{"-c", "sleep 300"})
	ctx := context.Background()
	created, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	crashAndObserve(t, mgr, created)
	source, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	nonce := freshMarker(t)
	marker := freshMarker(t)
	bootEpoch, err := currentBootEpoch()
	if err != nil {
		t.Fatal(err)
	}
	now := db.FormatTime(time.Now())
	err = repo.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO session_recovery_attempts (
			session_id, nonce, kind, source_status, source_generation,
			source_identity_marker, source_resume_attempts, source_updated_at,
			source_controller_revision, candidate_generation,
			candidate_identity_marker, candidate_tmux_socket_path,
			candidate_tmux_session_name, candidate_expected_executable,
			candidate_boot_epoch, state, created_at, updated_at
		) VALUES (?, ?, 'automatic', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			'executing', ?, ?)`, source.ID, nonce, source.Status, source.Generation,
			*source.IdentityMarker, source.ResumeAttempts, db.FormatTime(source.UpdatedAt),
			source.ControllerRevision, source.Generation+1, marker, client.SocketPath(),
			recoverySessionName(mgr.prefix, source.ID, marker), source.ExpectedExecutable,
			bootEpoch, now, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KillServer(ctx); err != nil {
		t.Fatal(err)
	}

	if err := mgr.ConvergeRecoveryCandidates(ctx); err != nil {
		t.Fatalf("converge legacy attempt with absent tmux server: %v", err)
	}
	var state string
	if err := repo.db.Reader().QueryRowContext(ctx, `SELECT state
		FROM session_recovery_attempts WHERE session_id = ? AND nonce = ?`,
		source.ID, nonce).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("legacy attempt state = %q, want failed", state)
	}
	if _, err := client.ListSessions(ctx); !errors.Is(err, tmux.ErrNoServer) {
		t.Fatalf("legacy convergence started or retained a tmux server: %v", err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			return string(raw)
		}
		if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			t.Fatalf("read test file: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
