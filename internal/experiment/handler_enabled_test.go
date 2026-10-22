package experiment

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/capacity"
	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/provider"
)

// experimentEnableEnv is the environment variable that turns the default-off
// unconfined capacity-routing experiment on. It names the same variable
// internal/config parses into Experiments.UnconfinedCapacityRouting; the CI
// job that exercises the enabled wiring sets it to exactly "true".
const experimentEnableEnv = "NOISEDECK_EXPERIMENTS_UNCONFINED_CAPACITY_ROUTING"

// fakeProviderDescriptor is a fully inert descriptor used only to prove that
// the experiment handler mounts and guards its routes. It never executes a
// real provider CLI and never touches credentials: the executable is
// /bin/true, so any probe runs a harmless coreutils binary.
type fakeProviderDescriptor struct {
	name     provider.Name
	execPath string
	version  string
}

func (f *fakeProviderDescriptor) Name() provider.Name { return f.name }
func (f *fakeProviderDescriptor) Executable() string  { return f.execPath }
func (f *fakeProviderDescriptor) IdentityContract() provider.IdentityContract {
	return provider.IdentityNone
}
func (f *fakeProviderDescriptor) Availability() error { return nil }
func (f *fakeProviderDescriptor) NewSessionArgs(provider.LaunchSpec) ([]string, error) {
	return []string{f.execPath}, nil
}
func (f *fakeProviderDescriptor) ResumeSessionArgs(provider.ResumeSpec) ([]string, error) {
	return nil, nil
}
func (f *fakeProviderDescriptor) Environment() []string          { return nil }
func (f *fakeProviderDescriptor) PermittedEnvironment() []string { return nil }
func (f *fakeProviderDescriptor) RequiredConfigPaths() []string  { return nil }
func (f *fakeProviderDescriptor) ExecutableIdentity() provider.ExecutableIdentity {
	return provider.ExecutableIdentity{}
}
func (f *fakeProviderDescriptor) Injection() provider.InjectionContract {
	return provider.InjectionContract{}
}
func (f *fakeProviderDescriptor) GracefulStopSignal() syscall.Signal { return syscall.SIGTERM }
func (f *fakeProviderDescriptor) ForceStopSignal() syscall.Signal    { return syscall.SIGKILL }

// newEnabledTestHandler builds the handler the way the flag-enabled boot path
// does: a fresh control capability, a collector over the fake descriptors, and
// enabled=true. The fake provider name is not claude or codex, so the capacity
// collector reports unknown-state evidence without executing anything.
func newEnabledTestHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	capability, err := NewControlCapability()
	if err != nil {
		t.Fatalf("NewControlCapability: %v", err)
	}
	if capability == "" {
		t.Fatal("NewControlCapability returned an empty capability")
	}
	descriptors := fakeDescriptors()
	return New(true, capability, capacity.New(descriptors), descriptors, nil, nil, zerolog.Nop()), capability
}

// fakeDescriptors is the one inert descriptor map every handler construction
// in this file shares.
func fakeDescriptors() map[provider.Name]provider.Descriptor {
	return map[provider.Name]provider.Descriptor{
		"fake-provider": &fakeProviderDescriptor{
			name: "fake-provider", execPath: "/bin/true", version: "true",
		},
	}
}

// TestEnabledExperimentGateAndRoutes is the CI gate for the experiment wiring.
// The experiment is off by default: without the enable env var the enabled
// behavior is deliberately not exercised here and the suite stays green and
// fast, so only a CI run with the flag set proves route mounting, capability
// minting, and the capability gate. This test must fail when that wiring
// breaks.
func TestEnabledExperimentGateAndRoutes(t *testing.T) {
	if os.Getenv(experimentEnableEnv) != "true" {
		t.Skip("unconfined capacity routing is off by default; run with " + experimentEnableEnv + "=true to exercise the enabled wiring")
	}

	h, capability := newEnabledTestHandler(t)
	mux := http.NewServeMux()
	h.Mount(mux.Handle)

	// GET /api/experimental/harnesses serves the list when the capability is correct.
	req := httptest.NewRequest(http.MethodGet, "/api/experimental/harnesses", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(CapabilityHeader, capability)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("harnesses list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list harnessList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("harnesses list body is not JSON: %v", err)
	}
	if !list.Unconfined {
		t.Errorf("harnesses list did not mark the response unconfined")
	}
	if len(list.Profiles) != 1 || list.Profiles[0].ID != "fake-provider" {
		t.Errorf("harnesses profiles = %v, want one %q profile", list.Profiles, "fake-provider")
	}

	// GET /api/experimental/harnesses/{provider} is mounted and reachable.
	req = httptest.NewRequest(http.MethodGet, "/api/experimental/harnesses/fake-provider", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(CapabilityHeader, capability)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("harness show status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// POST /api/experimental/projects/{id}/agent-handoffs is mounted. The fake
	// provider does not implement delegation, so the handler rejects it as
	// unsupported rather than returning a plain 404 from the mux.
	req = httptest.NewRequest(http.MethodPost, "/api/experimental/projects/p1/agent-handoffs", strings.NewReader(`{
		"provider": "fake-provider",
		"model": "model-x",
		"role": "reviewer",
		"task_id": "123",
		"correlation_id": "op-1"
	}`))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(CapabilityHeader, capability)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("agent handoff status = %d, body = %s", rec.Code, rec.Body.String())
	}

	for _, provided := range []string{"", "wrong-capability"} {
		req := httptest.NewRequest(http.MethodGet, "/api/experimental/harnesses", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		if provided != "" {
			req.Header.Set(CapabilityHeader, provided)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"capability_required"`) {
			t.Fatalf("capability %q response = %d %s", provided, rec.Code, rec.Body.String())
		}
	}
}

// TestAmbientEnableEnvReachesMountedHandler covers the seam the enabled CI
// job exists for: the ambient enable variable must travel through the real
// config loader into Experiments.UnconfinedCapacityRouting and from there,
// through boot's exact construction, into a handler whose mounted routes
// follow it. The variable is read where the process found it — never set or
// cleared here — and what it demands is what the handler must do, so a chain
// that drops the variable fails here instead of asserting whatever it
// produced. Both demands are real assertions: an ambient-off run must be
// refused and an ambient-on run must be served.
func TestAmbientEnableEnvReachesMountedHandler(t *testing.T) {
	required := false
	if ambient, set := os.LookupEnv(experimentEnableEnv); set {
		parsed, err := strconv.ParseBool(ambient)
		if err != nil {
			t.Fatalf("%s: %v", experimentEnableEnv, err)
		}
		required = parsed
	}

	// Defaults alone refuse to start because project roots have no safe
	// default; the enable variable itself stays ambient.
	t.Setenv("NOISEDECK_PROJECTS_ALLOWED_ROOTS", t.TempDir())

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Experiments.UnconfinedCapacityRouting != required {
		t.Fatalf("config loaded Experiments.UnconfinedCapacityRouting=%v while ambient %s demands %v",
			cfg.Experiments.UnconfinedCapacityRouting, experimentEnableEnv, required)
	}

	// Boot's construction: a capability is minted only when the loaded
	// value is true, so a disabled handler holds none at all.
	capability := ""
	if cfg.Experiments.UnconfinedCapacityRouting {
		if capability, err = NewControlCapability(); err != nil {
			t.Fatalf("NewControlCapability: %v", err)
		}
	}
	descriptors := fakeDescriptors()
	h := New(cfg.Experiments.UnconfinedCapacityRouting, capability,
		capacity.New(descriptors), descriptors, nil, nil, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle)

	// The request always presents a genuine minted capability: when the
	// loaded value disabled the experiment it must still be refused, which
	// separates a real refusal from a missing credential.
	offered := capability
	if offered == "" {
		if offered, err = NewControlCapability(); err != nil {
			t.Fatalf("NewControlCapability: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/experimental/harnesses", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(CapabilityHeader, offered)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if !required {
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"experiment_disabled"`) {
			t.Fatalf("ambient-off wiring response = %d %s", rec.Code, rec.Body.String())
		}
		return
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("ambient-on wiring status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list harnessList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("harnesses list body is not JSON: %v", err)
	}
	if !list.Unconfined || len(list.Profiles) != 1 || list.Profiles[0].ID != "fake-provider" {
		t.Fatalf("harnesses list = %+v, want the single unconfined fake-provider profile", list)
	}
}

// TestDisabledExperimentRoutesAreNeverServed: when the flag is off the routes
// stay mounted, so a stale caller gets a direct experiment_disabled diagnostic
// instead of a mux fall-through, but the enabled wiring is never reachable —
// even a correct capability cannot unlock a disabled handler.
func TestDisabledExperimentRoutesAreNeverServed(t *testing.T) {
	h := New(false, "capability", nil, nil, nil, nil, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle)

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/experimental/harnesses", ""},
		{http.MethodGet, "/api/experimental/harnesses/fake-provider", ""},
		{http.MethodPost, "/api/experimental/projects/p1/agent-handoffs", `{}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set(CapabilityHeader, "capability")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"experiment_disabled"`) {
			t.Fatalf("%s %s response = %d %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}
