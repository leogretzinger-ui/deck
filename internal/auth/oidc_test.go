package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"
)

// fakeGoogle is a deterministic OIDC provider: a JWKS endpoint and a token
// endpoint that mints an RS256 id_token from a claims template. Tests drive
// the real handler against it — no network, no real Google (doc.go says the
// endpoints are seams for exactly this).
type fakeGoogle struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	issuer string
	// claims applied to the next minted token; a test tweaks these to force
	// each refusal path.
	claims func() jwt.MapClaims
}

func newFakeGoogle(t *testing.T, clientID, email string) *fakeGoogle {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	f := &fakeGoogle{key: key}
	f.claims = func() jwt.MapClaims {
		return jwt.MapClaims{
			"iss":            f.issuer,
			"aud":            clientID,
			"sub":            "operator-subject-id",
			"email":          email,
			"email_verified": true,
			"exp":            time.Now().Add(time.Hour).Unix(),
			"iat":            time.Now().Add(-time.Minute).Unix(),
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/certs", func(w http.ResponseWriter, _ *http.Request) {
		pub := key.Public().(*rsa.PublicKey)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test-key",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	// nonce for the next minted token, set by the login step via the store.
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		claims := f.claims()
		if n := currentNonce; n != "" {
			claims["nonce"] = n
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "test-key"
		signed, err := tok.SignedString(key)
		if err != nil {
			http.Error(w, "sign", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "unused", "id_token": signed, "token_type": "Bearer",
		})
	})
	f.srv = httptest.NewServer(mux)
	f.issuer = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

// currentNonce bridges the login step's nonce to the fake token minter. A
// package var is safe: these tests do not run a login concurrently.
var currentNonce string

func newBuiltinHandler(t *testing.T, f *fakeGoogle, clientID, email string) *Handler {
	t.Helper()
	h, err := New(Config{
		Mode:          "builtin",
		ClientID:      clientID,
		ClientSecret:  "test-secret",
		RedirectURL:   "http://127.0.0.1:8837/auth/callback",
		AllowedEmail:  email,
		SessionSecret: []byte("0123456789abcdef0123456789abcdef"),
		Issuer:        f.issuer,
		AuthURL:       f.issuer + "/authorize",
		TokenURL:      f.srv.URL + "/token",
		JWKSURL:       f.srv.URL + "/certs",
		Client:        f.srv.Client(),
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// login runs /auth/login and returns the txn cookie and the state+nonce the
// provider will see, so a test can craft the matching callback.
func login(t *testing.T, h *Handler) (txn *http.Cookie, state string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handleLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("login = %d", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	state = loc.Query().Get("state")
	currentNonce = loc.Query().Get("nonce")
	for _, c := range rec.Result().Cookies() {
		if strings.Contains(c.Name, "oidc_txn") {
			txn = c
		}
	}
	if txn == nil {
		t.Fatal("login set no transaction cookie")
	}
	return txn, state
}

func callback(t *testing.T, h *Handler, txn *http.Cookie, state string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=test-code&state="+url.QueryEscape(state), nil)
	req.AddCookie(txn)
	rec := httptest.NewRecorder()
	h.handleCallback(rec, req)
	return rec
}

func TestLoginAccountSelection(t *testing.T) {
	f := newFakeGoogle(t, "fixture-client", "owner@example.test")
	h := newBuiltinHandler(t, f, "fixture-client", "owner@example.test")
	for _, query := range []string{"", "?choose_account=1", "?choose_account=0", "?choose_account=consent&prompt=none"} {
		t.Run(query, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.handleLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/login"+query, nil))
			if rec.Code != http.StatusFound {
				t.Fatalf("login returned %d", rec.Code)
			}
			target, err := url.Parse(rec.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			prompt := ""
			if query == "?choose_account=1" {
				prompt = "select_account"
			}
			if target.Query().Get("prompt") != prompt || target.Query().Get("redirect_uri") != h.cfg.RedirectURL ||
				target.Query().Get("state") == "" || target.Query().Get("nonce") == "" || target.Query().Get("code_challenge_method") != "S256" {
				t.Fatal("account selection changed the fixed login contract")
			}
		})
	}
}

func TestFullLoginIssuesSessionAndCSRF(t *testing.T) {
	const clientID, email = "client-id", "operator@example.com"
	f := newFakeGoogle(t, clientID, email)
	h := newBuiltinHandler(t, f, clientID, email)

	txn, state := login(t, h)
	rec := callback(t, h, txn, state)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d, want 303 (%s)", rec.Code, rec.Body)
	}
	var session, csrf *http.Cookie
	for _, c := range rec.Result().Cookies() {
		switch {
		case strings.Contains(c.Name, "session"):
			session = c
		case strings.Contains(c.Name, "csrf"):
			csrf = c
		}
	}
	if session == nil || !session.HttpOnly {
		t.Fatalf("session cookie missing or not HttpOnly: %+v", session)
	}
	if csrf == nil || csrf.HttpOnly {
		t.Fatalf("csrf cookie must exist and be readable by JS: %+v", csrf)
	}
	// The issued session must authenticate a subsequent request.
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.AddCookie(session)
	if _, err := h.sessionPayload(req); err != nil {
		t.Fatalf("issued session does not verify: %v", err)
	}
	called := false
	h.Require(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		identity, ok := CurrentLogin(r.Context())
		if !ok || identity.ID == "" || identity.Issuer != f.issuer || identity.Subject != "operator-subject-id" || identity.Email != email {
			t.Error("verified OIDC claims did not reach the authenticated browser context")
		}
	})).ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("issued OIDC session was refused by middleware")
	}
}

func TestSessionProbeGatesTheApplicationShell(t *testing.T) {
	const clientID, email = "client-id", "operator@example.com"
	f := newFakeGoogle(t, clientID, email)
	h := newBuiltinHandler(t, f, clientID, email)
	mux := http.NewServeMux()
	h.Mount(func(pattern string, handler http.Handler) { mux.Handle(pattern, handler) })

	unauthenticated := httptest.NewRecorder()
	mux.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/auth/session", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated probe = %d, want 401", unauthenticated.Code)
	}
	if got := unauthenticated.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("unauthenticated Cache-Control = %q, want no-store", got)
	}
	if got := unauthenticated.Header().Get("X-Noisedeck-Auth-Mode"); got != "builtin" {
		t.Fatalf("unauthenticated auth mode = %q, want builtin", got)
	}

	txn, state := login(t, h)
	loginResponse := callback(t, h, txn, state)
	var session *http.Cookie
	for _, cookie := range loginResponse.Result().Cookies() {
		if strings.Contains(cookie.Name, "session") {
			session = cookie
			break
		}
	}
	if session == nil {
		t.Fatal("login issued no session cookie")
	}
	authenticated := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(session)
	mux.ServeHTTP(authenticated, req)
	if authenticated.Code != http.StatusNoContent {
		t.Fatalf("authenticated probe = %d, want 204", authenticated.Code)
	}
	if got := authenticated.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("authenticated Cache-Control = %q, want no-store", got)
	}
}

func TestNoneSessionProbeIsImmediatelyAuthorized(t *testing.T) {
	h, err := New(Config{Mode: "none"}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Mount(func(pattern string, handler http.Handler) { mux.Handle(pattern, handler) })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/session", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("none probe = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("X-Noisedeck-Auth-Mode"); got != "none" {
		t.Fatalf("none auth mode header = %q", got)
	}
}

func TestCallbackRefusals(t *testing.T) {
	const clientID, email = "client-id", "operator@example.com"

	cases := []struct {
		name   string
		break_ func(f *fakeGoogle, txn *http.Cookie, state *string)
	}{
		{"wrong email", func(f *fakeGoogle, _ *http.Cookie, _ *string) {
			base := f.claims
			f.claims = func() jwt.MapClaims { c := base(); c["email"] = "intruder@example.com"; return c }
		}},
		{"email unverified", func(f *fakeGoogle, _ *http.Cookie, _ *string) {
			base := f.claims
			f.claims = func() jwt.MapClaims { c := base(); c["email_verified"] = false; return c }
		}},
		{"wrong audience", func(f *fakeGoogle, _ *http.Cookie, _ *string) {
			base := f.claims
			f.claims = func() jwt.MapClaims { c := base(); c["aud"] = "someone-else"; return c }
		}},
		{"expired token", func(f *fakeGoogle, _ *http.Cookie, _ *string) {
			base := f.claims
			f.claims = func() jwt.MapClaims { c := base(); c["exp"] = time.Now().Add(-time.Hour).Unix(); return c }
		}},
		{"empty subject", func(f *fakeGoogle, _ *http.Cookie, _ *string) {
			base := f.claims
			f.claims = func() jwt.MapClaims { c := base(); c["sub"] = ""; return c }
		}},
		{"tampered state", func(_ *fakeGoogle, _ *http.Cookie, state *string) {
			*state = "not-the-real-state"
		}},
		{"padded audience list", func(f *fakeGoogle, _ *http.Cookie, _ *string) {
			base := f.claims
			f.claims = func() jwt.MapClaims { c := base(); c["aud"] = []string{"client-id", "another-app"}; return c }
		}},
		{"issued in the future", func(f *fakeGoogle, _ *http.Cookie, _ *string) {
			base := f.claims
			f.claims = func() jwt.MapClaims { c := base(); c["iat"] = time.Now().Add(24 * time.Hour).Unix(); return c }
		}},
		{"control character in subject", func(f *fakeGoogle, _ *http.Cookie, _ *string) {
			base := f.claims
			f.claims = func() jwt.MapClaims { c := base(); c["sub"] = "op\x00erator"; return c }
		}},
		{"non-ascii subject", func(f *fakeGoogle, _ *http.Cookie, _ *string) {
			base := f.claims
			f.claims = func() jwt.MapClaims { c := base(); c["sub"] = "operatör"; return c }
		}},
		{"null azp", func(f *fakeGoogle, _ *http.Cookie, _ *string) {
			base := f.claims
			f.claims = func() jwt.MapClaims { c := base(); c["azp"] = nil; return c }
		}},
		{"present empty azp", func(f *fakeGoogle, _ *http.Cookie, _ *string) {
			base := f.claims
			f.claims = func() jwt.MapClaims { c := base(); c["azp"] = ""; return c }
		}},
		{"present wrong azp", func(f *fakeGoogle, _ *http.Cookie, _ *string) {
			base := f.claims
			f.claims = func() jwt.MapClaims { c := base(); c["azp"] = "some-other-app"; return c }
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeGoogle(t, clientID, email)
			h := newBuiltinHandler(t, f, clientID, email)
			txn, state := login(t, h)
			c.break_(f, txn, &state)
			rec := callback(t, h, txn, state)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s: callback = %d, want 401", c.name, rec.Code)
			}
			if strings.Contains(strings.ToLower(rec.Body.String()), "email") ||
				strings.Contains(rec.Body.String(), "@") {
				t.Fatalf("%s: response leaked detail: %q", c.name, rec.Body.String())
			}
		})
	}
}

// In secure mode the transaction cookie must be a valid __Host- cookie:
// __Host- requires Path=/, and a browser rejects any other path.
func TestSecureTxnCookieIsHostValid(t *testing.T) {
	const clientID, email = "client-id", "operator@example.com"
	f := newFakeGoogle(t, clientID, email)
	h, err := New(Config{
		Mode: "builtin", ClientID: clientID, ClientSecret: "s",
		RedirectURL:  "https://console.example.test/auth/callback",
		AllowedEmail: email, SessionSecret: []byte("0123456789abcdef0123456789abcdef"),
		SecureCookies: true,
		Issuer:        f.issuer, AuthURL: f.issuer + "/authorize",
		TokenURL: f.srv.URL + "/token", JWKSURL: f.srv.URL + "/certs", Client: f.srv.Client(),
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := httptest.NewRecorder()
	h.handleLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	for _, c := range rec.Result().Cookies() {
		if strings.HasPrefix(c.Name, "__Host-") {
			if c.Path != "/" || !c.Secure {
				t.Fatalf("__Host- cookie needs Path=/ and Secure, got path=%q secure=%v", c.Path, c.Secure)
			}
			return
		}
	}
	t.Fatal("no __Host- transaction cookie set in secure mode")
}

// A transaction is consumed exactly once: a replayed callback finds nothing.
func TestTransactionConsumedOnce(t *testing.T) {
	const clientID, email = "client-id", "operator@example.com"
	f := newFakeGoogle(t, clientID, email)
	h := newBuiltinHandler(t, f, clientID, email)

	txn, state := login(t, h)
	if rec := callback(t, h, txn, state); rec.Code != http.StatusSeeOther {
		t.Fatalf("first callback = %d", rec.Code)
	}
	if rec := callback(t, h, txn, state); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed callback = %d, want 401", rec.Code)
	}
}
