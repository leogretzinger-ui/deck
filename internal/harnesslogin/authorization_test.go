package harnesslogin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/rs/zerolog"
)

func TestAuthorizationReusesOnlyLiveNativeCodexAttempt(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"waiting", "foreign session", "foreign user", "claimed", "expired", "dead", "cancelled", "submitted", "pi"} {
		t.Run(scenario, func(t *testing.T) {
			provider := "codex"
			if scenario == "pi" {
				provider = "pi"
			}
			s, runtime, handle := fixture(t, provider)
			if err := s.Dismiss(ctx, "operator-a", handle.ID); err != nil {
				t.Fatal(err)
			}
			id := s.Pending(ctx, "operator-a", "session-a")[0].ID
			operator, session := "operator-a", "session-a"
			switch scenario {
			case "foreign session":
				session = "session-b"
			case "foreign user":
				operator = "operator-b"
			case "claimed", "cancelled", "submitted":
				handle, _ = s.Begin(ctx, operator, session, id)
				if scenario == "cancelled" {
					_ = s.Cancel(operator, handle.ID)
				}
				if scenario == "submitted" {
					runtime.dialError = true
					_, _ = s.Submit(ctx, operator, handle.ID, callbackURL)
				}
			case "expired":
				s.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
			case "dead":
				runtime.alive = false
			}
			url, err := s.Authorization(ctx, operator, session, id)
			if scenario == "waiting" {
				if err != nil || url != authURL("native-state") {
					t.Fatal("waiting attempt did not return the exact original URL")
				}
				if len(s.Pending(ctx, operator, session)) != 1 {
					t.Fatal("opening the URL claimed the attempt")
				}
			} else if err == nil || url != "" {
				t.Fatal("unavailable attempt exposed a URL")
			}
			if scenario == "expired" || scenario == "cancelled" || scenario == "submitted" || scenario == "pi" {
				if s.registrations[id].url != "" {
					t.Fatal("terminal or unsupported attempt retained URL")
				}
			}
		})
	}
}

func TestAuthorizationEndpointProtectsNativeURL(t *testing.T) {
	ctx := context.Background()
	s, _, handle := fixture(t, "codex")
	if err := s.Dismiss(ctx, "operator-a", handle.ID); err != nil {
		t.Fatal(err)
	}
	id := s.Pending(ctx, "operator-a", "session-a")[0].ID
	s.registrations[id].owner = auth.ProxyPrincipal("operator-a")
	h := NewHandler(s, []string{"https://deck.example"})
	a, err := auth.New(auth.Config{Mode: "trusted_proxy", ProxyAddresses: []string{"127.0.0.1"}, IdentityHeader: "X-Identity"}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"action": "authorization", "session": "session-a", "registration": id})
	for _, valid := range []bool{false, true} {
		req := httptest.NewRequest("POST", "/api/harness-login", strings.NewReader(string(body)))
		req.RemoteAddr = "127.0.0.1:30000"
		req.Header.Set("X-Identity", "operator-a")
		req.Header.Set("Origin", "https://deck.example")
		req.Header.Set("Content-Type", "application/json")
		if valid {
			req.Header.Set(CSRFHeader, h.csrf(auth.ProxyPrincipal("operator-a")))
		}
		out := httptest.NewRecorder()
		a.Require(h).ServeHTTP(out, req)
		if !valid {
			if out.Code != 403 || strings.Contains(out.Body.String(), "native-state") {
				t.Fatal("authorization URL escaped CSRF gate")
			}
			continue
		}
		var result struct {
			URL string `json:"url"`
		}
		if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &result) != nil || result.URL != authURL("native-state") {
			t.Fatalf("authorization result status %d", out.Code)
		}
		if out.Header().Get("Cache-Control") != "no-store" || out.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("authorization URL response missing privacy headers")
		}
	}
}

func TestAuthorizationAfterRejectedCallbackRetainsClaim(t *testing.T) {
	ctx := context.Background()
	s, _, handle := fixture(t, "codex")
	if _, err := s.Submit(ctx, "operator-a", handle.ID, "not-a-callback"); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid callback did not leave a retryable claim")
	}
	for _, tc := range []struct{ operator, session, handle string }{
		{"operator-b", "session-a", handle.ID},
		{"operator-a", "session-b", handle.ID},
		{"operator-a", "session-a", "unknown"},
	} {
		if url, err := s.AuthorizationForHandle(ctx, tc.operator, tc.session, tc.handle); err == nil || url != "" {
			t.Fatal("foreign or unknown handle exposed authorization URL")
		}
	}
	if url, err := s.AuthorizationForHandle(ctx, "operator-a", "session-a", handle.ID); err != nil || url != authURL("native-state") {
		t.Fatal("user could not reopen the same claimed native login")
	}
	if err := s.Cancel("operator-a", handle.ID); err != nil {
		t.Fatal(err)
	}
	if url, err := s.AuthorizationForHandle(ctx, "operator-a", "session-a", handle.ID); err == nil || url != "" {
		t.Fatal("consumed handle exposed authorization URL")
	}
}
