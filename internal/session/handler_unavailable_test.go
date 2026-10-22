package session

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noisefloor-oss/deck/internal/provider"
)

// A provider whose CLI is not installed must refuse with an
// actionable provider_not_launchable error that names the missing path, not a
// generic 500 "could not create session" after tmux tries to exec a
// nonexistent binary. The missing path lives under t.TempDir() so the test is
// deterministic and does not depend on any real provider's install location.
func TestSessionAPICreateProviderExecutableMissing(t *testing.T) {
	mgr, _, mux, projectID := newAPIEnv(t)
	missing := filepath.Join(t.TempDir(), "no-such-cli")
	mgr.providers[provider.Omp] = &fakeDescriptor{
		name:       provider.Omp,
		executable: missing,
		contract:   provider.IdentityAssigned,
	}
	rec := apiDo(t, mux, http.MethodPost, "/api/projects/"+projectID+"/sessions",
		`{"provider":"omp"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("missing executable: status = %d, want 409, body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), missing) {
		t.Fatalf("missing executable: body does not name the missing path: %s", rec.Body)
	}
}
