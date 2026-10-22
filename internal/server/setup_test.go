package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/rs/zerolog"
)

func TestSetupUsesOriginGuardPrivateShellAndContentFreeLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-state.json")
	state, err := auth.EnsureBootstrapState(path, "")
	if err != nil {
		t.Fatal(err)
	}
	link, err := auth.IssueSetupToken(path, "http://127.0.0.1:8837", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(link)
	fragment, _ := url.ParseQuery(parsed.Fragment)
	token := fragment.Get("token")
	var logs bytes.Buffer
	h, err := auth.New(auth.Config{Mode: "password", PasswordStatePath: path, SessionSecret: state.State.SessionSecret, AllowedOrigins: []string{"http://127.0.0.1:8837"}}, zerolog.New(&logs))
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, nil)
	s.log = zerolog.New(&logs)
	h.Mount(s.Handle)
	shell := StaticHandler(staticAssets(), "test")
	s.Handle("GET /setup", h.SetupPage(shell))
	s.Handle("GET /", shell)
	for _, target := range []string{"/setup?token=" + token, "/setup/", "/setup/unknown"} {
		rec := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		want := http.StatusNotFound
		if strings.HasPrefix(target, "/setup?") {
			want = http.StatusOK
			if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Content-Security-Policy") != staticCSP {
				t.Fatal("setup shell lost security headers")
			}
		}
		if rec.Code != want {
			t.Fatalf("setup page status %d want %d", rec.Code, want)
		}
	}
	body, _ := json.Marshal(map[string]string{"token": token, "password": "password-canary"})
	for _, origin := range []string{"https://evil.example", "http://127.0.0.1:8837"} {
		req := httptest.NewRequest(http.MethodPost, "/auth/setup?token="+token, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(rec, req)
		want := http.StatusForbidden
		if origin == "http://127.0.0.1:8837" {
			want = http.StatusNoContent
		}
		if rec.Code != want {
			t.Fatalf("setup post status %d want %d", rec.Code, want)
		}
	}
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatal("setup shell remained public after creation")
	}
	if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "password-canary") {
		t.Fatal("setup credentials leaked into logs")
	}
	if !strings.Contains(logs.String(), "/auth/setup") {
		t.Fatal("setup bypassed request logging")
	}
}
