package workspaceservice

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacegateway"
	"github.com/noisefloor-oss/deck/internal/workspacegrants"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
)

func (f *fixture) gateway() http.Handler {
	f.t.Helper()
	invites, err := workspacegateway.NewInvitations(f.ctx, f.db, f.keys, f.actor.Login.Email, time.Now)
	must(f.t, err)
	handler, err := workspacegateway.New(workspacegateway.Config{InstanceID: "instance", Origin: "https://deck.example", RPID: "deck.example", Auth: auth.Config{
		Mode: "oidc", ClientID: "test-client", ClientSecret: "synthetic-google-client", SessionSecret: bytes.Repeat([]byte{7}, 32), SecureCookies: true,
		RedirectURL: "https://deck.example/auth/callback", AllowedEmail: f.actor.Login.Email,
	}}, f.keys, invites, workspacegateway.Options{
		Assets: fstest.MapFS{"index.html": {Data: []byte("Security")}}, Runtime: f.service, Operations: f.service, Access: f.grants,
	})
	must(f.t, err)
	return workspacegrants.CookieContext(handler)
}

// Knowing the ordinary Google-cookie signing secret does not supply a method
// proof, independent enrollment receipt or browser unlock cookie.
func signedRequest(t *testing.T, method, path string, body any, login auth.Login, cookies ...*http.Cookie) *http.Request {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		must(t, err)
	}
	r := httptest.NewRequest(method, "https://deck.example"+path, bytes.NewReader(encoded))
	r.Header.Set("Origin", "https://deck.example")
	r.Header.Set("Content-Type", "application/json")
	now := time.Now()
	fields := []string{"v3", fmt.Sprint(now.Unix()), fmt.Sprint(now.Add(time.Hour).Unix()), login.ID}
	for _, value := range []string{login.Issuer, login.Subject, login.Email} {
		fields = append(fields, base64.RawURLEncoding.EncodeToString([]byte(value)))
	}
	payload := strings.Join(fields, ":")
	sign := func(value string) string {
		mac := hmac.New(sha256.New, bytes.Repeat([]byte{7}, 32))
		_, _ = mac.Write([]byte(value))
		return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	r.AddCookie(&http.Cookie{Name: "__Host-noisedeck_session", Value: base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sign(payload)})
	r.Header.Set(auth.CSRFHeader, sign("csrf:"+payload))
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	return r
}

func send(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHTTPProofToNativeRuntimeToIndependentBrowserCookie(t *testing.T) {
	f := newFixture(t)
	h := f.gateway()
	challenge, err := f.keys.BeginChallenge(f.ctx, f.actor, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
	must(t, err)
	body := map[string]string{"challengeId": challenge.ID, "signature": signature(t, f.day.key, challenge), "volumeSecret": base64.RawURLEncoding.EncodeToString(f.secret)}
	// Even a fully forged Google session cannot submit the real proof without
	// the independent bootstrap receipt to which the challenge is bound.
	w := send(h, signedRequest(t, "POST", "/api/security/v1/operations", body, f.actor.Login))
	if w.Code != http.StatusForbidden || len(f.native.calls) != 0 {
		t.Fatal("Google-only bootstrap reached native effects", w.Code)
	}
	w = send(h, signedRequest(t, "POST", "/api/security/v1/operations", body, f.actor.Login,
		&http.Cookie{Name: "__Host-deck_enrollment", Value: f.receiptToken}))
	if w.Code != http.StatusOK {
		t.Fatalf("initialization status=%d response=%s", w.Code, w.Body)
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == workspacegrants.CookieName {
			cookie = c
		}
	}
	if cookie == nil || !cookie.Secure || !cookie.HttpOnly || cookie.Domain != "" || cookie.Path != "/" {
		t.Fatal("native completion did not deliver a protected workspace cookie")
	}
	if strings.Contains(w.Body.String(), body["volumeSecret"]) || strings.Contains(w.Body.String(), cookie.Value) {
		t.Fatal("operation response exposed transient handoff or bearer")
	}
	w = send(h, signedRequest(t, "GET", "/api/security/v1/methods", nil, f.actor.Login))
	if w.Code != http.StatusForbidden {
		t.Fatal("running workspace disclosed methods to Google alone", w.Code)
	}
	w = send(h, signedRequest(t, "GET", "/api/security/v1/methods", nil, f.actor.Login, cookie))
	if w.Code != http.StatusOK {
		t.Fatal("new cookie did not authorize browser", w.Code)
	}
	before := len(f.native.calls)
	challenge, err = f.keys.BeginChallenge(f.ctx, f.actor, workspacekeys.Action{Operation: workspacekeys.Lock, Method: f.day.ref})
	must(t, err)
	body = map[string]string{"challengeId": challenge.ID, "signature": signature(t, f.day.key, challenge)}
	w = send(h, signedRequest(t, "POST", "/api/security/v1/operations", body, f.actor.Login, cookie))
	if w.Code != http.StatusOK || len(f.native.calls) != before+1 || !f.native.state.Locked {
		t.Fatal("explicit lock did not reach native closure", w.Code)
	}
	w = send(h, signedRequest(t, "GET", "/api/security/v1/methods", nil, f.actor.Login, cookie))
	if w.Code != http.StatusForbidden {
		t.Fatal("old browser cookie survived lock", w.Code)
	}
	w = send(h, signedRequest(t, "GET", "/api/security/v1/operations/"+challenge.OperationID, nil, f.actor.Login))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"locked":true`) || !strings.Contains(w.Body.String(), `"status":"complete"`) {
		t.Fatalf("revoked browser cannot observe its lock: status=%d response=%s", w.Code, w.Body)
	}
}
