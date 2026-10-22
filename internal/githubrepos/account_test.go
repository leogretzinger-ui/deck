package githubrepos

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/noisefloor-oss/deck/internal/githubconnect"
)

func userRunner(body string) func(context.Context, []string) ([]byte, error) {
	return func(_ context.Context, argv []string) ([]byte, error) {
		if argv[len(argv)-1] != "/user" {
			return nil, errors.New("unexpected argv")
		}
		return ghResponse(200, "OK", nil, body), nil
	}
}

func TestAccountBuildsNoreplyAddress(t *testing.T) {
	s := newTestService(authStatus("alice"), userRunner(`{"id":583231,"login":"alice","name":" Alice Liddell ","email":"private@example.com"}`))
	got := s.Account(context.Background())
	want := AccountResult{Outcome: "ok", Account: "alice", Name: "Alice Liddell", Email: "583231+alice@users.noreply.github.com"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestAccountNameFallsBackToLogin(t *testing.T) {
	for _, name := range []string{`null`, `""`, `"  "`, `"two\nlines"`} {
		s := newTestService(authStatus("alice"), userRunner(`{"id":7,"login":"alice","name":`+name+`}`))
		if got := s.Account(context.Background()); got.Outcome != "ok" || got.Name != "alice" {
			t.Fatalf("name %s: got %+v", name, got)
		}
	}
}

func TestAccountFailures(t *testing.T) {
	signedOut := func(context.Context) githubconnect.Result {
		return githubconnect.Result{Outcome: githubconnect.OutcomeNoAccount}
	}
	moved := statusSeq(func(call int) githubconnect.Result {
		if call == 1 {
			return authResult("alice")
		}
		return authResult("bob")
	})
	unauthorized := func(context.Context, []string) ([]byte, error) {
		return ghResponse(401, "Unauthorized", nil, `{}`), nil
	}
	for name, tc := range map[string]struct {
		service *Service
		outcome string
	}{
		"signed out":     {newTestService(signedOut, nil), "authentication-required"},
		"other login":    {newTestService(authStatus("alice"), userRunner(`{"id":7,"login":"bob","name":"Bob"}`)), "account-changed"},
		"moved mid-read": {newTestService(moved, userRunner(`{"id":7,"login":"alice","name":"A"}`)), "account-changed"},
		"no id":          {newTestService(authStatus("alice"), userRunner(`{"login":"alice","name":"A"}`)), "malformed"},
		"bad login":      {newTestService(authStatus("alice"), userRunner(`{"id":7,"login":"al ice","name":"A"}`)), "malformed"},
		"unauthorized":   {newTestService(authStatus("alice"), unauthorized), "authentication-required"},
	} {
		got := tc.service.Account(context.Background())
		if got.Outcome != tc.outcome || got.Name != "" || got.Email != "" {
			t.Fatalf("%s: got %+v, want outcome %s and no identity", name, got, tc.outcome)
		}
	}
}

func TestHandlerAccount(t *testing.T) {
	srv := newTestServer(t, newTestService(authStatus("alice"), userRunner(`{"id":7,"login":"alice","name":"Alice"}`)))

	resp := get(t, srv, "", "/api/github/account")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", resp.StatusCode)
	}
	resp = get(t, srv, "op", "/api/github/account")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Fatalf("status=%d cache=%q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	var body AccountResult
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Email != "7+alice@users.noreply.github.com" || body.Name != "Alice" {
		t.Fatalf("body=%+v", body)
	}
}
