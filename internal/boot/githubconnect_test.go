package boot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/noisefloor-oss/deck/internal/tmux"
)

func TestGitHubRoutesBeforeAnyProject(t *testing.T) {
	cfg := testConfig(t)
	cfg.Auth.Mode = "trusted_proxy"
	cfg.Auth.TrustedProxy.Addresses = []string{"127.0.0.1"}
	cfg.Auth.TrustedProxy.IdentityHeader = "X-Identity"
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\n[ \"$1 $2\" = 'auth status' ] || exit 97\nprintf '%s\\n' '{\"hosts\":{}}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	h := upOrFail(t, cfg)
	base := serveServer(t, h)
	for _, path := range []string{"/api/github/connect/status", "/api/github/connect/attempt"} {
		req, err := http.NewRequest(http.MethodGet, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Identity", "operator")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil {
			t.Fatalf("%s: status=%d err=%v", path, resp.StatusCode, err)
		}
		if path == "/api/github/connect/status" && body["outcome"] != "no-account" {
			t.Fatalf("status = %v", body)
		}
		if path == "/api/github/connect/attempt" && (body["attempt"] != nil || body["csrf"] == "") {
			t.Fatalf("attempt = %v", body)
		}
	}
	if sessions, err := h.Tmux.ListSessions(context.Background()); (err != nil && !errors.Is(err, tmux.ErrNoServer)) || len(sessions) != 0 {
		t.Fatalf("readonly routes created sessions: count=%d err=%v", len(sessions), err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfg.Tmux.SocketPath), "github-connect", "tmux.sock")); !os.IsNotExist(err) {
		t.Fatalf("readonly routes started login tmux: %v", err)
	}
}
