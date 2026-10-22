package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/project"
	"github.com/noisefloor-oss/deck/internal/provider"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// Session endpoints over the real manager, real tmux, and real SQLite —
// the same env as the resume tests, with an httptest mux on top.

func newAPIEnv(t *testing.T) (*Manager, *Repository, http.Handler, string) {
	t.Helper()
	mgr, repo, projectID, _, _ := newResumeEnv(t, []string{"-c", "sleep 300; exit 0"})
	h := NewHandler(mgr, repo, project.NewRepository(repo.db), zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	return mgr, repo, mux, projectID
}

func apiDo(t *testing.T, mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func assertRecoveryProjectionJSON(t *testing.T, raw []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode recovery projection: %v", err)
	}
	var objects []map[string]any
	switch v := value.(type) {
	case map[string]any:
		objects = []map[string]any{v}
	case []any:
		for _, item := range v {
			object, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("session projection is %T, want object", item)
			}
			objects = append(objects, object)
		}
	default:
		t.Fatalf("session response is %T, want object or array", value)
	}
	valid := map[string]map[string]bool{
		"recovery_capability": {"resume": true, "relaunch": true, "none": true},
		"recovery_state":      {"unavailable": true, "automatic_pending": true, "recovering": true, "awaiting_gesture": true},
		"presentation_state":  {"running": true, "recovering": true, "attachment_unavailable": true, "ended": true, "inactive": true, "resume_ended": true},
	}
	for _, object := range objects {
		for field, values := range valid {
			got, ok := object[field].(string)
			if !ok || !values[got] {
				t.Fatalf("%s = %#v, want an explicit supported value", field, object[field])
			}
		}
		for _, retired := range []string{
			"detached", "attention_detail", "actions",
			"resume_next_at", "resume_series_started_at", "resume_probation_until",
		} {
			if _, ok := object[retired]; ok {
				t.Fatalf("retired session field %q is still serialized", retired)
			}
		}
	}
}

// The list groups a project's sessions (all statuses, newest first) and
// Get reads one; both 404 on the right missing things and nothing else.
func TestSessionAPIListAndGet(t *testing.T) {
	mgr, _, mux, projectID := newAPIEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var ids []string
	for i := 0; i < 2; i++ {
		s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		ids = append(ids, s.ID)
		defer func(id string) {
			stopCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
			defer c()
			_ = mgr.Stop(stopCtx, id, true)
		}(s.ID)
	}

	rec := apiDo(t, mux, http.MethodGet, "/api/projects/"+projectID+"/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status = %d, body %s", rec.Code, rec.Body)
	}
	assertRecoveryProjectionJSON(t, rec.Body.Bytes())
	var listed []*Session
	if err := json.NewDecoder(rec.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d sessions, want 2", len(listed))
	}

	rec = apiDo(t, mux, http.MethodGet, "/api/sessions/"+ids[0], "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d", rec.Code)
	}
	assertRecoveryProjectionJSON(t, rec.Body.Bytes())
	if rec := apiDo(t, mux, http.MethodGet, "/api/sessions/nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get missing: status = %d, want 404", rec.Code)
	}
	if rec := apiDo(t, mux, http.MethodGet, "/api/projects/nope/sessions", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("list missing project: status = %d, want 404", rec.Code)
	}
}

func TestSessionAPICreateRequiresProjectGitIdentitySetup(t *testing.T) {
	_, sessions, mux, projectID := newAPIEnv(t)
	if err := sessions.db.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE projects SET git_author_name = '', git_author_email = '' WHERE id = ?`, projectID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A registered provider, so the refusal under test is the identity gate
	// and not the admission gate ahead of it.
	rec := apiDo(t, mux, http.MethodPost, "/api/projects/"+projectID+"/sessions", `{"provider":"claude"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "git_identity_required") {
		t.Fatalf("create without Git setup = %d %s, want 409 git_identity_required", rec.Code, rec.Body.String())
	}
}

// Create validates the provider and the project, launches through
// the real manager, and answers honestly when a valid provider cannot be
// launched yet.
func TestSessionAPICreate(t *testing.T) {
	mgr, _, mux, projectID := newAPIEnv(t)
	pi := sleeperDescriptor()
	pi.name = provider.Pi
	mgr.providers[provider.Pi] = pi
	// A valid provider that cannot be launched today (retrieved
	// identity, like the real codex contract): the handler must answer
	// 409 with the honest reason, not 400 or a silent fallback.
	execPath := gatePassingCLI(t)
	mgr.providers[provider.Codex] = &fakeDescriptor{
		name:       provider.Codex,
		executable: execPath,
		contract:   provider.IdentityRetrieved,
		hook: &provider.IdentityHook{
			Event:          "SessionStart",
			SessionIDField: "session_id",
			ConfigPath:     writeHookConfig(t, `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/usr/local/bin/noisedeck-provider-hook"}]}]}}`),
		},
	}

	rec := apiDo(t, mux, http.MethodPost, "/api/projects/"+projectID+"/sessions",
		`{"provider":"claude"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body %s", rec.Code, rec.Body)
	}
	assertRecoveryProjectionJSON(t, rec.Body.Bytes())
	var created Session
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Status != StatusRunning && created.Status != StatusExited {
		t.Fatalf("created status = %s", created.Status)
	}
	piRec := apiDo(t, mux, http.MethodPost, "/api/projects/"+projectID+"/sessions",
		`{"provider":"pi"}`)
	if piRec.Code != http.StatusCreated {
		t.Fatalf("create Pi: status = %d, body %s", piRec.Code, piRec.Body)
	}
	var piSession Session
	if err := json.NewDecoder(piRec.Body).Decode(&piSession); err != nil {
		t.Fatal(err)
	}
	if piSession.Provider != string(provider.Pi) || piSession.NativeSessionID == nil {
		t.Fatalf("Pi session = provider %q native id %v", piSession.Provider, piSession.NativeSessionID)
	}

	if rec := apiDo(t, mux, http.MethodPost, "/api/projects/"+projectID+"/sessions",
		`{"provider":"emacs"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad provider: status = %d, want 400", rec.Code)
	}
	// Admission is the registry, not a list of every harness the project has
	// ever supported. This environment registered claude, codex and pi, so
	// devin is refused here exactly as it would be on an image that does not
	// ship it — and the refusal names what the user can actually run,
	// rather than reciting sixteen harnesses they may not have.
	rejected := apiDo(t, mux, http.MethodPost, "/api/projects/"+projectID+"/sessions",
		`{"provider":"devin"}`)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("unregistered Devin: status = %d, want 400 unsupported, body %s",
			rejected.Code, rejected.Body)
	}
	if body := rejected.Body.String(); !strings.Contains(body, "claude, codex, pi") {
		t.Fatalf("refusal = %s, want it to name the registered providers", body)
	}
	for _, absent := range []string{"devin", "qwen", "mimo", "qoder"} {
		if strings.Contains(rejected.Body.String(), absent) {
			t.Fatalf("refusal names %s, which this console cannot launch: %s", absent, rejected.Body)
		}
	}
	if rec := apiDo(t, mux, http.MethodPost, "/api/projects/nope/sessions",
		`{"provider":"claude"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing project: status = %d, want 404", rec.Code)
	}

	// A REGISTERED provider that cannot be launched today is a conflict with
	// the honest reason, not a 400 or a silent fallback. The two refusals are
	// different answers to different questions: 400 means this console has no
	// such harness, 409 means it has one and cannot start it right now.
	rec = apiDo(t, mux, http.MethodPost, "/api/projects/"+projectID+"/sessions",
		`{"provider":"codex"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("unlaunchable provider: status = %d, want 409, body %s", rec.Code, rec.Body)
	}
}

// A registered MiMo must launch. Admission reads the registry, so this
// covers the whole path a picker chip takes on an image that ships MiMo:
// registered, offered, launched. It submits the exact request the picker
// sends and requires a real session row back.
func TestSessionAPICreateAdmitsMimo(t *testing.T) {
	mgr, _, mux, projectID := newAPIEnv(t)
	dir := t.TempDir()
	execPath := filepath.Join(dir, "mimo")
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) echo \"0.1.13\" ;;\n  *) sleep 300 ;;\nesac\n"
	if err := os.WriteFile(execPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake mimo: %v", err)
	}
	d, err := provider.NewMimo(execPath)
	if err != nil {
		t.Fatalf("NewMimo: %v", err)
	}
	mgr.providers[provider.Mimo] = d

	rec := apiDo(t, mux, http.MethodPost, "/api/projects/"+projectID+"/sessions",
		`{"provider":"mimo"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create MiMo: status = %d, body %s", rec.Code, rec.Body)
	}
	assertRecoveryProjectionJSON(t, rec.Body.Bytes())
	var created Session
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Provider != string(provider.Mimo) {
		t.Fatalf("created provider = %q, want mimo", created.Provider)
	}
	if created.Status != StatusRunning && created.Status != StatusExited {
		t.Fatalf("created status = %s", created.Status)
	}
}

// A registered Qoder must launch. Admission reads the registry, so this
// covers the whole path a picker chip takes on an image that ships Qoder:
// registered, offered, launched. It submits the exact request the picker
// sends and requires a real session row back.
func TestSessionAPICreateAdmitsQoder(t *testing.T) {
	mgr, _, mux, projectID := newAPIEnv(t)
	dir := t.TempDir()
	execPath := filepath.Join(dir, "qoder")
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) echo \"1.1.28\" ;;\n  *) sleep 300 ;;\nesac\n"
	if err := os.WriteFile(execPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake qoder: %v", err)
	}
	d, err := provider.NewQoder(execPath)
	if err != nil {
		t.Fatalf("NewQoder: %v", err)
	}
	mgr.providers[provider.Qoder] = d

	rec := apiDo(t, mux, http.MethodPost, "/api/projects/"+projectID+"/sessions",
		`{"provider":"qoder"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create Qoder: status = %d, body %s", rec.Code, rec.Body)
	}
	assertRecoveryProjectionJSON(t, rec.Body.Bytes())
	var created Session
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Provider != string(provider.Qoder) {
		t.Fatalf("created provider = %q, want qoder", created.Provider)
	}
	if created.Status != StatusRunning && created.Status != StatusExited {
		t.Fatalf("created status = %s", created.Status)
	}
}

// A registered Cursor must launch. Admission reads the registry, so this
// covers the whole path a picker chip takes on an image that ships Cursor:
// registered, offered, launched. It submits the exact request the picker
// sends and requires a real session row back.
func TestSessionAPICreateAdmitsCursor(t *testing.T) {
	mgr, _, mux, projectID := newAPIEnv(t)
	dir := t.TempDir()
	execPath := filepath.Join(dir, "cursor-agent")
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) echo \"2026.08.11-e8db854\" ;;\n  *) sleep 300 ;;\nesac\n"
	if err := os.WriteFile(execPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake cursor-agent: %v", err)
	}
	d, err := provider.NewCursor(execPath)
	if err != nil {
		t.Fatalf("NewCursor: %v", err)
	}
	mgr.providers[provider.Cursor] = d

	rec := apiDo(t, mux, http.MethodPost, "/api/projects/"+projectID+"/sessions",
		`{"provider":"cursor"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create Cursor: status = %d, body %s", rec.Code, rec.Body)
	}
	assertRecoveryProjectionJSON(t, rec.Body.Bytes())
	var created Session
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Provider != string(provider.Cursor) {
		t.Fatalf("created provider = %q, want cursor", created.Provider)
	}
	if created.Status != StatusRunning && created.Status != StatusExited {
		t.Fatalf("created status = %s", created.Status)
	}
}

// A registered Grok must launch. Admission reads the registry, so this
// covers the whole path a picker chip takes on an image that ships Grok:
// registered, offered, launched. It submits the exact request the picker
// sends and requires a real session row back.
func TestSessionAPICreateAdmitsGrok(t *testing.T) {
	mgr, _, mux, projectID := newAPIEnv(t)
	dir := t.TempDir()
	execPath := filepath.Join(dir, "grok")
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) echo \"grok 1.0.5 (5115b46bc9)\" ;;\n  *) sleep 300 ;;\nesac\n"
	if err := os.WriteFile(execPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake grok: %v", err)
	}
	d, err := provider.NewGrok(execPath)
	if err != nil {
		t.Fatalf("NewGrok: %v", err)
	}
	mgr.providers[provider.Grok] = d

	rec := apiDo(t, mux, http.MethodPost, "/api/projects/"+projectID+"/sessions",
		`{"provider":"grok"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create Grok: status = %d, body %s", rec.Code, rec.Body)
	}
	assertRecoveryProjectionJSON(t, rec.Body.Bytes())
	var created Session
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Provider != string(provider.Grok) {
		t.Fatalf("created provider = %q, want grok", created.Provider)
	}
	if created.Status != StatusRunning && created.Status != StatusExited {
		t.Fatalf("created status = %s", created.Status)
	}
}

// Stop honors the confirmation gate for force and records the
// observed exit; stopping a dead session is a conflict.
func TestSessionAPIStop(t *testing.T) {
	mgr, repo, mux, projectID := newAPIEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if rec := apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/stop",
		`{"force":true,"confirmed":false}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed force: status = %d, want 400", rec.Code)
	}

	rec := apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/stop", `{"force":false}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("graceful stop: status = %d, body %s", rec.Code, rec.Body)
	}
	got, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusExited {
		t.Fatalf("status = %s, want exited", got.Status)
	}

	if rec := apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/stop",
		`{"force":true,"confirmed":true}`); rec.Code != http.StatusConflict {
		t.Fatalf("stop of exited: status = %d, want 409", rec.Code)
	}
}

// SetColor accepts exactly the tag keys the sidebar's swatch strip offers
// (SESSION_COLOR_TAGS in web/src/components/sidebar.ts) plus the legacy
// family keys stored rows still carry. A strip key the map refuses
// answers 400 on every pick of that swatch, so each key is posted and
// read back as the persisted tag.
func TestSessionColors(t *testing.T) {
	mgr, repo, mux, projectID := newAPIEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	strip := []string{
		"crimson", "vermilion", "gold", "lime", "emerald", "teal", "cerulean",
		"sapphire", "indigo", "violet", "orchid",
		"pearl", "silver", "graphite", "ink",
	}
	for _, key := range strip {
		rec := apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/color", `{"color":"`+key+`"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("set %s: status = %d, body %s", key, rec.Code, rec.Body)
		}
		row, err := repo.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("get after %s: %v", key, err)
		}
		if row.Color == nil || *row.Color != key {
			t.Fatalf("set %s: stored color = %v, want the strip key verbatim", key, row.Color)
		}
	}
	// The legacy family keys stay accepted so stored rows and a
	// mid-upgrade client keep working; the web resolves them.
	for _, key := range []string{"red", "amber", "green", "blue", "magenta", "gray"} {
		if rec := apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/color", `{"color":"`+key+`"}`); rec.Code != http.StatusNoContent {
			t.Fatalf("set legacy %s: status = %d, body %s", key, rec.Code, rec.Body)
		}
	}
	// A row stored under gray renders and selects silver, so clicking that
	// selected swatch submits silver; it must persist, or the row is stuck
	// on a key the strip no longer offers.
	if rec := apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/color", `{"color":"silver"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("re-tag gray row as silver: status = %d, body %s", rec.Code, rec.Body)
	}
	got, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Color == nil || *got.Color != "silver" {
		t.Fatalf("stored color = %v, want silver", got.Color)
	}
	// A token the strip never offered is refused rather than stored
	// as a tag nothing renders.
	if rec := apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/color", `{"color":"accent"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown token: status = %d, want 400", rec.Code)
	}
	// null clears the tag, and the cleared row really carries no colour.
	if rec := apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/color", `{"color":null}`); rec.Code != http.StatusNoContent {
		t.Fatalf("clear: status = %d, want 204", rec.Code)
	}
	cleared, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if cleared.Color != nil {
		t.Fatalf("stored color after clear = %q, want nil", *cleared.Color)
	}
}

// The stop deadline consumes the request context waiting for an
// exit that never comes, so the honest 200 must read the session on a
// fresh bounded context — not the expired one.
func TestSessionAPIStopUnobservedReturnsSession(t *testing.T) {
	desc := &fakeDescriptor{
		name:       provider.Claude,
		executable: "/bin/sh",
		args:       []string{"-c", "trap '' TERM; sleep 300; exit 0"},
		contract:   provider.IdentityAssigned,
		graceful:   syscall.SIGTERM,
		force:      syscall.SIGKILL,
	}
	mgr, repo, _, projectID := newTestEnv(t, desc)
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))
	h := NewHandler(mgr, repo, project.NewRepository(repo.db), zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// A request whose own deadline is what the stop will burn waiting
	// for the trapped-away SIGTERM.
	reqCtx, reqCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer reqCancel()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+s.ID+"/stop",
		strings.NewReader(`{"force":false}`)).WithContext(reqCtx)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("stop unobserved: status = %d, want 200 with the honest session; body %s", rec.Code, rec.Body)
	}
	var got Session
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != StatusRunning {
		t.Fatalf("status = %s, want running (exit unobserved, never assumed)", got.Status)
	}
	t.Cleanup(func() {
		killCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = mgr.Stop(killCtx, s.ID, true)
	})
}

// Only project.ErrNotFound is a 404 — a storage failure is a
// logged 500, in List and Create alike.
func TestSessionAPIProjectReadFailureIs500(t *testing.T) {
	_, repo, mux, projectID := newAPIEnv(t)
	_ = repo.db.Close()

	if rec := apiDo(t, mux, http.MethodGet, "/api/projects/"+projectID+"/sessions", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("list with dead storage: status = %d, want 500", rec.Code)
	}
	if rec := apiDo(t, mux, http.MethodPost, "/api/projects/"+projectID+"/sessions",
		`{"provider":"claude"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("create with dead storage: status = %d, want 500", rec.Code)
	}
}

// Kill the pane's process out of band, then read.
// The API must not present the raw row — the status comes back derived.
func TestSessionAPIGetReconcilesDeadPane(t *testing.T) {
	mgr, repo, mux, projectID := newAPIEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Out of band: the process group dies. The row still says running.
	if err := syscall.Kill(-*s.ProcessGroupID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	rec := apiDo(t, mux, http.MethodGet, "/api/sessions/"+s.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d", rec.Code)
	}
	var got Session
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status == StatusRunning {
		t.Fatal("a dead pane was presented as running")
	}
	if got.Status != StatusExited {
		t.Fatalf("status = %s, want exited (observed evidence recorded)", got.Status)
	}

	// And the recording is durable, the reconciler's own path.
	row, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("repo get: %v", err)
	}
	if row.Status != StatusExited || row.ExitSignal == nil {
		t.Fatalf("row = %s signal %v, want the observed exit recorded", row.Status, row.ExitSignal)
	}

	// The list derives too.
	rec = apiDo(t, mux, http.MethodGet, "/api/projects/"+projectID+"/sessions", "")
	var listed []*Session
	if err := json.NewDecoder(rec.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	for _, l := range listed {
		if l.Status == StatusRunning {
			t.Fatal("list presented a dead pane as running")
		}
	}
}

// Stop maps typed errors precisely: unknown session is 404, an identity
// conflict is 409, not a flattened 500.
func TestSessionAPIStopErrorMapping(t *testing.T) {
	mgr, _, mux, projectID := newAPIEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if rec := apiDo(t, mux, http.MethodPost, "/api/sessions/nope/stop", `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("stop missing: status = %d, want 404", rec.Code)
	}

	// An identity conflict: something out of band re-marks the pane.
	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		killCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = mgr.Stop(killCtx, s.ID, true)
	})
	if err := mgr.tmux.SetPaneOption(ctx, *s.TmuxPaneID, tmux.OptMarker, "foreign-marker"); err != nil {
		t.Fatalf("re-mark pane: %v", err)
	}
	if rec := apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/stop", `{}`); rec.Code != http.StatusConflict {
		t.Fatalf("stop identity mismatch: status = %d, want 409", rec.Code)
	}
}

// Losing the tmux server makes the Deck attachment unavailable. It does not
// prove the predecessor dead, but it also cannot gate a separately fenced
// successor or turn passive reads into errors.
func TestSessionAPIGetWithDeadTmuxServer(t *testing.T) {
	mgr, repo, mux, projectID := newAPIEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := mgr.tmux.KillServer(ctx); err != nil {
		t.Fatalf("kill tmux server: %v", err)
	}

	rec := apiDo(t, mux, http.MethodGet, "/api/sessions/"+s.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get with dead tmux server: status = %d, want 200, body %s", rec.Code, rec.Body)
	}
	assertRecoveryProjectionJSON(t, rec.Body.Bytes())
	var detail Session
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detached detail: %v", err)
	}
	if detail.PresentationState != PresentationAttachmentUnavailable ||
		detail.RecoveryCapability != RecoveryCapabilityResume ||
		detail.RecoveryState != RecoveryAutomaticPending {
		t.Fatalf("detached projection = presentation %q capability %q recovery %q",
			detail.PresentationState, detail.RecoveryCapability, detail.RecoveryState)
	}

	// The row records only attachment loss. It does not invent exit evidence or
	// touch the predecessor process.
	row, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("repo get: %v", err)
	}
	if row.Status != StatusInterrupted || row.ExitCode != nil || row.ExitSignal != nil {
		t.Fatalf("row = status %s code %v signal %v, want evidence-free interrupted",
			row.Status, row.ExitCode, row.ExitSignal)
	}
}

func TestSessionAPIGetAndListSurviveTmuxObservationFailure(t *testing.T) {
	mgr, _, mux, projectID := newAPIEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	info, err := mgr.tmux.PaneInfoByID(ctx, *s.TmuxPaneID)
	if err != nil {
		t.Fatalf("observe predecessor: %v", err)
	}
	socketInfo, err := os.Stat(mgr.tmux.SocketPath())
	if err != nil {
		t.Fatalf("stat tmux socket: %v", err)
	}
	if err := os.Chmod(mgr.tmux.SocketPath(), 0); err != nil {
		t.Fatalf("make tmux observation fail: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(mgr.tmux.SocketPath(), socketInfo.Mode().Perm()) })

	for _, request := range []struct {
		name string
		path string
	}{
		{name: "get", path: "/api/sessions/" + s.ID},
		{name: "list", path: "/api/projects/" + projectID + "/sessions"},
	} {
		t.Run(request.name, func(t *testing.T) {
			rec := apiDo(t, mux, http.MethodGet, request.path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
			}
			assertRecoveryProjectionJSON(t, rec.Body.Bytes())
		})
	}
	alive, err := tmux.ProcAlive(info.PID, info.StartTicks)
	if err != nil || !alive {
		t.Fatalf("observation failure affected predecessor: alive=%v err=%v", alive, err)
	}
}

func TestIntentionalExitNeverExposesRecoveryGesture(t *testing.T) {
	t.Run("clean exit", func(t *testing.T) {
		mgr, repo, mux, projectID := newAPIEnv(t)
		s := createInterrupted(t, mgr, projectID)
		if err := repo.db.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE sessions SET status = ?, exit_code = 0,
				exit_signal = NULL, exited_at = ?, stop_requested_at = NULL,
				resume_attempts = 0 WHERE id = ?`,
				StatusExited, db.FormatTime(time.Now()), s.ID)
			return err
		}); err != nil {
			t.Fatalf("seed clean exit: %v", err)
		}
		assertRecoveryClosedHTTP(t, mux, s.ID)
	})

	t.Run("user stop", func(t *testing.T) {
		mgr, repo, mux, projectID := newAPIEnv(t)
		s := createInterrupted(t, mgr, projectID)
		at := db.FormatTime(time.Now())
		if err := repo.db.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE sessions SET status = ?, exit_code = NULL,
				exit_signal = '15', exited_at = ?, stop_requested_at = ?,
				resume_attempts = 0 WHERE id = ?`, StatusExited, at, at, s.ID)
			return err
		}); err != nil {
			t.Fatalf("seed user stop: %v", err)
		}
		assertRecoveryClosedHTTP(t, mux, s.ID)
	})

	t.Run("identity-none status", func(t *testing.T) {
		desc := mustShellDescriptor(t)
		for _, code := range []int{0, 1, 23} {
			s := &Session{Status: StatusExited, ExitCode: &code}
			if automaticRecoveryOpportunity(s, desc, true, time.Now()) {
				t.Fatalf("identity-none exit code %d exposed recovery", code)
			}
		}
	})
}

func TestIncompleteLegacyAttentionConvergesInactiveBeforeAPIResponse(t *testing.T) {
	mgr, repo, mux, projectID := newAPIEnv(t)
	s := createInterrupted(t, mgr, projectID)
	ctx := context.Background()
	const nativeID = "0198a6a4-43c2-7b4f-8d21-b55cc8e27f14"
	if err := repo.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET status = ?,
			native_session_id = ?, resume_attempts = 7,
			pane_pid = NULL, process_group_id = NULL,
			process_start_ticks = NULL, expected_executable = NULL,
			boot_epoch = NULL WHERE id = ?`, StatusNeedsAttention, nativeID, s.ID)
		return err
	}); err != nil {
		t.Fatalf("seed incomplete legacy row: %v", err)
	}

	get := apiDo(t, mux, http.MethodGet, "/api/sessions/"+s.ID, "")
	if get.Code != http.StatusOK {
		t.Fatalf("legacy GET = %d (%s), want 200", get.Code, get.Body.String())
	}
	var detail Session
	if err := json.NewDecoder(get.Body).Decode(&detail); err != nil {
		t.Fatalf("decode legacy detail: %v", err)
	}
	if detail.Status == StatusNeedsAttention || detail.Status != StatusInterrupted ||
		detail.ResumeAttempts != 0 || detail.RecoveryState != RecoveryUnavailable ||
		detail.PresentationState != PresentationInactive {
		t.Fatalf("legacy projection = status %q attempts %d recovery %q presentation %q",
			detail.Status, detail.ResumeAttempts, detail.RecoveryState, detail.PresentationState)
	}

	resume := apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/resume",
		`{"nonce":"incomplete-legacy-gesture"}`)
	if resume.Code != http.StatusNotFound {
		t.Fatalf("legacy resume = %d (%s), want unmounted route", resume.Code, resume.Body.String())
	}
	if slot, err := repo.SuccessorSlot(ctx, s.ID); err != nil || slot != nil {
		t.Fatalf("legacy successor slot = %+v err=%v, want none", slot, err)
	}
}

func assertRecoveryClosedHTTP(t *testing.T, mux http.Handler, sessionID string) {
	t.Helper()
	get := apiDo(t, mux, http.MethodGet, "/api/sessions/"+sessionID, "")
	if get.Code != http.StatusOK {
		t.Fatalf("closed session GET = %d (%s)", get.Code, get.Body.String())
	}
	var detail Session
	if err := json.NewDecoder(get.Body).Decode(&detail); err != nil {
		t.Fatalf("decode closed session: %v", err)
	}
	if detail.PresentationState != PresentationEnded ||
		detail.RecoveryState != RecoveryUnavailable {
		t.Fatalf("closed projection = presentation %q recovery %q",
			detail.PresentationState, detail.RecoveryState)
	}
	resume := apiDo(t, mux, http.MethodPost, "/api/sessions/"+sessionID+"/resume",
		`{"nonce":"closed-row-gesture"}`)
	if resume.Code != http.StatusNotFound {
		t.Fatalf("closed session resume = %d (%s), want unmounted route", resume.Code, resume.Body.String())
	}
}

// A confirmed observation whose RECORDING fails must not serve
// the stale row. With the exit-record write failing, GET is a 500, not
// a 200 reading "running" over a dead pane.
func TestSessionAPIGetWithFailingExitRecord(t *testing.T) {
	mgr, repo, mux, projectID := newAPIEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The pane dies out of band...
	if err := syscall.Kill(-*s.ProcessGroupID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// ...and the recording of that truth fails.
	if err := repo.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			CREATE TRIGGER fail_exit_record
			BEFORE UPDATE OF status ON sessions
			BEGIN
				SELECT RAISE(FAIL, 'injected exit-record failure');
			END`)
		return err
	}); err != nil {
		t.Fatalf("install trigger: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.db.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS fail_exit_record`)
			return err
		})
	})

	rec := apiDo(t, mux, http.MethodGet, "/api/sessions/"+s.ID, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("get with failing exit record: status = %d, want 500, body %s", rec.Code, rec.Body)
	}
}

// The honest 200 after an unobserved stop stands on the
// stop_unobserved audit record. When THAT write fails, the
// endpoint must fail honestly (500), not present success without it.
func TestSessionAPIStopUnobservedAuditFailureIs500(t *testing.T) {
	desc := &fakeDescriptor{
		name:       provider.Claude,
		executable: "/bin/sh",
		args:       []string{"-c", "trap '' TERM; sleep 300; exit 0"},
		contract:   provider.IdentityAssigned,
		graceful:   syscall.SIGTERM,
		force:      syscall.SIGKILL,
	}
	mgr, repo, _, projectID := newTestEnv(t, desc)
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))
	h := NewHandler(mgr, repo, project.NewRepository(repo.db), zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		killCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = mgr.Stop(killCtx, s.ID, true)
	})

	// The stop_unobserved audit write fails — but only that one; the
	// stop_requested audit and the signal itself proceed.
	if err := repo.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			CREATE TRIGGER fail_unobserved_audit
			BEFORE INSERT ON audit_events
			WHEN NEW.event_type = 'session.stop_unobserved'
			BEGIN
				SELECT RAISE(FAIL, 'injected audit failure');
			END`)
		return err
	}); err != nil {
		t.Fatalf("install trigger: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.db.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS fail_unobserved_audit`)
			return err
		})
	})

	reqCtx, reqCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer reqCancel()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+s.ID+"/stop",
		strings.NewReader(`{"force":false}`)).WithContext(reqCtx)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("stop with failing stop_unobserved audit: status = %d, want 500, body %s",
			rec.Code, rec.Body)
	}
}

// The API's JSON keys are the contract: snake_case, matching the project
// registry and the browser client. An untagged struct would silently
// emit PascalCase the client cannot consume.
func TestSessionAPIWireShapeIsSnakeCase(t *testing.T) {
	mgr, _, mux, projectID := newAPIEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = mgr.Stop(stopCtx, s.ID, true)
	})

	for _, path := range []string{"/api/sessions/" + s.ID, "/api/projects/" + projectID + "/sessions"} {
		rec := apiDo(t, mux, http.MethodGet, path, "")
		var body map[string]any
		raw := rec.Body.Bytes()
		// The list wraps the same object shape in an array.
		if path != "/api/sessions/"+s.ID {
			var arr []map[string]any
			if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
				t.Fatalf("%s: decode list: %v", path, err)
			}
			body = arr[0]
		} else if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("%s: decode: %v", path, err)
		}
		for _, key := range []string{"id", "project_id", "provider", "status", "generation", "created_at"} {
			if _, ok := body[key]; !ok {
				t.Fatalf("%s: missing snake_case key %q in %v", path, key, body)
			}
		}
		for _, key := range []string{"ID", "ProjectID", "Provider", "Status", "Generation", "CreatedAt"} {
			if _, ok := body[key]; ok {
				t.Fatalf("%s: PascalCase key %q leaked into the wire shape", path, key)
			}
		}
	}
}

func TestRemoveDeletesSession(t *testing.T) {
	mgr, repo, mux, projectID := newAPIEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	rec := apiDo(t, mux, http.MethodDelete, "/api/sessions/"+s.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}

	got, err := repo.Get(context.Background(), s.ID)
	if err != nil {
		t.Fatalf("get removed session: %v", err)
	}
	if got.RemovedAt == nil {
		t.Fatal("removed_at is unset after DELETE")
	}

	// The removed session leaves every active list.
	active, err := repo.ListByProject(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list by project: %v", err)
	}
	for _, x := range active {
		if x.ID == s.ID {
			t.Fatal("removed session still listed")
		}
	}

	// The removal is audited.
	var n int
	if err := repo.db.Reader().QueryRow(
		`SELECT count(*) FROM audit_events WHERE session_id = ? AND event_type = 'session.removed'`, s.ID,
	).Scan(&n); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if n != 1 {
		t.Fatalf("session.removed audit events = %d, want 1", n)
	}

	// Idempotent: a second DELETE does not error and does not double-record.
	rec2 := apiDo(t, mux, http.MethodDelete, "/api/sessions/"+s.ID, "")
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("second DELETE = %d, want 204 (%s)", rec2.Code, rec2.Body.String())
	}
	if err := repo.db.Reader().QueryRow(
		`SELECT count(*) FROM audit_events WHERE session_id = ? AND event_type = 'session.removed'`, s.ID,
	).Scan(&n); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if n != 1 {
		t.Fatalf("audit events after second DELETE = %d, want 1", n)
	}

	// Unknown ids 404.
	rec3 := apiDo(t, mux, http.MethodDelete, "/api/sessions/nope", "")
	if rec3.Code != http.StatusNotFound {
		t.Fatalf("DELETE unknown = %d, want 404", rec3.Code)
	}
}

func TestRemoveStartingSessionIsRecordOnly(t *testing.T) {
	mgr, repo, mux, projectID := newAPIEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	live, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := &Session{
		ID: "s-starting", ProjectID: projectID, Provider: "claude",
		TmuxSocketPath: live.TmuxSocketPath, TmuxSessionName: "noisedeck-test-starting",
		Status: StatusStarting, Generation: 1,
	}
	if err := repo.Create(context.Background(), s); err != nil {
		t.Fatalf("seed starting session: %v", err)
	}
	rec := apiDo(t, mux, http.MethodDelete, "/api/sessions/s-starting", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE starting = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	got, err := repo.Get(context.Background(), s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RemovedAt == nil {
		t.Fatal("starting session was not retired record-only")
	}
}

func TestRenameSessionRoundTripsAndValidates(t *testing.T) {
	mgr, repo, mux, projectID := newAPIEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = mgr.Stop(stopCtx, s.ID, true)
	}()

	// The label round-trips through the served record.
	rec := apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/rename", `{"name":"deploy watcher"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("rename = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	got, err := repo.Get(context.Background(), s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DisplayName != "deploy watcher" {
		t.Fatalf("display_name = %q, want the recorded label", got.DisplayName)
	}

	// Clearing returns the row to its provider rendering.
	rec = apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/rename", `{"name":"  "}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("clear rename = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	got, err = repo.Get(context.Background(), s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DisplayName != "" {
		t.Fatalf("display_name = %q, want cleared", got.DisplayName)
	}

	// Over-length is a 400; unknown sessions 404. Both renames audited.
	long := strings.Repeat("x", 101)
	rec = apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/rename", `{"name":"`+long+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("over-length rename = %d, want 400", rec.Code)
	}
	rec = apiDo(t, mux, http.MethodPost, "/api/sessions/nope/rename", `{"name":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("rename unknown = %d, want 404", rec.Code)
	}
	var n int
	if err := repo.db.Reader().QueryRow(
		`SELECT count(*) FROM audit_events WHERE session_id = ? AND event_type = 'session.renamed'`, s.ID,
	).Scan(&n); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if n != 2 {
		t.Fatalf("session.renamed audit events = %d, want 2", n)
	}
}

// A recorded last-output time must reach the browser
// under the key the sidebar reads. The field existed in the client's
// type long before anything populated it, so the row and the wire are
// asserted together — a stamp the API drops is indistinguishable from
// no stamp at all.
func TestSessionAPIExposesLastOutputTime(t *testing.T) {
	mgr, repo, mux, projectID := newAPIEnv(t)
	ctx := context.Background()

	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Absent until output happens: the key must not appear at all, so the
	// client renders nothing rather than a zero time.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d (%s)", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "last_output_at") {
		t.Fatalf("a session with no output emitted last_output_at: %s", rec.Body)
	}

	at := time.Now().UTC().Add(-4 * time.Minute).Truncate(time.Millisecond)
	if err := repo.RecordOutput(ctx, s.ID, at); err != nil {
		t.Fatalf("RecordOutput: %v", err)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/sessions", nil))
	var listed []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d sessions, want 1", len(listed))
	}
	raw, ok := listed[0]["last_output_at"].(string)
	if !ok {
		t.Fatalf("last_output_at missing or not a string: %+v", listed[0])
	}
	got, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("last_output_at %q is not a timestamp the client can parse: %v", raw, err)
	}
	if !got.Equal(at) {
		t.Fatalf("last_output_at = %v, want %v", got, at)
	}
}

func TestRetiredAttentionRoutesAreUnmounted(t *testing.T) {
	_, _, mux, _ := newAPIEnv(t)
	for _, path := range []string{
		"/api/sessions/missing/actions",
		"/api/sessions/missing/recheck",
		"/api/sessions/missing/forget",
		"/api/sessions/missing/discard-stale",
		"/api/sessions/missing/recovery-retry",
	} {
		rec := apiDo(t, mux, http.MethodPost, path, `{}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("retired route %s = %d, want 404", path, rec.Code)
		}
	}
}

// The create-size hint is cosmetic: whatever number arrives, creation must
// not fail over it. Extremes that would overflow a narrow decode field are
// the trap this pins — a uint16 body field turned 65536 into a 400 before
// the bounds could speak.
func TestSessionAPICreateSizeHintNeverFailsCreation(t *testing.T) {
	_, _, mux, projectID := newAPIEnv(t)
	for _, body := range []string{
		`{"provider":"claude"}`,
		`{"provider":"claude","cols":0,"rows":0}`,
		`{"provider":"claude","cols":120}`,
		`{"provider":"claude","rows":40}`,
		`{"provider":"claude","cols":20,"rows":5}`,
		`{"provider":"claude","cols":500,"rows":300}`,
		`{"provider":"claude","cols":19,"rows":5}`,
		`{"provider":"claude","cols":501,"rows":300}`,
		`{"provider":"claude","cols":65536,"rows":40}`,
		`{"provider":"claude","cols":-1,"rows":-1}`,
		`{"provider":"claude","cols":9007199254740991,"rows":9007199254740991}`,
	} {
		rec := apiDo(t, mux, http.MethodPost, "/api/projects/"+projectID+"/sessions", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create with %s: status = %d, body %s", body, rec.Code, rec.Body)
		}
	}
}
