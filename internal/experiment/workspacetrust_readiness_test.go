package experiment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/capacity"
	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/project"
	"github.com/noisefloor-oss/deck/internal/provider"
)

// delegatingDescriptor is the inert fake carrying a name the workspace-trust
// reader has a measured gate for, so the readout has something to report.
type delegatingDescriptor struct {
	fakeProviderDescriptor
}

func (d *delegatingDescriptor) NewDelegatedSessionArgs(provider.LaunchSpec, provider.DelegatedLaunch) ([]string, error) {
	return []string{d.execPath}, nil
}

// handoffFixture builds the enabled handler over a real project whose git
// identity is complete, so a handoff reaches the capacity probe rather than
// stopping at an earlier precondition.
func handoffFixture(t *testing.T, homes provider.TrustHomes, workDir string) (*Handler, string, string) {
	t.Helper()
	d, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "control.db")})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	projects := project.NewRepository(d)
	p, _, err := projects.Register(context.Background(), "demo", workDir, []string{workDir},
		project.GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if p.GitIdentityProfileID == nil || p.GitIdentitySetupRevision < 1 {
		t.Fatalf("precondition: registered project has no complete git identity (%+v)", p)
	}
	capability, err := NewControlCapability()
	if err != nil {
		t.Fatalf("NewControlCapability: %v", err)
	}
	descriptors := map[provider.Name]provider.Descriptor{
		provider.Kimi: &delegatingDescriptor{fakeProviderDescriptor{
			name: provider.Kimi, execPath: "/bin/true", version: "true",
		}},
	}
	h := New(true, capability, capacity.New(descriptors), descriptors, nil, projects, zerolog.Nop())
	h.trustHomes = homes
	return h, capability, p.ID
}

func handoff(t *testing.T, h *Handler, capability, projectID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.Mount(mux.Handle)
	body, err := json.Marshal(map[string]string{
		"provider": "kimi", "model": "k2", "role": "implementer",
		"task_id": "426", "correlation_id": "c1",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/api/experimental/projects/"+projectID+"/agent-handoffs", strings.NewReader(string(body)))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(CapabilityHeader, capability)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The readiness readout answers one provisioned precondition per harness for
// one project, from the provider's own record. It REPORTS: a router that will
// not attend a modal asks first and decides for itself, because reading a
// provider's configuration to conclude that a launch would stop at a prompt
// is the inference AGENTS.md forbids.
func TestHarnessReadinessReportsAnUnpreparedProject(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code", "workspace-trust"), 0o700); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	h, capability, projectID := handoffFixture(t, provider.TrustHomes{Home: home}, workDir)
	body := readiness(t, h, capability, projectID)
	if body.Harnesses[0].WorkspaceTrust != "absent" {
		t.Fatalf("readout = %+v", body.Harnesses[0])
	}
	// The record path is the whole point: "trust is missing" is not something
	// a user can act on.
	if !strings.Contains(body.Harnesses[0].Record, provider.KimiWorkspaceTrustFileName(workDir)) {
		t.Errorf("readout does not name the record file: %+v", body.Harnesses[0])
	}
	if body.Directory != workDir {
		t.Errorf("readout names directory %q, want the project's own", body.Directory)
	}
}

// A prepared record reads prepared, and every uncertainty reads unknown with
// the reason — never absent, which is the only value a caller should act on.
func TestHarnessReadinessSeparatesPreparedFromUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		want    string
		prepare func(t *testing.T, home, workDir string)
	}{
		{"prepared", "prepared", func(t *testing.T, home, workDir string) {
			dir := filepath.Join(home, ".kimi-code", "workspace-trust")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			record, err := json.Marshal(map[string]any{"root": workDir, "trustedAt": 1})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, provider.KimiWorkspaceTrustFileName(workDir)), record, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"unknown", "unknown", func(t *testing.T, home, workDir string) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			workDir := t.TempDir()
			tc.prepare(t, home, workDir)
			h, capability, projectID := handoffFixture(t, provider.TrustHomes{Home: home}, workDir)
			body := readiness(t, h, capability, projectID)
			if body.Harnesses[0].WorkspaceTrust != tc.want {
				t.Fatalf("readout = %+v, want %s", body.Harnesses[0], tc.want)
			}
			if tc.want == "unknown" && body.Harnesses[0].Reason == "" {
				t.Error("an unknown readout carries no reason")
			}
		})
	}
}

// The readout is a diagnostic, and the launch paths do not consult it: a
// handoff into an unprepared project proceeds to its next precondition rather
// than being refused on a configuration parse.
func TestHandoffDoesNotGateOnWorkspaceTrust(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code", "workspace-trust"), 0o700); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	h, capability, projectID := handoffFixture(t, provider.TrustHomes{Home: home}, workDir)
	rec := handoff(t, h, capability, projectID)
	if strings.Contains(rec.Body.String(), "workspace_trust") {
		t.Fatalf("the handoff gated on the trust readout: %s", rec.Body.String())
	}
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "capacity_unknown") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func readiness(t *testing.T, h *Handler, capability, projectID string) harnessReadinessList {
	t.Helper()
	mux := http.NewServeMux()
	h.Mount(mux.Handle)
	req := httptest.NewRequest(http.MethodGet,
		"/api/experimental/projects/"+projectID+"/harness-readiness", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(CapabilityHeader, capability)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body harnessReadinessList
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(body.Harnesses) != 1 {
		t.Fatalf("readout carries %d harnesses, want the one configured", len(body.Harnesses))
	}
	return body
}

// The readout carries the same guard as every other route on this API: the
// experiment off, a non-loopback caller, and a missing or wrong capability
// each refuse before the project is read. A handler that forgot the guard
// would answer a caller that reached the port.
func TestHarnessReadinessCarriesTheExperimentGuard(t *testing.T) {
	// Disabled: the route answers not-found whatever else is right. The
	// handler is called directly with a nil project repository, so skipping
	// the guard panics rather than passing — which is the point.
	off := New(false, "", nil, nil, nil, nil, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/experimental/projects/p1/harness-readiness", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	off.HarnessReadiness(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"experiment_disabled"`) {
		t.Fatalf("disabled: %d %s", rec.Code, rec.Body.String())
	}
	on := New(true, "test-capability", nil, nil, nil, nil, zerolog.Nop())
	for _, tc := range []struct {
		name, remote, capability, code string
		status                         int
	}{
		{"non-loopback", "192.0.2.10:1234", "test-capability", "loopback_required", http.StatusForbidden},
		{"no capability", "127.0.0.1:1234", "", "capability_required", http.StatusForbidden},
		{"wrong capability", "127.0.0.1:1234", "wrong-capability", "capability_required", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/experimental/projects/p1/harness-readiness", nil)
			req.RemoteAddr = tc.remote
			if tc.capability != "" {
				req.Header.Set(CapabilityHeader, tc.capability)
			}
			rec := httptest.NewRecorder()
			on.HarnessReadiness(rec, req)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}
