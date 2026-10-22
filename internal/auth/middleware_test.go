package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"strconv"
	"strings"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func builtinForMiddleware(t *testing.T) *Handler {
	t.Helper()
	h, err := New(Config{
		Mode:          "builtin",
		ClientID:      "client-id",
		ClientSecret:  "s",
		RedirectURL:   "http://127.0.0.1:8837/auth/callback",
		AllowedEmail:  "operator@example.com",
		SessionSecret: []byte("0123456789abcdef0123456789abcdef"),
		JWKSURL:       "http://127.0.0.1:1/certs", // never hit: no login here
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// A minted session + its derived CSRF token, for exercising the middleware
// without running the whole OIDC dance.
func (h *Handler) mintSession(t *testing.T) (session *http.Cookie, csrf string) {
	t.Helper()
	value, err := h.issueSessionValue(h.now())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	payload, err := h.verifySessionValue(value, h.now())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return &http.Cookie{Name: h.sessionCookieName(), Value: value}, h.csrfFor(payload)
}

func TestBuiltinRequireRejectsNoSession(t *testing.T) {
	h := builtinForMiddleware(t)
	rec := httptest.NewRecorder()
	h.Require(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session GET = %d, want 401", rec.Code)
	}
}

func TestBuiltinRequireAllowsValidSession(t *testing.T) {
	h := builtinForMiddleware(t)
	session, _ := h.mintSession(t)
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	h.Require(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("valid session GET = %d, want 204", rec.Code)
	}
}

func TestBuiltinCSRFOnStateChange(t *testing.T) {
	h := builtinForMiddleware(t)
	session, csrf := h.mintSession(t)

	// POST without the CSRF header is forbidden even with a valid session.
	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	h.Require(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF = %d, want 403", rec.Code)
	}

	// With the matching token it passes.
	req = httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.AddCookie(session)
	req.Header.Set(CSRFHeader, csrf)
	rec = httptest.NewRecorder()
	h.Require(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST with CSRF = %d, want 204", rec.Code)
	}

	// A wrong token is refused.
	req = httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.AddCookie(session)
	req.Header.Set(CSRFHeader, "wrong")
	rec = httptest.NewRecorder()
	h.Require(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST wrong CSRF = %d, want 403", rec.Code)
	}
}

func proxyHandler(t *testing.T, addrs []string) *Handler {
	t.Helper()
	h, err := New(Config{
		Mode:           "trusted_proxy",
		ProxyAddresses: addrs,
		IdentityHeader: "X-Forwarded-User",
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func TestTrustedProxyMatrix(t *testing.T) {
	h := proxyHandler(t, []string{"10.0.0.5", "192.168.1.0/24"})

	// A downstream that echoes whether it saw the identity header, so we can
	// prove the header is consumed before the request continues.
	var sawHeader string
	sink := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHeader = r.Header.Get("X-Forwarded-User")
		w.WriteHeader(http.StatusNoContent)
	})

	cases := []struct {
		name     string
		remote   string
		header   string
		wantCode int
	}{
		{"trusted exact ip with identity", "10.0.0.5:5555", "operator", http.StatusNoContent},
		{"trusted cidr with identity", "192.168.1.42:5555", "operator", http.StatusNoContent},
		{"trusted ip no identity", "10.0.0.5:5555", "", http.StatusUnauthorized},
		{"untrusted ip", "203.0.113.9:5555", "operator", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sawHeader = "sentinel"
			req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
			req.RemoteAddr = c.remote
			if c.header != "" {
				req.Header.Set("X-Forwarded-User", c.header)
			}
			rec := httptest.NewRecorder()
			h.Require(sink).ServeHTTP(rec, req)
			if rec.Code != c.wantCode {
				t.Fatalf("%s: %d, want %d", c.name, rec.Code, c.wantCode)
			}
			if c.wantCode == http.StatusNoContent && sawHeader != "" {
				t.Fatalf("%s: identity header must be consumed before downstream, saw %q", c.name, sawHeader)
			}
		})
	}
}

// A tampered session payload or one past its expiry must not authenticate.
func TestSessionTamperAndExpiry(t *testing.T) {
	h := builtinForMiddleware(t)

	// Tamper: flip a byte of a valid token's payload; the MAC no longer
	// matches.
	value, _ := h.issueSessionValue(h.now())
	tampered := "AAAA" + value[4:]
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.AddCookie(&http.Cookie{Name: h.sessionCookieName(), Value: tampered})
	if _, err := h.sessionPayload(req); err == nil {
		t.Fatal("tampered session verified")
	}

	// Expiry: a handler whose clock is a day and change ahead of issuance
	// rejects the same token.
	future := New1DayLater(t, h)
	req2 := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req2.AddCookie(&http.Cookie{Name: future.sessionCookieName(), Value: value})
	if _, err := future.sessionPayload(req2); err == nil {
		t.Fatal("expired session verified")
	}
}

// A session in use slides: a request from a handler whose clock has moved
// two hours re-issues the cookies, and the renewed token verifies a full
// window past the renewal — where the original would already be dead.
func TestSessionSlidesOnUse(t *testing.T) {
	h := builtinForMiddleware(t)
	session, _ := h.mintSession(t)

	later := NewLater(t, h, 2*time.Hour)
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	later.Require(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("aging session GET = %d, want 204", rec.Code)
	}
	renewed := ""
	for _, c := range rec.Result().Cookies() {
		if c.Name == later.sessionCookieName() {
			renewed = c.Value
		}
	}
	if renewed == "" {
		t.Fatal("a two-hour-old session in use was not renewed")
	}
	deepFuture := NewLater(t, h, 2*time.Hour+SessionDuration-time.Minute)
	if _, err := deepFuture.verifySessionValue(renewed, deepFuture.now()); err != nil {
		t.Fatalf("the renewed session did not carry a fresh window: %v", err)
	}
	if _, err := deepFuture.verifySessionValue(session.Value, deepFuture.now()); err == nil {
		t.Fatal("the original token outlived its own window")
	}
}

// A fresh session is left alone: renewing on every request would set
// cookies on every response for nothing.
func TestFreshSessionIsNotRenewed(t *testing.T) {
	h := builtinForMiddleware(t)
	session, _ := h.mintSession(t)
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	h.Require(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("fresh session GET = %d, want 204", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatalf("a fresh session was renewed: %d cookies set", len(rec.Result().Cookies()))
	}
}

// The absolute cap holds through renewal: a session whose window would
// cross the cap renews clamped TO the cap, not a full window past it — one
// login cannot slide forever. The timeline issues below the cap so the
// renewal genuinely extends: auth at 0, minted at +59d (expiry +89d),
// renewed at +61d (window would give +91d, clamped to +90d).
func TestSessionCapClampsRenewal(t *testing.T) {
	h := builtinForMiddleware(t)
	minted := NewLater(t, h, 59*24*time.Hour)
	value, err := h.issueSessionValueAt(h.now(), minted.now())
	if err != nil {
		t.Fatalf("issue below the cap: %v", err)
	}
	renewPoint := NewLater(t, h, 61*24*time.Hour)
	payload, err := renewPoint.verifySessionValue(value, renewPoint.now())
	if err != nil {
		t.Fatalf("verify below the cap: %v", err)
	}
	renewedValue, _, err := renewPoint.sessionRenewal(payload, renewPoint.now())
	if err != nil {
		t.Fatalf("renewal below the cap: %v", err)
	}
	if renewedValue == "" {
		t.Fatal("an aging session under its cap was not renewed")
	}
	justUnderCap := NewLater(t, h, SessionMaxDuration-time.Minute)
	if _, err := justUnderCap.verifySessionValue(renewedValue, justUnderCap.now()); err != nil {
		t.Fatalf("the clamped renewal died before the cap: %v", err)
	}
	pastCap := NewLater(t, h, SessionMaxDuration+time.Minute)
	if _, err := pastCap.verifySessionValue(renewedValue, pastCap.now()); err == nil {
		t.Fatal("a renewal carried the session past its absolute cap")
	}
}

// A token already clamped to the cap is left alone: re-issuing cannot
// extend it, and rotating both cookies on every request for the session's
// final stretch is churn with no security content.
func TestClampedSessionIsNotReRenewed(t *testing.T) {
	h := builtinForMiddleware(t)
	// A token minted at the cap edge is clamped from issue: its expiry IS
	// the cap, and no request afterwards should rotate the cookies again.
	nearCap := NewLater(t, h, SessionMaxDuration-2*time.Hour)
	value, err := h.issueSessionValueAt(h.now(), nearCap.now())
	if err != nil {
		t.Fatalf("issue at cap edge: %v", err)
	}
	payload, err := nearCap.verifySessionValue(value, nearCap.now())
	if err != nil {
		t.Fatalf("verify at cap edge: %v", err)
	}
	again, _, err := nearCap.sessionRenewal(payload, nearCap.now().Add(sessionRenewAfter+time.Minute))
	if err != nil {
		t.Fatalf("re-renewal check: %v", err)
	}
	if again != "" {
		t.Fatal("a clamped session was re-issued on the next request")
	}
}

// v1 tokens keep verifying until their own expiry, so shipping v2 logs
// nobody out; a v1 session in use renews INTO v2 with the cap counted from
// the renewal, because its true login time was never recorded.
func TestV1SessionVerifiesAndRenewsIntoV2(t *testing.T) {
	h := builtinForMiddleware(t)
	// Every deployed v1 token was minted under the legacy 24-hour window;
	// the fixture encodes that reality rather than today's constant.
	const legacyV1Window = 24 * time.Hour
	payload := "v1:" + strconv.FormatInt(h.now().Add(legacyV1Window).Unix(), 10) + ":00"
	v1 := encodeSigned(payload, h.cfg.SessionSecret)
	if _, err := h.verifySessionValue(v1, h.now()); err != nil {
		t.Fatalf("v1 no longer verifies: %v", err)
	}
	later := NewLater(t, h, 2*time.Hour)
	verified, err := later.verifySessionValue(v1, later.now())
	if err != nil {
		t.Fatalf("aging v1 no longer verifies: %v", err)
	}
	value, renewed, err := later.sessionRenewal(verified, later.now())
	if err != nil || value == "" {
		t.Fatalf("aging v1 did not renew: %v", err)
	}
	if !strings.HasPrefix(renewed, "v2:") {
		t.Fatalf("v1 renewed into %q, want v2", renewed)
	}
}

// NewLater clones h with a clock moved forward by d, sharing the secret so
// tokens are comparable across the pair.
func NewLater(t *testing.T, h *Handler, d time.Duration) *Handler {
	t.Helper()
	later, err := New(Config{
		Mode:          "builtin",
		ClientID:      "client-id",
		ClientSecret:  "s",
		RedirectURL:   "http://127.0.0.1:8837/auth/callback",
		AllowedEmail:  "operator@example.com",
		SessionSecret: h.cfg.SessionSecret,
		JWKSURL:       "http://127.0.0.1:1/certs",
		Now:           func() time.Time { return h.now().Add(d) },
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return later
}

// New1DayLater clones h with a clock advanced past SessionDuration, sharing
// the same secret so tokens are comparable.
func New1DayLater(t *testing.T, h *Handler) *Handler {
	t.Helper()
	future, err := New(Config{
		Mode:          "builtin",
		ClientID:      "client-id",
		ClientSecret:  "s",
		RedirectURL:   "http://127.0.0.1:8837/auth/callback",
		AllowedEmail:  "operator@example.com",
		SessionSecret: h.cfg.SessionSecret,
		JWKSURL:       "http://127.0.0.1:1/certs",
		Now:           func() time.Time { return h.now().Add(SessionDuration + time.Hour) },
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return future
}

// Logout clears both cookies but only under a valid CSRF token.
func TestLogoutRequiresCSRF(t *testing.T) {
	h := builtinForMiddleware(t)
	var revoked string
	h.OnLogout(func(principal string) { revoked = principal })
	session, csrf := h.mintSession(t)

	// Without CSRF: forbidden, nothing cleared.
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	h.handleLogout(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF = %d, want 403", rec.Code)
	}
	if revoked != "" {
		t.Fatal("unauthenticated logout revoked preview access")
	}

	// With CSRF: 204 and both cookies expired.
	req = httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(session)
	req.Header.Set(CSRFHeader, csrf)
	rec = httptest.NewRecorder()
	h.handleLogout(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout with CSRF = %d, want 204", rec.Code)
	}
	if revoked != principalHash("operator") {
		t.Fatal("logout did not revoke the user's preview access")
	}
	cleared := 0
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			cleared++
		}
	}
	if cleared != 2 {
		t.Fatalf("logout cleared %d cookies, want 2 (session + csrf)", cleared)
	}
}

// An untrusted source's identity header must never reach a handler, even if
// the request is somehow allowed later — it is stripped on refusal.
func TestUntrustedHeaderStripped(t *testing.T) {
	h := proxyHandler(t, []string{"10.0.0.5"})
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set("X-Forwarded-User", "spoofed")
	rec := httptest.NewRecorder()
	h.Require(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("untrusted = %d, want 403", rec.Code)
	}
	// The spoofed header must be stripped even though the request was
	// refused — nothing downstream may ever see it.
	if got := req.Header.Get("X-Forwarded-User"); got != "" {
		t.Fatalf("spoofed identity header survived refusal: %q", got)
	}
}

func TestNonePassesWithoutSessionOrCSRF(t *testing.T) {
	h, err := New(Config{Mode: "none"}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	h.Require(next).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/test", nil))
	if !called || rec.Code != http.StatusNoContent {
		t.Fatalf("none mode called=%v status=%d", called, rec.Code)
	}
}
