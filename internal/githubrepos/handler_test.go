package githubrepos

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/auth"
)

// newTestServer mounts the handler behind the real trusted-proxy auth
// middleware, so the principal path is the production one.
func newTestServer(t *testing.T, s *Service) *httptest.Server {
	t.Helper()
	authn, err := auth.New(auth.Config{
		Mode:           "trusted_proxy",
		ProxyAddresses: []string{"127.0.0.1"},
		IdentityHeader: "X-Identity",
	}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(s).Mount(mux.Handle, authn.Require)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, srv *httptest.Server, principal, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if principal != "" {
		req.Header.Set("X-Identity", principal)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func okService(t *testing.T) *Service {
	t.Helper()
	run := func(_ context.Context, argv []string) ([]byte, error) {
		if strings.Contains(argv[len(argv)-1], "page=1") {
			return ghResponse(200, "OK", nil, "["+repoJSON(1, "alice", "alpha", "User", "http://evil.example/x", false)+"]"), nil
		}
		return ghResponse(200, "OK", nil, repoJSON(7, "alice", "renamed", "User", "", true)), nil
	}
	return newTestService(authStatus("alice"), run)
}

func TestHandlerListAuthenticated(t *testing.T) {
	srv := newTestServer(t, okService(t))

	resp := get(t, srv, "op", "/api/github/repositories?account=alice")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control=%q", cc)
	}
	var body ListResult
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Outcome != "ok" || body.Account != "alice" || !body.Complete || len(body.Repositories) != 1 {
		t.Fatalf("body=%+v", body)
	}
	if body.Repositories[0].CloneURL != "https://github.com/alice/alpha.git" {
		t.Fatalf("clone url=%q", body.Repositories[0].CloneURL)
	}
}

func TestHandlerListPartialSSO(t *testing.T) {
	s := newTestService(authStatus("alice"), func(context.Context, []string) ([]byte, error) {
		return ghResponse(200, "OK", map[string]string{"X-GitHub-SSO": "partial-results; organizations=1"}, "["+repoJSON(1, "alice", "alpha", "User", "", false)+"]"), nil
	})
	srv := newTestServer(t, s)
	resp := get(t, srv, "op", "/api/github/repositories?account=alice")
	defer resp.Body.Close()
	var body struct {
		Outcome      string       `json:"outcome"`
		SSOPartial   bool         `json:"ssoPartial"`
		Repositories []Repository `json:"repositories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || body.Outcome != outcomeOK || !body.SSOPartial || len(body.Repositories) != 1 {
		t.Fatalf("status=%d body=%+v", resp.StatusCode, body)
	}
}

func TestHandlerListRefreshParameter(t *testing.T) {
	calls := 0
	s := okService(t)
	s.run = func(context.Context, []string) ([]byte, error) {
		calls++
		return ghResponse(200, "OK", nil, "[]"), nil
	}
	srv := newTestServer(t, s)

	for _, q := range []string{"?account=alice", "?account=alice&refresh=0", "?account=alice&refresh=1"} {
		resp := get(t, srv, "op", "/api/github/repositories"+q)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", resp.StatusCode)
		}
	}
	if calls != 2 {
		t.Fatalf("calls=%d (cache hit + refresh bypass)", calls)
	}
}

func TestHandlerUnauthenticated(t *testing.T) {
	srv := newTestServer(t, okService(t))

	for _, path := range []string{"/api/github/repositories?account=alice", "/api/github/repositories/7?account=alice"} {
		resp := get(t, srv, "", path)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s status=%d", path, resp.StatusCode)
		}
	}
}

func TestHandlerInvalidIdsAndMissingAccount(t *testing.T) {
	srv := newTestServer(t, okService(t))

	for _, path := range []string{
		"/api/github/repositories/abc?account=alice",
		"/api/github/repositories/-7?account=alice",
		"/api/github/repositories/0?account=alice",
		"/api/github/repositories",
		"/api/github/repositories/7",
	} {
		resp := get(t, srv, "op", path)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s status=%d", path, resp.StatusCode)
		}
	}
}

func TestHandlerRepository(t *testing.T) {
	srv := newTestServer(t, okService(t))

	resp := get(t, srv, "op", "/api/github/repositories/7?account=alice")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body RepositoryResult
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Outcome != "ok" || body.Account != "alice" || body.Repository == nil || body.Repository.ID != 7 {
		t.Fatalf("body=%+v", body)
	}
}

func TestHandlerStructuredFailureIsStill200(t *testing.T) {
	srv := newTestServer(t, newTestService(authStatus("bob"), nil))

	resp := get(t, srv, "op", "/api/github/repositories?account=alice")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body ListResult
	json.NewDecoder(resp.Body).Decode(&body)
	if body.Outcome != "account-changed" || body.Complete || body.Repositories == nil {
		t.Fatalf("body=%+v", body)
	}
}
