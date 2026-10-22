package auth

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStreamDeadlineComesFromVerifiedRequestCookie(t *testing.T) {
	h := builtinForMiddleware(t)
	if _, ok := SessionExpires(context.Background()); ok {
		t.Fatal("missing authentication acquired a deadline")
	}
	identity := Login{Issuer: "https://accounts.google.com", Subject: "operator-subject", Email: "operator@example.com"}
	token, err := h.issueLoginSession(h.now(), h.now(), identity)
	if err != nil {
		t.Fatal(err)
	}
	want := h.now().Add(SessionDuration).Truncate(time.Second)
	// Renewal may send a later cookie, but cannot extend the cookie already
	// admitted on this request or an existing stream opened with that cookie.
	later := NewLater(t, h, 2*time.Hour)
	called := false
	handler := later.Require(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		expires, ok := SessionExpires(r.Context())
		if !ok || !expires.Equal(want) {
			t.Fatalf("stream deadline = %v, want %v", expires, want)
		}
	}))
	r := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	r.AddCookie(&http.Cookie{Name: h.sessionCookieName(), Value: token})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, r)
	if !called || len(recorder.Result().Cookies()) == 0 {
		t.Fatal("fixture must admit the request and renew its cookie")
	}
	called = false
	r = httptest.NewRequest(http.MethodGet, "/api/events", nil)
	r.AddCookie(&http.Cookie{Name: h.sessionCookieName(), Value: token + "tampered"})
	handler.ServeHTTP(httptest.NewRecorder(), r)
	if called {
		t.Fatal("tampered cookie reached the stream handler")
	}
}

func TestBrowserLoginIdentitySurvivesRenewal(t *testing.T) {
	h := builtinForMiddleware(t)
	identity := Login{Issuer: "https://accounts.google.com", Subject: "operator-subject", Email: "operator@example.com"}
	first, err := h.issueLoginSession(h.now(), h.now(), identity)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.issueLoginSession(h.now(), h.now(), identity)
	if err != nil {
		t.Fatal(err)
	}
	firstPayload, err := h.verifySessionValue(first, h.now())
	if err != nil {
		t.Fatal(err)
	}
	firstLogin, ok := loginFromPayload(firstPayload)
	if !ok || len(firstLogin.ID) != 64 || firstLogin.Issuer != identity.Issuer || firstLogin.Subject != identity.Subject || firstLogin.Email != identity.Email {
		t.Fatal("login did not retain its verified identity and opaque ID")
	}
	secondPayload, err := h.verifySessionValue(second, h.now())
	if err != nil {
		t.Fatal(err)
	}
	secondLogin, ok := loginFromPayload(secondPayload)
	if !ok || firstLogin.ID == secondLogin.ID {
		t.Fatal("independent logins share a browser ID")
	}
	later := NewLater(t, h, 2*time.Hour)
	replacement, renewedPayload, err := later.sessionRenewal(firstPayload, later.now())
	if err != nil || replacement == "" || replacement == first {
		t.Fatalf("aging login did not renew: %v", err)
	}
	renewedLogin, ok := loginFromPayload(renewedPayload)
	if !ok || renewedLogin != firstLogin {
		t.Fatal("renewal changed the browser login identity")
	}
	for _, token := range []string{first, replacement} {
		request := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		request.AddCookie(&http.Cookie{Name: h.sessionCookieName(), Value: token})
		request.Header.Set("X-Session-ID", secondLogin.ID)
		called := false
		later.Require(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			called = true
			got, ok := CurrentLogin(r.Context())
			if !ok || got != firstLogin {
				t.Error("middleware lost or accepted a supplied browser identity")
			}
			if Principal(r.Context()) != principalHash("operator") {
				t.Error("browser identity changed the existing principal")
			}
		})).ServeHTTP(httptest.NewRecorder(), request)
		if !called {
			t.Fatal("verified browser request was refused")
		}
	}
}

func TestLegacySessionsDoNotInventBrowserOrGoogleIdentity(t *testing.T) {
	h := builtinForMiddleware(t)
	expiry := strconv.FormatInt(h.now().Add(24*time.Hour).Unix(), 10)
	for _, payload := range []string{
		"v1:" + expiry + ":00",
		"v2:" + strconv.FormatInt(h.now().Unix(), 10) + ":" + expiry + ":00",
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		r.AddCookie(&http.Cookie{Name: h.sessionCookieName(), Value: encodeSigned(payload, h.cfg.SessionSecret)})
		called := false
		h.Require(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			called = true
			if _, ok := CurrentLogin(r.Context()); ok {
				t.Error("legacy cookie acquired an identity never recorded at login")
			}
		})).ServeHTTP(httptest.NewRecorder(), r)
		if !called {
			t.Fatal("legacy authentication stopped working")
		}
	}
}

func TestPasswordLoginHasNoOIDCIdentity(t *testing.T) {
	h := builtinForMiddleware(t)
	value, err := h.issueSessionValue(h.now())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := h.verifySessionValue(value, h.now())
	if err != nil {
		t.Fatal(err)
	}
	login, ok := loginFromPayload(payload)
	if !ok || login.ID == "" || login.Issuer != "" || login.Subject != "" || login.Email != "" {
		t.Fatal("password login must identify the browser without claiming an OIDC ceremony")
	}
}

func TestBrowserLogoutUsesOnlyTheAuthenticatedLogin(t *testing.T) {
	h := builtinForMiddleware(t)
	var revoked []Login
	h.OnBrowserLogout(func(login Login) { revoked = append(revoked, login) })
	first, csrf := h.mintSession(t)
	second, _ := h.mintSession(t)
	payload, err := h.verifySessionValue(first.Value, h.now())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := loginFromPayload(payload)
	for _, token := range []string{"", csrf} {
		r := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
		r.AddCookie(first)
		r.Header.Set(CSRFHeader, token)
		rec := httptest.NewRecorder()
		h.handleLogout(rec, r)
		if token == "" && (rec.Code != http.StatusForbidden || len(revoked) != 0) {
			t.Fatal("logout without CSRF invoked browser revocation")
		}
		if token != "" && rec.Code != http.StatusNoContent {
			t.Fatalf("verified logout failed: %d", rec.Code)
		}
	}
	if len(revoked) != 1 || revoked[0] != want {
		t.Fatal("logout did not target exactly the authenticated browser login")
	}
	if _, err := h.verifySessionValue(second.Value, h.now()); err != nil {
		t.Fatal("another browser's login was invalidated")
	}
}

func TestLogoutRevokesBrowserWhenCallbackCrossesSessionExpiry(t *testing.T) {
	h := builtinForMiddleware(t)
	now := h.now()
	h.now = func() time.Time { return now }
	cookie, csrf := h.mintSession(t)
	payload, err := h.verifySessionValue(cookie.Value, now)
	if err != nil {
		t.Fatal(err)
	}
	want, ok := loginFromPayload(payload)
	if !ok {
		t.Fatal("fixture has no browser login")
	}
	called := 0
	h.OnLogout(func(string) { now = now.Add(SessionDuration + time.Second) })
	h.OnBrowserLogout(func(login Login) {
		called++
		if login != want {
			t.Error("revocation lost the identity verified at request admission")
		}
	})
	r := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	r.AddCookie(cookie)
	r.Header.Set(CSRFHeader, csrf)
	w := httptest.NewRecorder()
	h.handleLogout(w, r)
	if w.Code != http.StatusNoContent || called != 1 {
		t.Fatalf("status=%d browser revocations=%d", w.Code, called)
	}
	if _, err := h.verifySessionValue(cookie.Value, now); err == nil {
		t.Fatal("fixture did not cross session expiry")
	}
}

func TestMalformedBrowserLoginCookiesAreRefused(t *testing.T) {
	h := builtinForMiddleware(t)
	value, err := h.issueLoginSession(h.now(), h.now(), Login{
		Issuer: "https://accounts.google.com", Subject: "operator-subject", Email: "operator@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := h.verifySessionValue(value, h.now())
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func([]string){
		"short ID":              func(p []string) { p[3] = "00" },
		"non-hex ID":            func(p []string) { p[3] = strings.Repeat("z", 64) },
		"bad encoding":          func(p []string) { p[4] = "!" },
		"missing issuer":        func(p []string) { p[4] = "" },
		"missing subject":       func(p []string) { p[5] = "" },
		"missing email":         func(p []string) { p[6] = "" },
		"non-UTF8 email":        func(p []string) { p[6] = base64.RawURLEncoding.EncodeToString([]byte{255}) },
		"oversized claim":       func(p []string) { p[6] = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 1025))) },
		"future authentication": func(p []string) { p[1] = strconv.FormatInt(h.now().Add(time.Hour).Unix(), 10) },
		"expiry beyond cap":     func(p []string) { p[2] = strconv.FormatInt(h.now().Add(SessionMaxDuration+time.Second).Unix(), 10) },
	} {
		t.Run(name, func(t *testing.T) {
			parts := strings.Split(payload, ":")
			change(parts)
			malformed := encodeSigned(strings.Join(parts, ":"), h.cfg.SessionSecret)
			if _, err := h.verifySessionValue(malformed, h.now()); err == nil {
				t.Fatal("malformed signed browser identity accepted")
			}
		})
	}
}
