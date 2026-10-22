package session

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/hookcapture"
)

func attentionFixture(t *testing.T) (*Repository, string, string, AttentionEvent) {
	t.Helper()
	repo := newUsageEnv(t)
	cwd := t.TempDir()
	ctx := context.Background()
	err := repo.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET identity_marker='attention-fixture', native_session_id=NULL WHERE id='s1'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := hookcapture.NewService(repo, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.RegisterFenced(ctx, "s1", "claude", cwd, 1, "attention-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET native_session_id=? WHERE id='s1'`, capturedCodexID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	ev := AttentionEvent{SessionID: capturedCodexID, CWD: cwd, Sequence: 1, Turn: strings.Repeat("a", 64), Revision: strings.Repeat("1", 32), Kind: "started"}
	return repo, token, hash, ev
}
func attentionAccept(t *testing.T, repo *Repository, hash string, ev AttentionEvent) {
	t.Helper()
	if err := repo.AcceptAttention(context.Background(), hash, ev); err != nil {
		t.Fatal(err)
	}
}
func attentionRow(t *testing.T, repo *Repository) *Session {
	t.Helper()
	s, err := repo.Get(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAttentionOrderingPersistenceAndAcknowledgmentRace(t *testing.T) {
	repo, _, hash, ev := attentionFixture(t)
	ctx := context.Background()
	attentionAccept(t, repo, hash, ev)
	ev.Sequence++
	ev.Kind = "input"
	attentionAccept(t, repo, hash, ev)
	if got := attentionRow(t, repo); got.AttentionState != "input" || got.CompletionUnread {
		t.Fatalf("pending input: %+v", got)
	}
	if err := repo.AcknowledgeCompletion(ctx, "s1", 1, ev.Revision); err != nil {
		t.Fatal(err)
	}
	if got := attentionRow(t, repo).AttentionState; got != "input" {
		t.Fatal("seeing input cleared it")
	}
	ev.Sequence++
	ev.Kind = "completed"
	attentionAccept(t, repo, hash, ev)
	if !attentionRow(t, repo).CompletionUnread {
		t.Fatal("no completion badge")
	}
	if err := repo.AcknowledgeCompletion(ctx, "s1", 1, ev.Revision); err != nil {
		t.Fatal(err)
	}
	attentionAccept(t, repo, hash, ev) // lost hook response replay must not resurrect unread
	if attentionRow(t, repo).CompletionUnread {
		t.Fatal("replay resurrected acknowledged completion")
	}
	altered := ev
	altered.Kind = "approval"
	if err := repo.AcceptAttention(ctx, hash, altered); !errors.Is(err, hookcapture.ErrConflict) {
		t.Fatalf("changed same-sequence replay = %v", err)
	}
	old := ev
	old.Sequence--
	if err := repo.AcceptAttention(ctx, hash, old); !errors.Is(err, hookcapture.ErrConflict) {
		t.Fatalf("old event = %v", err)
	}
	ev.Sequence += 3
	ev.Turn = strings.Repeat("b", 64)
	ev.Kind = "started"
	attentionAccept(t, repo, hash, ev)
	ev.Sequence++
	ev.Kind = "completed"
	ev.Revision = strings.Repeat("2", 32)
	attentionAccept(t, repo, hash, ev)
	if err := repo.AcknowledgeCompletion(ctx, "s1", 1, strings.Repeat("1", 32)); err != nil {
		t.Fatal(err)
	}
	if !attentionRow(t, repo).CompletionUnread {
		t.Fatal("stale acknowledgment cleared new completion")
	}
	// Reconstruct repository: all facts and seen state live in SQLite.
	restarted := NewRepository(repo.db)
	if !attentionRow(t, restarted).CompletionUnread {
		t.Fatal("restart lost unread")
	}
	ev.Sequence++
	ev.Kind = "working"
	attentionAccept(t, repo, hash, ev)
	if attentionRow(t, repo).CompletionUnread {
		t.Fatal("same-turn continuation retained stale completion")
	}
	ev.Sequence++
	ev.Kind = "completed"
	ev.Revision = strings.Repeat("3", 32)
	attentionAccept(t, repo, hash, ev)
	ev.Sequence++
	ev.Kind = "started"
	ev.Turn = strings.Repeat("c", 64)
	attentionAccept(t, repo, hash, ev)
	if !attentionRow(t, repo).CompletionUnread {
		t.Fatal("next turn cleared unseen previous turn")
	}
}

func TestAttentionIdentityGenerationAndLifecycleFences(t *testing.T) {
	repo, _, hash, ev := attentionFixture(t)
	ctx := context.Background()
	for _, change := range []func(*AttentionEvent){func(e *AttentionEvent) { e.SessionID = "87654321-4321-4abc-8abc-123456789abc" }, func(e *AttentionEvent) { e.CWD = t.TempDir() }, func(e *AttentionEvent) { e.Kind = "completed" }, func(e *AttentionEvent) { e.Sequence = 0 }, func(e *AttentionEvent) { e.Turn = "prompt content" }} {
		bad := ev
		change(&bad)
		if err := repo.AcceptAttention(ctx, hash, bad); !errors.Is(err, hookcapture.ErrConflict) {
			t.Fatalf("foreign/invalid event accepted: %v", err)
		}
	}
	if err := repo.AcceptAttention(ctx, strings.Repeat("0", 64), ev); !errors.Is(err, hookcapture.ErrUnknownToken) {
		t.Fatal(err)
	}
	attentionAccept(t, repo, hash, ev)
	ev.Sequence++
	ev.Kind = "approval"
	attentionAccept(t, repo, hash, ev)
	update := func(query string) {
		t.Helper()
		if err := repo.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error { _, err := tx.ExecContext(ctx, query); return err }); err != nil {
			t.Fatal(err)
		}
	}
	update(`UPDATE sessions SET native_session_id='87654321-4321-4abc-8abc-123456789abc' WHERE id='s1'`)
	if got := attentionRow(t, repo); got.AttentionState != "unknown" || got.CompletionUnread {
		t.Fatal("identity move retained badges")
	}
	ev.Sequence++
	if err := repo.AcceptAttention(ctx, hash, ev); !errors.Is(err, hookcapture.ErrConflict) {
		t.Fatal("foreign native event accepted")
	}
	update(`UPDATE sessions SET generation=2 WHERE id='s1'`)
	if err := repo.AcceptAttention(ctx, hash, ev); !errors.Is(err, hookcapture.ErrConflict) {
		t.Fatal("old generation accepted")
	}
	update(`UPDATE sessions SET status='interrupted' WHERE id='s1'`)
	if got := attentionRow(t, repo).AttentionState; got != "unknown" {
		t.Fatal(got)
	}
}

func TestAttentionHookHTTPBoundsAndAuthentication(t *testing.T) {
	repo, token, _, ev := attentionFixture(t)
	for _, tc := range []struct {
		token, body string
		status      int
	}{
		{"", `{}`, 410}, {token, `{"prompt":"private"}`, 400}, {token, strings.Repeat("x", 5000), 400},
		{token, `{} {}`, 400},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/hooks/attention", strings.NewReader(tc.body))
		req.Header.Set("X-Noisedeck-Hook-Token", tc.token)
		repo.AttentionHook().ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("status %d want %d", rec.Code, tc.status)
		}
	}
	raw, _ := json.Marshal(ev)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/hooks/attention", strings.NewReader(string(raw)))
	req.Header.Set("X-Noisedeck-Hook-Token", token)
	repo.AttentionHook().ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatal(rec.Code)
	}
}

// Real provisioned adapter -> authenticated HTTP -> SQLite with test-owned
// native-shaped fixtures. This exercises transport, not a logged-in model CLI.
func TestAttentionNativeAdapterTransport(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			repo, token, _, ev := attentionFixture(t)
			if provider == "codex" {
				if err := repo.db.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `UPDATE sessions SET provider='codex' WHERE id='s1'; UPDATE provider_identity_capture_grants SET provider='codex' WHERE session_id='s1'`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(repo.AttentionHook())
			defer server.Close()
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			adapter, err := filepath.Abs("../../deploy/provider-hook/noisedeck-attention-hook")
			if err != nil {
				t.Fatal(err)
			}
			emit := func(name string, fields map[string]any) {
				t.Helper()
				body := map[string]any{"session_id": ev.SessionID, "cwd": ev.CWD, "hook_event_name": name, "turn_id": "fixture-turn", "prompt_id": "fixture-prompt"}
				for k, v := range fields {
					body[k] = v
				}
				raw, _ := json.Marshal(body)
				cmd := exec.Command("python3", adapter, provider)
				cmd.Env = append(os.Environ(), "NOISEDECK_SESSION_HOOK_URL="+server.URL+"/api/hooks/capture", "NOISEDECK_SESSION_HOOK_TOKEN="+token, "NOISEDECK_ATTENTION_DIR="+dir, "NOISEDECK_SESSION_HOOK_SOCKET=")
				cmd.Stdin = strings.NewReader(string(raw))
				if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
					t.Fatalf("adapter failed: %v %s", err, out)
				}
			}
			questionTool := "AskUserQuestion"
			if provider == "codex" {
				questionTool = "request_user_input"
			}
			emit("UserPromptSubmit", map[string]any{"prompt": "MUST_NOT_PERSIST"})
			emit("PreToolUse", map[string]any{"tool_name": questionTool, "tool_use_id": "tool-1", "tool_input": map[string]any{"question": "MUST_NOT_PERSIST"}})
			if got := attentionRow(t, repo).AttentionState; got != "input" {
				t.Fatalf("input=%s", got)
			}
			emit("PostToolUse", map[string]any{"tool_name": questionTool, "tool_use_id": "tool-1"})
			emit("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "tool-2"})
			emit("PermissionRequest", map[string]any{"tool_name": "Bash"})
			if attentionRow(t, repo).AttentionState != "approval" {
				t.Fatal("no approval")
			}
			emit("PostToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "tool-2"})
			emit("Stop", nil)
			if attentionRow(t, repo).CompletionUnread {
				t.Fatal("Stop attempt falsely completed")
			}
			emit("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "tool-3"}) // blocked Stop continued
			if got := attentionRow(t, repo).AttentionState; got != "working" {
				t.Fatal(got)
			}
			if provider == "claude" {
				emit("Notification", map[string]any{"notification_type": "idle_prompt"})
			} else {
				raw, _ := json.Marshal(map[string]any{"type": "agent-turn-complete", "thread-id": ev.SessionID, "turn-id": "fixture-turn", "cwd": ev.CWD, "last-assistant-message": "MUST_NOT_PERSIST"})
				cmd := exec.Command("python3", adapter, "codex-notify", string(raw))
				cmd.Env = append(os.Environ(), "NOISEDECK_SESSION_HOOK_URL="+server.URL+"/api/hooks/capture", "NOISEDECK_SESSION_HOOK_TOKEN="+token, "NOISEDECK_ATTENTION_DIR="+dir, "NOISEDECK_SESSION_HOOK_SOCKET=")
				if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
					t.Fatal(err, string(out))
				}
			}
			if !attentionRow(t, repo).CompletionUnread {
				t.Fatal("native completion signal missing")
			}
			emit("SessionEnd", map[string]any{"turn_id": nil, "prompt_id": nil})
			if attentionRow(t, repo).AttentionState != "unknown" {
				t.Fatal("SessionEnd without turn id failed to clear")
			}
			raw, err := os.ReadFile(filepath.Join(dir, "attention.json"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "MUST_NOT_PERSIST") || strings.Contains(string(raw), token) {
				t.Fatal("content or credential persisted")
			}
		})
	}
}

func TestAttentionAcknowledgmentRouteUsesBrowserWrapper(t *testing.T) {
	repo, _, hash, ev := attentionFixture(t)
	attentionAccept(t, repo, hash, ev)
	ev.Sequence++
	ev.Kind = "completed"
	attentionAccept(t, repo, hash, ev)
	handler := &Handler{sessions: repo}
	mux := http.NewServeMux()
	handler.Mount(mux.Handle, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-Browser") != "authorized" {
				w.WriteHeader(403)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	for _, tc := range []struct {
		authorized bool
		body       string
		code       int
	}{
		{false, `{"generation":1,"revision":"` + ev.Revision + `"}`, 403},
		{true, `{"generation":1,"revision":"` + ev.Revision + `","unexpected":true}`, 400},
		{true, `{"generation":1,"revision":"` + ev.Revision + `"}`, 204},
	} {
		req := httptest.NewRequest("POST", "/api/sessions/s1/completion/ack", strings.NewReader(tc.body))
		if tc.authorized {
			req.Header.Set("X-Test-Browser", "authorized")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.code {
			t.Fatalf("ack status=%d want=%d", rec.Code, tc.code)
		}
	}
	if attentionRow(t, repo).CompletionUnread {
		t.Fatal("ack did not persist")
	}
}

func TestAttentionRevokedRemovedAndInactive(t *testing.T) {
	for _, query := range []string{
		`UPDATE provider_identity_capture_grants SET state='revoked', closed_at=updated_at WHERE session_id='s1'`,
		`UPDATE sessions SET removed_at=updated_at WHERE id='s1'`,
		`UPDATE sessions SET status='interrupted' WHERE id='s1'`,
	} {
		t.Run(query, func(t *testing.T) {
			repo, _, hash, ev := attentionFixture(t)
			attentionAccept(t, repo, hash, ev)
			ev.Sequence++
			ev.Kind = "input"
			attentionAccept(t, repo, hash, ev)
			if err := repo.db.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error { _, err := tx.ExecContext(ctx, query); return err }); err != nil {
				t.Fatal(err)
			}
			ev.Sequence++
			if err := repo.AcceptAttention(context.Background(), hash, ev); err == nil {
				t.Fatal("inactive/revoked event accepted")
			}
		})
	}
}
