package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"
)

const setupOrigin = "http://127.0.0.1:8837"

func setupHandler(t *testing.T) (*Handler, string, string, *time.Time) {
	t.Helper()
	now := time.Now()
	path := filepath.Join(t.TempDir(), "auth-state.json")
	if _, err := EnsureBootstrapState(path, ""); err != nil {
		t.Fatal(err)
	}
	link, err := IssueSetupToken(path, setupOrigin, now)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(link)
	fragment, _ := url.ParseQuery(parsed.Fragment)
	state, err := readBootstrapState(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{Mode: "password", PasswordStatePath: path, SessionSecret: state.State.SessionSecret,
		AllowedOrigins: []string{setupOrigin}, Now: func() time.Time { return now }}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if h.password.global.inFlight != 0 {
			t.Errorf("global retained %d reservations", h.password.global.inFlight)
		}
		for source, attempt := range h.password.sources {
			if attempt.inFlight != 0 {
				t.Errorf("source %s retained %d reservations", source, attempt.inFlight)
			}
		}
	})
	return h, fragment.Get("token"), path, &now
}

func authRequest(h *Handler, path string, body any, cookies []*http.Cookie, csrf string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", setupOrigin)
	req.Header.Set(CSRFHeader, csrf)
	req.RemoteAddr = "192.0.2.10:12345"
	for _, c := range cookies {
		req.AddCookie(c)
	}
	mux := http.NewServeMux()
	h.Mount(mux.Handle)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSetupTokenPrivateHashExpiryAndRestart(t *testing.T) {
	h, token, path, now := setupHandler(t)
	if len(token) != 43 {
		t.Fatal("setup token must contain 256 bits of randomness")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(token)) {
		t.Fatal("raw token persisted in auth state")
	}
	for _, name := range []string{path, SetupURLPath(path)} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private file: %v", err)
		}
	}
	link, err := EnsureSetupURL(path, setupOrigin, *now)
	if err != nil || !strings.HasSuffix(link, token) {
		t.Fatal("startup discarded a valid reset link")
	}
	if !validSetupToken(h.credentials, token, now.Add(SetupTokenTTL-time.Nanosecond)) {
		t.Fatal("token expired early")
	}
	*now = now.Add(SetupTokenTTL)
	rec := authRequest(h, "/auth/setup", passwordUpdate{Token: token, Password: "my new password"}, nil, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expiry status %d", rec.Code)
	}
	replacement, err := EnsureSetupURL(path, setupOrigin, *now)
	if err != nil || replacement == link {
		t.Fatal("restart did not replace expired link")
	}
	state, _ := readBootstrapState(path)
	if validSetupToken(state.State, token, *now) {
		t.Fatal("old token survived replacement")
	}
}

func TestSetupCheckRenewsExpiredLink(t *testing.T) {
	h, token, path, now := setupHandler(t)
	var output, logs bytes.Buffer
	h.cfg.SetupOutput = &output
	h.log = zerolog.New(&logs)
	before, err := os.ReadFile(SetupURLPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if rec := authRequest(h, "/auth/setup/check", passwordUpdate{Token: token}, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("unexpired check = %d", rec.Code)
	}
	*now = now.Add(SetupTokenTTL)
	rec := authRequest(h, "/auth/setup/check", passwordUpdate{Token: token}, nil, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired check = %d", rec.Code)
	}
	after, err := os.ReadFile(SetupURLPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, after) {
		t.Fatal("setup check did not replace the expired link")
	}
	parsed, err := url.Parse(strings.TrimSpace(string(after)))
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	fresh := fragment.Get("token")
	if len(fresh) != 43 || fresh == token || !validSetupToken(h.credentials, fresh, *now) {
		t.Fatal("replacement token is not active in the running handler")
	}
	if strings.Contains(rec.Body.String(), fresh) || strings.Contains(rec.Header().Get("Location"), fresh) || len(rec.Result().Cookies()) != 0 {
		t.Fatal("replacement token leaked through the unauthenticated response")
	}
	if output.String() != "Create your password: "+strings.TrimSpace(string(after))+"\n" || strings.Contains(logs.String(), fresh) {
		t.Fatal("replacement link was not confined to setup output")
	}
	state, err := readBootstrapState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !validSetupToken(state.State, fresh, *now) || validSetupToken(state.State, token, *now) {
		t.Fatal("replacement token was not persisted")
	}
	*now = now.Add(time.Minute)
	if rec := authRequest(h, "/auth/setup/check", passwordUpdate{Token: fresh}, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("replacement check = %d", rec.Code)
	}
	unchanged, err := os.ReadFile(SetupURLPath(path))
	if err != nil || !bytes.Equal(after, unchanged) {
		t.Fatal("setup check replaced an unexpired link")
	}
	if strings.Count(output.String(), "Create your password:") != 1 {
		t.Fatal("setup check printed an unexpired link again")
	}
	if rec := authRequest(h, "/auth/setup", passwordUpdate{Token: fresh, Password: "new password"}, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("replacement setup = %d", rec.Code)
	}
}

func TestSetupCheckRenewalFailureDoesNotAdmitExpiredToken(t *testing.T) {
	h, token, path, now := setupHandler(t)
	*now = now.Add(SetupTokenTTL)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	rec := authRequest(h, "/auth/setup/check", passwordUpdate{Token: token}, nil, "")
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), token) {
		t.Fatalf("renewal failure = %d", rec.Code)
	}
}

func TestSetupCheckRetriesAfterOutputFailure(t *testing.T) {
	h, token, path, now := setupHandler(t)
	*now = now.Add(SetupTokenTTL)
	failedOutput, err := os.CreateTemp(t.TempDir(), "setup-output")
	if err != nil {
		t.Fatal(err)
	}
	if err := failedOutput.Close(); err != nil {
		t.Fatal(err)
	}
	h.cfg.SetupOutput = failedOutput
	before := h.credentials
	if rec := authRequest(h, "/auth/setup/check", passwordUpdate{Token: token}, nil, ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("output failure = %d", rec.Code)
	}
	if !bytes.Equal(before.SetupHash, h.credentials.SetupHash) || !before.SetupExpires.Equal(h.credentials.SetupExpires) {
		t.Fatal("failed output replaced the expired in-memory token")
	}
	var output bytes.Buffer
	h.cfg.SetupOutput = &output
	if rec := authRequest(h, "/auth/setup/check", passwordUpdate{Token: token}, nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("retry = %d", rec.Code)
	}
	link, err := os.ReadFile(SetupURLPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "Create your password: "+strings.TrimSpace(string(link))+"\n" {
		t.Fatal("retry did not print the replacement link")
	}
	parsed, err := url.Parse(strings.TrimSpace(string(link)))
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	if rec := authRequest(h, "/auth/setup/check", passwordUpdate{Token: fragment.Get("token")}, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("replacement check = %d", rec.Code)
	}
}

func TestSetupMissingWrongAndRateLimited(t *testing.T) {
	for _, token := range []string{"", "wrong"} {
		t.Run("token="+token, func(t *testing.T) {
			h, _, _, _ := setupHandler(t)
			for i, want := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
				rec := authRequest(h, "/auth/setup", passwordUpdate{Token: token, Password: "new password"}, nil, "")
				if rec.Code != want {
					t.Fatalf("attempt %d got %d want %d", i, rec.Code, want)
				}
				if want == http.StatusTooManyRequests && rec.Header().Get("Retry-After") == "" {
					t.Fatal("missing retry interval")
				}
			}
			if !h.SetupPending() {
				t.Fatal("invalid token set a password")
			}
		})
	}
}

func TestSetupConsumesOncePersistsHashAndDisappears(t *testing.T) {
	h, token, path, _ := setupHandler(t)
	if rec := authRequest(h, "/auth/setup/check", passwordUpdate{Token: token}, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatal("valid setup link was refused")
	}
	shell := h.SetupPage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	rec := httptest.NewRecorder()
	shell.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("setup shell not protected")
	}
	for i, want := range []int{http.StatusNoContent, http.StatusNotFound} {
		rec := authRequest(h, "/auth/setup", passwordUpdate{Token: token, Password: "my new password"}, nil, "")
		if rec.Code != want {
			t.Fatalf("use %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	rec = httptest.NewRecorder()
	shell.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatal("setup page survived password creation")
	}
	if rec := authRequest(h, "/auth/setup/check", passwordUpdate{Token: token}, nil, ""); rec.Code != http.StatusNotFound {
		t.Fatal("token check survived setup")
	}
	if _, err := os.Lstat(SetupURLPath(path)); !os.IsNotExist(err) {
		t.Fatal("setup URL file survived")
	}
	state, err := readBootstrapState(path)
	if err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword(state.State.PasswordHash, []byte("my new password")) != nil {
		t.Fatal("new password not stored as adaptive hash")
	}
	if len(state.State.SetupHash) != 0 {
		t.Fatal("setup token hash survived use")
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("my new password")) || bytes.Contains(raw, []byte(token)) {
		t.Fatal("plaintext credential retained")
	}
	if rec := authRequest(h, "/auth/password", map[string]string{"password": "my new password"}, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatal("created password cannot sign in")
	}
}

func TestSetupRefusesShortPasswordAndKeepsLink(t *testing.T) {
	h, token, _, _ := setupHandler(t)
	for _, password := range []string{"", "1234567", "pässwör"} {
		rec := authRequest(h, "/auth/setup", passwordUpdate{Token: token, Password: password}, nil, "")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "at least 8 characters") {
			t.Fatalf("%q got %d %s", password, rec.Code, rec.Body.String())
		}
		if !h.SetupPending() {
			t.Fatal("short password consumed the setup link")
		}
	}
	if rec := authRequest(h, "/auth/setup", passwordUpdate{Token: token, Password: "pässwörd"}, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("eight characters refused: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPasswordChangeRefusesShortPassword(t *testing.T) {
	h, token, path, _ := setupHandler(t)
	if rec := authRequest(h, "/auth/setup", passwordUpdate{Token: token, Password: "original password"}, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	login := authRequest(h, "/auth/password", map[string]string{"password": "original password"}, nil, "")
	cookies := login.Result().Cookies()
	csrf := ""
	for _, c := range cookies {
		if c.Name == h.csrfCookieName() {
			csrf = c.Value
		}
	}
	before, _ := os.ReadFile(path)
	for _, password := range []string{"", "short"} {
		rec := authRequest(h, "/auth/password/change", passwordUpdate{CurrentPassword: "original password", Password: password}, cookies, csrf)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "at least 8 characters") {
			t.Fatalf("%q got %d %s", password, rec.Code, rec.Body.String())
		}
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("short password changed the stored hash")
	}
}

func TestSetupTokenHasOneConcurrentWinner(t *testing.T) {
	h, token, _, _ := setupHandler(t)
	var wg sync.WaitGroup
	statuses := make(chan int, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- authRequest(h, "/auth/setup", passwordUpdate{Token: token, Password: "password"}, nil, "").Code
		}()
	}
	wg.Wait()
	close(statuses)
	wins := 0
	for code := range statuses {
		if code == http.StatusNoContent {
			wins++
		} else if code != http.StatusNotFound {
			t.Fatalf("unexpected race status %d", code)
		}
	}
	if wins != 1 {
		t.Fatalf("setup winners = %d", wins)
	}
}

func TestSetupRequiresOriginAndJSON(t *testing.T) {
	for _, origin := range []string{"", "https://evil.example", setupOrigin} {
		h, token, _, _ := setupHandler(t)
		req := httptest.NewRequest(http.MethodPost, "/auth/setup", strings.NewReader(`{"token":"`+token+`","password":"password"}`))
		req.Header.Set("Origin", origin)
		if origin != setupOrigin {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		h.handleSetup(rec, req)
		if rec.Code != http.StatusForbidden || !h.SetupPending() {
			t.Fatalf("origin/json refusal = %d", rec.Code)
		}
	}
}

func TestPasswordChangeRequiresSessionCSRFAndCurrentPassword(t *testing.T) {
	h, token, path, now := setupHandler(t)
	if rec := authRequest(h, "/auth/setup", passwordUpdate{Token: token, Password: "original password"}, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	login := authRequest(h, "/auth/password", map[string]string{"password": "original password"}, nil, "")
	cookies := login.Result().Cookies()
	csrf := ""
	for _, c := range cookies {
		if c.Name == h.csrfCookieName() {
			csrf = c.Value
		}
	}
	for _, tc := range []struct {
		cookies       []*http.Cookie
		csrf, current string
		want          int
	}{
		{nil, "", "original password", http.StatusUnauthorized},
		{cookies, "", "original password", http.StatusForbidden},
		{cookies, csrf, "wrong", http.StatusUnauthorized},
		{cookies, csrf, "original password", http.StatusNoContent},
	} {
		*now = now.Add(time.Minute)
		rec := authRequest(h, "/auth/password/change", passwordUpdate{CurrentPassword: tc.current, Password: "changed password"}, tc.cookies, tc.csrf)
		if rec.Code != tc.want {
			t.Fatalf("change got %d want %d: %s", rec.Code, tc.want, rec.Body.String())
		}
	}
	state, _ := readBootstrapState(path)
	if bcrypt.CompareHashAndPassword(state.State.PasswordHash, []byte("changed password")) != nil {
		t.Fatal("new password did not persist")
	}
	*now = now.Add(time.Minute)
	if rec := authRequest(h, "/auth/password", map[string]string{"password": "original password"}, nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatal("old password still works")
	}
	*now = now.Add(time.Minute)
	if rec := authRequest(h, "/auth/password", map[string]string{"password": "changed password"}, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatal("new password cannot sign in")
	}
}

func TestPasswordChangeRotatesSessions(t *testing.T) {
	h, token, path, _ := setupHandler(t)
	if rec := authRequest(h, "/auth/setup", passwordUpdate{Token: token, Password: "original password"}, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	login := authRequest(h, "/auth/password", map[string]string{"password": "original password"}, nil, "")
	other := authRequest(h, "/auth/password", map[string]string{"password": "original password"}, nil, "")
	if login.Code != http.StatusNoContent || other.Code != http.StatusNoContent {
		t.Fatal("login failed")
	}
	cookies := login.Result().Cookies()
	csrf := ""
	for _, cookie := range cookies {
		if cookie.Name == h.csrfCookieName() {
			csrf = cookie.Value
		}
	}
	before := bytes.Clone(h.credentials.SessionSecret)
	payload, err := h.verifySessionValue(cookies[0].Value, h.now())
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := loginFromPayload(payload)
	if !ok {
		t.Fatal("login has no browser identity")
	}
	change := authRequest(h, "/auth/password/change", passwordUpdate{CurrentPassword: "original password", Password: "changed password"}, cookies, csrf)
	if change.Code != http.StatusNoContent {
		t.Fatalf("change = %d: %s", change.Code, change.Body.String())
	}
	probe := func(handler *Handler, cookies []*http.Cookie, method, csrf string) int {
		t.Helper()
		req := httptest.NewRequest(method, "/private", nil)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		req.Header.Set(CSRFHeader, csrf)
		rec := httptest.NewRecorder()
		handler.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(rec, req)
		return rec.Code
	}
	for _, old := range [][]*http.Cookie{cookies, other.Result().Cookies()} {
		if got := probe(h, old, http.MethodGet, ""); got != http.StatusUnauthorized {
			t.Fatalf("old session survived password change: %d", got)
		}
	}
	fresh := change.Result().Cookies()
	if len(fresh) != 2 || probe(h, fresh, http.MethodGet, "") != http.StatusNoContent {
		t.Fatal("password change did not keep the caller signed in")
	}
	payload, err = h.verifySessionValue(fresh[0].Value, h.now())
	if err != nil {
		t.Fatal(err)
	}
	if replacement, ok := loginFromPayload(payload); !ok || replacement != identity {
		t.Fatal("password change replaced the caller's browser identity")
	}
	freshCSRF := ""
	for _, cookie := range fresh {
		if cookie.Name == h.csrfCookieName() {
			freshCSRF = cookie.Value
		}
	}
	if freshCSRF == "" || freshCSRF == csrf || probe(h, fresh, http.MethodPost, freshCSRF) != http.StatusNoContent {
		t.Fatal("replacement CSRF cookie is unusable")
	}
	if got := probe(h, fresh, http.MethodPost, csrf); got != http.StatusForbidden {
		t.Fatalf("old CSRF token survived password change: %d", got)
	}
	state, err := readBootstrapState(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, state.State.SessionSecret) || !bytes.Equal(h.credentials.SessionSecret, state.State.SessionSecret) {
		t.Fatal("rotated session secret was not persisted")
	}
	restarted, err := New(Config{Mode: "password", PasswordStatePath: path, SessionSecret: state.State.SessionSecret}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if probe(restarted, fresh, http.MethodGet, "") != http.StatusNoContent || probe(restarted, cookies, http.MethodGet, "") != http.StatusUnauthorized {
		t.Fatal("restart did not preserve session revocation")
	}
}

func TestSessionRenewalCannotReviveRevokedCookie(t *testing.T) {
	now := time.Now()
	h := newPasswordHandler(t, func() time.Time { return now })
	value, err := h.issueSessionValue(now.Add(-2 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	renewing, resume := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	h.now = func() time.Time {
		if calls.Add(1) == 2 {
			close(renewing)
			<-resume
		}
		return now
	}
	req := httptest.NewRequest(http.MethodGet, "/private", nil)
	req.AddCookie(&http.Cookie{Name: h.sessionCookieName(), Value: value})
	rec := httptest.NewRecorder()
	served := make(chan struct{})
	go func() {
		h.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(rec, req)
		close(served)
	}()
	<-renewing
	rotated := make(chan struct{})
	go func() {
		h.credentialMu.Lock()
		state := h.credentials
		state.SessionSecret = bytes.Repeat([]byte{42}, sessionSecretBytes)
		h.replaceCredentials(state)
		h.credentialMu.Unlock()
		close(rotated)
	}()
	select {
	case <-rotated:
	case <-time.After(time.Second):
	}
	close(resume)
	<-served
	<-rotated
	if rec.Code != http.StatusNoContent {
		t.Fatalf("in-flight request = %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatal("aging session was not renewed")
	}
	if _, err := h.verifySessionValue(cookies[0].Value, now); err == nil {
		t.Fatal("renewal revived a cookie revoked by secret rotation")
	}
}

func TestLegacyBootstrapPasswordRemainsUsable(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("old printed bootstrap password"), bcrypt.MinCost)
	path := filepath.Join(t.TempDir(), "auth-state.json")
	raw, _ := json.Marshal(map[string]any{"version": 1, "password_hash": string(hash), "session_secret": strings.Repeat("c", 43)})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := EnsureBootstrapState(path, "")
	if err != nil {
		t.Fatal(err)
	}
	link, err := EnsureSetupURL(path, setupOrigin, time.Now())
	if err != nil || link != "" {
		t.Fatal("legacy hash replaced by setup")
	}
	h, err := New(Config{Mode: "password", PasswordStatePath: path, PasswordHash: result.State.PasswordHash, SessionSecret: result.State.SessionSecret}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if rec := authRequest(h, "/auth/password", map[string]string{"password": "old printed bootstrap password"}, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatal("legacy bootstrap sign-in broken")
	}
}

func TestStoredShortPasswordSignsInAndCanBeChanged(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("short"), bcrypt.MinCost)
	path := filepath.Join(t.TempDir(), "auth-state.json")
	raw, _ := json.Marshal(map[string]any{"version": 1, "password_hash": string(hash), "session_secret": strings.Repeat("c", 43)})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := EnsureBootstrapState(path, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	h, err := New(Config{Mode: "password", PasswordStatePath: path, SessionSecret: result.State.SessionSecret,
		AllowedOrigins: []string{setupOrigin}, Now: func() time.Time { return now }}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	login := authRequest(h, "/auth/password", map[string]string{"password": "short"}, nil, "")
	if login.Code != http.StatusNoContent {
		t.Fatalf("stored short password refused at sign-in: %d %s", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	csrf := ""
	for _, c := range cookies {
		if c.Name == h.csrfCookieName() {
			csrf = c.Value
		}
	}
	if rec := authRequest(h, "/auth/password/change", passwordUpdate{CurrentPassword: "short", Password: "tiny"}, cookies, csrf); rec.Code != http.StatusBadRequest {
		t.Fatalf("short replacement accepted: %d %s", rec.Code, rec.Body.String())
	}
	if rec := authRequest(h, "/auth/password/change", passwordUpdate{CurrentPassword: "short", Password: "long enough now"}, cookies, csrf); rec.Code != http.StatusNoContent {
		t.Fatalf("change from a short password refused: %d %s", rec.Code, rec.Body.String())
	}
	now = now.Add(time.Minute)
	if rec := authRequest(h, "/auth/password", map[string]string{"password": "long enough now"}, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("new password cannot sign in: %d", rec.Code)
	}
}

func TestSetupRejectsMalformedAndOversizedBodies(t *testing.T) {
	for _, body := range []string{`{}`, `{"token":"wrong","password":"x","extra":true}`, `{"token":"wrong","password":"x"}{}`, `{"token":"` + strings.Repeat("x", maxPasswordBody) + `"}`, `{"password":"` + strings.Repeat("x", 73) + `"}`} {
		h, _, _, _ := setupHandler(t)
		req := httptest.NewRequest(http.MethodPost, "/auth/setup", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", setupOrigin)
		rec := httptest.NewRecorder()
		h.handleSetup(rec, req)
		if rec.Code != http.StatusUnauthorized || !h.SetupPending() {
			t.Fatalf("malformed setup status %d", rec.Code)
		}
	}
}

func TestPasswordPinCannotBeChangedThroughSettings(t *testing.T) {
	h, token, path, _ := setupHandler(t)
	rec := authRequest(h, "/auth/setup", passwordUpdate{Token: token, Password: "original password"}, nil, "")
	if rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	h.cfg.PasswordPinned = true
	before, _ := os.ReadFile(path)
	login := authRequest(h, "/auth/password", map[string]string{"password": "original password"}, nil, "")
	cookies := login.Result().Cookies()
	csrf := ""
	for _, c := range cookies {
		if c.Name == h.csrfCookieName() {
			csrf = c.Value
		}
	}
	rec = authRequest(h, "/auth/password/change", passwordUpdate{CurrentPassword: "original password", Password: "new password"}, cookies, csrf)
	if rec.Code != http.StatusConflict {
		t.Fatal("pinned password changed")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("pinned auth state changed")
	}
}
