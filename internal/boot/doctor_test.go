package boot

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/noisefloor-oss/deck/internal/doctor"
)

func TestDoctorReportIsAuthenticated(t *testing.T) {
	cfg := testConfig(t)
	cfg.Auth.Mode = "trusted_proxy"
	cfg.Auth.TrustedProxy.Addresses = []string{"127.0.0.1"}
	cfg.Taskmaster.Executable = filepath.Join(t.TempDir(), "missing-noise")
	h := upOrFail(t, cfg)
	base := serveServer(t, h)
	if code, _ := get(t, base+"/api/doctor"); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated doctor = %d, want 401", code)
	}
	code, body := authenticatedGet(t, base+"/api/doctor")
	if code != http.StatusOK {
		t.Fatalf("doctor = %d (%s)", code, body)
	}
	var report doctor.Report
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		t.Fatal(err)
	}
	if report.OK || len(report.Checks) == 0 {
		t.Fatalf("missing engine did not fail report: %+v", report)
	}
	for _, check := range report.Checks {
		if check.Name == "listen-address" && !check.OK {
			t.Fatalf("running Deck did not recognize its own listener: %+v", check)
		}
	}
}
