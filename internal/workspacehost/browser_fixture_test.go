//go:build linux

package workspacehost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacegateway"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
	"github.com/noisefloor-oss/deck/internal/workspaceplain"
	"github.com/noisefloor-oss/deck/internal/workspaceruntime"
)

// browserNative substitutes kernel, FUSE and engine observations only. The
// browser still uses production envelopes, registry proofs and browser grants.
type browserNative struct {
	*assemblyNative
	uncertainLock    bool
	migrationPending bool
}

func (n *browserNative) Lock(ctx context.Context, id string, prepare func(context.Context) error) (workspaceruntime.Operation, error) {
	n.mu.Lock()
	uncertain := n.uncertainLock
	n.uncertainLock = false
	n.mu.Unlock()
	op, err := n.assemblyNative.Lock(ctx, id, prepare)
	if err == nil && uncertain {
		// Preparation succeeded, but the synthetic unmount observation was lost.
		// Only this post-preparation stage may later settle successfully.
		n.mu.Lock()
		n.state = workspaceruntime.State{Configured: true, Storage: "unknown", Runtime: "unknown"}
		op.Status, op.Stage = "uncertain", "unmounting"
		op.State = n.state
		n.ops[id] = op
		n.mu.Unlock()
		err = workspaceruntime.ErrUncertain
	}
	return op, err
}

func (n *browserNative) Initialize(ctx context.Context, id string, secret []byte) (workspaceruntime.Operation, error) {
	n.mu.Lock()
	old, ok := n.ops[id]
	old.State = n.state
	n.mu.Unlock()
	if ok {
		return old, nil
	}
	return n.assemblyNative.Initialize(ctx, id, secret)
}

func (n *browserNative) Unlock(_ context.Context, id string, secret []byte) (workspaceruntime.Operation, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(secret) != 32 || n.state.Storage != "closed" {
		return workspaceruntime.Operation{}, workspaceruntime.ErrPrecondition
	}
	n.calls = append(n.calls, "unlock")
	n.state = workspaceruntime.State{Configured: true, Storage: "open", Runtime: "stopped", Epoch: "mounted-" + id}
	op := workspaceruntime.Operation{ID: id, Kind: "unlock", Status: "succeeded", State: n.state}
	n.ops[id] = op
	return op, nil
}

func (n *browserNative) Start(_ context.Context, id string) (workspaceruntime.Operation, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.migrationPending {
		n.state.Migration = "pending"
		return workspaceruntime.Operation{}, workspaceruntime.ErrMigrationRequired
	}
	n.calls = append(n.calls, "start")
	n.state.Runtime, n.state.Epoch = "running", "running-"+id
	op := workspaceruntime.Operation{ID: id, Kind: "start", Status: "succeeded", State: n.state}
	n.ops[id] = op
	return op, nil
}

// TestBrowserGatewayFixture is an opt-in server for the companion browser
// probe, never a production auth bypass. Only the listening origin is written
// to the ignored readiness file; credentials and kits stay in memory.
func TestBrowserGatewayFixture(t *testing.T) {
	ready := os.Getenv("DECK_WORKSPACE_BROWSER_FIXTURE")
	if ready == "" {
		t.Skip("opt-in fixture: run web/tools/security-go-probe.mjs")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	requireOK(t, err)
	if !filepath.IsAbs(ready) || filepath.Clean(ready) != ready || !strings.HasPrefix(ready, filepath.Join(root, "run")+string(filepath.Separator)) {
		t.Fatal("fixture readiness must be inside this checkout's ignored run directory")
	}
	assets := os.DirFS(filepath.Join(root, "web", "dist"))
	if _, err := fs.Stat(assets, "index.html"); err != nil {
		t.Fatal("build web assets before starting the browser fixture")
	}
	cfg := hostConfig(t)
	var plain *assemblyPlain
	if policy := workspacekeys.EncryptionPolicy(os.Getenv("DECK_WORKSPACE_BROWSER_POLICY")); policy != "" {
		if policy != workspacekeys.EncryptionOptional && policy != workspacekeys.EncryptionUnavailable {
			t.Fatal("unknown browser fixture protection policy")
		}
		cfg = plainHostConfig(t, policy)
		plain = &assemblyPlain{state: workspaceplain.State{Runtime: "stopped", Epoch: "before"}, ops: make(map[string]workspaceplain.Operation)}
	}
	front := httptest.NewUnstartedServer(nil)
	cfg.Bind = front.Listener.Addr().String()
	_, port, err := net.SplitHostPort(cfg.Bind)
	requireOK(t, err)
	// The WebAuthn library requires a domain RP ID, including for password-only
	// installations. localhost resolves to the loopback listener above.
	cfg.Origin = "https://localhost:" + port
	cfg.Auth.RedirectURL = cfg.Origin + "/auth/callback"
	cfg.Native.InstanceID = "browser-go-fixture"
	if cfg.Plain != nil {
		cfg.Plain.InstanceID, cfg.Plain.PublicOrigin = cfg.Native.InstanceID, cfg.Origin
	}
	f := &assemblyFixture{t: t, h: &Host{cfg: cfg}, native: &assemblyNative{
		state: workspaceruntime.State{Storage: "closed", Runtime: "stopped", Epoch: "closed"},
		ops:   make(map[string]workspaceruntime.Operation)}}
	t.Cleanup(f.h.Close)
	requireOK(t, cfg.Validate())
	requireOK(t, f.h.acquire())
	native := &browserNative{assemblyNative: f.native, migrationPending: os.Getenv("DECK_WORKSPACE_BROWSER_MIGRATION") == "1"}
	socket := cfg.Socket()
	if plain == nil {
		requireOK(t, f.h.mount(context.Background(), assets, "browser-go-fixture", native))
	} else {
		requireOK(t, f.h.mountWithPlain(context.Background(), assets, "browser-go-fixture", nil, plain))
		socket = filepath.Join(cfg.Plain.StateRoot, "data", "gateway", "backend.sock")
	}
	requireOK(t, os.MkdirAll(filepath.Dir(socket), 0700))
	listener, err := net.Listen("unix", socket)
	requireOK(t, err)
	identity, err := auth.New(cfg.Auth, zerolog.Nop())
	requireOK(t, err)
	f.prepareStatus.Store(http.StatusNoContent)
	backend := httptest.NewUnstartedServer(identity.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/internal/workspace/prepare-lock" {
			f.serveBackend(w, r)
			return
		}
		f.backendHits.Add(1)
		if strings.HasSuffix(r.URL.Path, "/events") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, ": synthetic application stream\n\n")
			_ = http.NewResponseController(w).Flush()
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/projects":
			_, _ = io.WriteString(w, `[{"id":"fixture-project","name":"Fixture project","canonical_path":"/synthetic/fixture-project","last_used_at":"2026-01-01T00:00:00Z","git_identity_setup_revision":1,"git_identity_profile_id":"fixture","git_identity_profile_revision":1,"git_author_name":"Fixture","git_author_email":"fixture@example.invalid"}]`)
		case "/api/projects/fixture-project/preview/settings":
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			_, _ = io.WriteString(w, `{"configured":true,"self_service":true,"enabled":false}`)
		case "/api/providers":
			_, _ = io.WriteString(w, `{"providers":[]}`)
		case "/api/settings":
			_, _ = io.WriteString(w, `{"skip_permissions":false,"skip_close_confirm":false,"skip_remove_project_confirm":false}`)
		case "/api/settings/layout":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{}`)
		case "/api/git-identities":
			_, _ = io.WriteString(w, `{"profiles":[{"id":"fixture","revision":1,"name":"Fixture","email":"fixture@example.invalid"}],"default_profile_id":"fixture","revision":1}`)
		default:
			_, _ = io.WriteString(w, `[]`)
		}
	})))
	requireOK(t, backend.Listener.Close())
	backend.Listener = listener
	backend.Start()
	t.Cleanup(backend.Close)
	keys, err := workspacekeys.OpenExisting(context.Background(), f.h.db, workspacekeys.Config{
		InstanceID: cfg.Native.InstanceID, Origin: cfg.Origin, RPID: cfg.Gateway().RPID, EncryptionPolicy: cfg.policy()})
	requireOK(t, err)
	invites, err := workspacegateway.NewInvitations(context.Background(), f.h.db, keys, cfg.AllowedEmail, nil)
	requireOK(t, err)
	done := make(chan struct{})
	var stop sync.Once
	production := f.h.server.Handler
	f.h.server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case "/auth/login":
			id := make([]byte, 32)
			if _, err := rand.Read(id); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			b := f.browser("a")
			if r.URL.Query().Get("owner") == "other" {
				b.login.Subject = "another-fixture-owner"
			}
			if r.URL.Query().Get("owner") == "ineligible" {
				b.login.Subject, b.login.Email = "ineligible-fixture-owner", "ineligible@example.test"
			}
			b.login.ID = hex.EncodeToString(id)
			headers := b.headers()
			request := &http.Request{Header: headers}
			session := request.Cookies()[0]
			session.Path, session.Secure, session.HttpOnly, session.SameSite = "/", true, true, http.SameSiteLaxMode
			http.SetCookie(w, session)
			http.SetCookie(w, &http.Cookie{Name: "__Host-noisedeck_csrf", Value: headers.Get(auth.CSRFHeader), Path: "/", Secure: true, SameSite: http.SameSiteLaxMode})
			http.Redirect(w, r, "/", http.StatusFound)
		case "/__fixture/invitation":
			token, _, err := invites.Issue(r.Context())
			if err != nil {
				w.WriteHeader(http.StatusConflict)
				return
			}
			http.Redirect(w, r, "/#invite="+token, http.StatusFound)
		case "/__fixture/observation":
			f.native.mu.Lock()
			calls := append([]string{}, f.native.calls...)
			state := f.native.state
			f.native.mu.Unlock()
			var plainState *workspaceplain.State
			plainCalls := 0
			if plain != nil {
				plain.mu.Lock()
				copy := plain.state
				plainState, plainCalls = &copy, plain.calls
				plain.mu.Unlock()
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				Calls      []string               `json:"calls"`
				State      workspaceruntime.State `json:"state"`
				Private    int32                  `json:"privateRequests"`
				PlainState *workspaceplain.State  `json:"plainState,omitempty"`
				PlainCalls int                    `json:"plainCalls"`
			}{calls, state, f.backendHits.Load(), plainState, plainCalls})
		case "/__fixture/uncertain-start", "/__fixture/settle-start", "/__fixture/fail-start", "/__fixture/stop-plain":
			if r.Method != http.MethodPost || plain == nil {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			plain.mu.Lock()
			switch r.URL.Path {
			case "/__fixture/uncertain-start":
				plain.uncertain = true
			case "/__fixture/settle-start":
				plain.uncertain = false
				plain.state.Runtime = "running"
				for id, op := range plain.ops {
					if op.Kind == "start" && op.Status == "uncertain" {
						op.Status, op.State = "succeeded", plain.state
						plain.ops[id] = op
					}
				}
			case "/__fixture/fail-start":
				plain.uncertain = false
				plain.state.Runtime = "stopped"
				for id, op := range plain.ops {
					if op.Kind == "start" && op.Status == "uncertain" {
						op.Status, op.State = "failed", plain.state
						plain.ops[id] = op
					}
				}
			case "/__fixture/stop-plain":
				plain.state.Runtime = "stopped"
			}
			plain.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/__fixture/import-complete":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			native.mu.Lock()
			native.migrationPending = false
			native.state.Migration = "complete"
			native.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/__fixture/uncertain-lock":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			native.mu.Lock()
			native.uncertainLock = true
			native.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/__fixture/closed-observation":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			f.native.mu.Lock()
			f.native.state = workspaceruntime.State{Configured: true, Locked: true, Storage: "closed", Runtime: "stopped", Epoch: "settled-lock"}
			f.native.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/__fixture/settle-lock":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			f.native.mu.Lock()
			eligible := false
			for _, op := range f.native.ops {
				eligible = eligible || op.Kind == "lock" && op.Status == "uncertain" && op.Stage == "unmounting"
			}
			if !eligible {
				f.native.mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				return
			}
			f.native.state = workspaceruntime.State{Configured: true, Locked: true, Storage: "closed", Runtime: "stopped", Epoch: "settled-lock"}
			for id, op := range f.native.ops {
				if op.Kind == "lock" && op.Status == "uncertain" && op.Stage == "unmounting" {
					op.Status, op.Stage, op.State = "succeeded", "complete", f.native.state
					f.native.ops[id] = op
				}
			}
			f.native.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/__fixture/done":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			stop.Do(func() { close(done) })
		default:
			production.ServeHTTP(w, r)
		}
	})
	front.Config = f.h.server
	front.StartTLS()
	t.Cleanup(front.Close)
	requireOK(t, os.MkdirAll(filepath.Dir(ready), 0700))
	metadata, err := json.Marshal(map[string]any{"origin": cfg.Origin, "ready": true})
	requireOK(t, err)
	requireOK(t, os.WriteFile(ready, metadata, 0600))
	t.Cleanup(func() { _ = os.Remove(ready) })
	// Close grant-owned streams before httptest waits for application handlers.
	t.Cleanup(f.h.Close)
	select {
	case <-done:
		t.Log("browser fixture completed; closing test streams")
	case <-time.After(8 * time.Minute):
		t.Fatal("browser probe did not finish before the fixture deadline")
	}
}
