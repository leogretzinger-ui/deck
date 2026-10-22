package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/lock"
	"github.com/rs/zerolog"
)

type setupOutputFunc func([]byte) (int, error)

func (f setupOutputFunc) Write(p []byte) (int, error) { return f(p) }

func TestPasswordSetupRenewalUsesSetupOutput(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	cfg := testConfig(t)
	cfg.Auth.Mode, cfg.Auth.PasswordHash, cfg.Auth.SessionSecret = "password", "", ""
	path := filepath.Join(filepath.Dir(cfg.Database.Path), "auth-state.json")
	var output bytes.Buffer
	var expiredLink string
	writer := setupOutputFunc(func(p []byte) (int, error) {
		if expiredLink == "" {
			link, err := auth.IssueSetupToken(path, cfg.Server.AllowedOrigins[0], time.Now().Add(-auth.SetupTokenTTL))
			if err != nil {
				return 0, err
			}
			expiredLink = link
		}
		return output.Write(p)
	})
	h, err := Up(context.Background(), cfg, zerolog.Nop(), WithSetupOutput(writer))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	parsed, err := url.Parse(expiredLink)
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"token": fragment.Get("token")})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/setup/check", bytes.NewReader(body))
	req.Header.Set("Origin", cfg.Server.AllowedOrigins[0])
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.Auth.Mount(mux.Handle)
	output.Reset()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired check = %d", rec.Code)
	}
	link, err := os.ReadFile(auth.SetupURLPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(link)) == expiredLink || output.String() != "Create your password: "+strings.TrimSpace(string(link))+"\n" {
		t.Fatal("renewal did not print the new link to the configured setup output")
	}
}

func TestPasswordSetupChangeAndOfflineResetOverHTTP(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	cfg := testConfig(t)
	cfg.Auth.Mode, cfg.Auth.PasswordHash, cfg.Auth.SessionSecret = "password", "", ""
	var output bytes.Buffer
	h, err := Up(context.Background(), cfg, zerolog.Nop(), WithSetupOutput(&output))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	base, stop := startServer(t, h.Server.Run, h.Server.BoundAddr)
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	type response struct {
		status   int
		location string
		cookies  []*http.Cookie
	}
	request := func(path string, body any, cookies []*http.Cookie, csrf string) response {
		t.Helper()
		method := http.MethodGet
		var reader io.Reader
		if body != nil {
			method = http.MethodPost
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		req, err := http.NewRequest(method, base+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", cfg.Server.AllowedOrigins[0])
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(auth.CSRFHeader, csrf)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return response{status: resp.StatusCode, location: resp.Header.Get("Location"), cookies: resp.Cookies()}
	}
	root := request("/", nil, nil, "")
	if root.status != http.StatusSeeOther || root.location != "/setup" {
		t.Fatalf("first-run root = %d", root.status)
	}
	path := filepath.Join(filepath.Dir(cfg.Database.Path), "auth-state.json")
	raw, err := os.ReadFile(auth.SetupURLPath(path))
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(strings.TrimSpace(string(raw)))
	fragment, _ := url.ParseQuery(parsed.Fragment)
	token := fragment.Get("token")
	if status := request("/auth/setup", map[string]string{"token": token, "password": "first password"}, nil, "").status; status != http.StatusNoContent {
		t.Fatalf("create = %d", status)
	}
	if status := request("/setup", nil, nil, "").status; status != http.StatusNotFound {
		t.Fatalf("completed setup page = %d", status)
	}
	login := request("/auth/password", map[string]string{"password": "first password"}, nil, "")
	if login.status != http.StatusNoContent {
		t.Fatalf("sign in = %d", login.status)
	}
	cookies := login.cookies
	csrf := ""
	for _, cookie := range cookies {
		if strings.Contains(cookie.Name, "csrf") {
			csrf = cookie.Value
		}
	}
	changed := request("/auth/password/change", map[string]string{"current_password": "first password", "password": "changed password"}, cookies, csrf)
	if changed.status != http.StatusNoContent {
		t.Fatalf("change = %d", changed.status)
	}
	if status := request("/auth/session", nil, cookies, "").status; status != http.StatusUnauthorized {
		t.Fatalf("change retained old browser session: %d", status)
	}
	cookies = changed.cookies
	if status := request("/auth/session", nil, cookies, "").status; status != http.StatusNoContent {
		t.Fatalf("change signed out current browser: %d", status)
	}
	stop()
	h.Close()
	guard, err := lock.Acquire(cfg.Database.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.ResetPassword(path, cfg.Server.AllowedOrigins[0], time.Now())
	_ = guard.Release()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Auth.PasswordHash, cfg.Auth.SessionSecret = "", ""
	output.Reset()
	h, err = Up(context.Background(), cfg, zerolog.Nop(), WithSetupOutput(&output))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	base, _ = startServer(t, h.Server.Run, h.Server.BoundAddr)
	if status := request("/auth/session", nil, cookies, "").status; status != http.StatusUnauthorized {
		t.Fatalf("reset retained browser session: %d", status)
	}
	if status := request("/auth/setup", map[string]string{"token": token, "password": "reused password"}, nil, "").status; status != http.StatusUnauthorized {
		t.Fatalf("reset retained old token: %d", status)
	}
	if !h.Auth.SetupPending() {
		t.Fatal("reset did not restore first-run setup")
	}
}
