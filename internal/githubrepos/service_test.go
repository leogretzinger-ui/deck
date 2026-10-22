package githubrepos

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/githubconnect"
)

// ghResponse builds gh api --include stdout: status line, headers, body.
func ghResponse(code int, status string, headers map[string]string, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", code, status)
	for k, v := range headers {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

func linkNext(page int) string {
	return fmt.Sprintf(`<https://api.github.com/user/repos?page=%d>; rel="next", <https://api.github.com/user/repos?page=1>; rel="first"`, page)
}

func repoJSON(id int64, owner, name, ownerType, clone string, private bool) string {
	return fmt.Sprintf(`{"id":%d,"name":%q,"full_name":%q,"private":%t,"description":"d","clone_url":%q,"owner":{"login":%q,"type":%q}}`,
		id, name, owner+"/"+name, private, clone, owner, ownerType)
}

func authResult(login string) githubconnect.Result {
	return githubconnect.Result{
		Outcome: githubconnect.OutcomeAuthenticated,
		Status:  &githubconnect.Status{Account: githubconnect.Account{Login: login, Active: true, State: githubconnect.StateSuccess}},
	}
}

func authStatus(login string) func(context.Context) githubconnect.Result {
	return func(context.Context) githubconnect.Result {
		return githubconnect.Result{
			Outcome: githubconnect.OutcomeAuthenticated,
			Status:  &githubconnect.Status{Account: githubconnect.Account{Login: login, Active: true, State: githubconnect.StateSuccess}},
		}
	}
}

func statusSeq(fn func(call int) githubconnect.Result) func(context.Context) githubconnect.Result {
	var mu sync.Mutex
	call := 0
	return func(context.Context) githubconnect.Result {
		mu.Lock()
		defer mu.Unlock()
		call++
		return fn(call)
	}
}

// pageRunner serves canned list pages by page number.
func pageRunner(headers map[int]map[string]string, bodies map[int]string, errs map[int]error) func(context.Context, []string) ([]byte, error) {
	return func(_ context.Context, argv []string) ([]byte, error) {
		arg := argv[len(argv)-1]
		var page int
		fmt.Sscanf(arg, "/user/repos?affiliation=owner,collaborator,organization_member&sort=full_name&per_page=100&page=%d", &page)
		if page == 0 {
			return nil, fmt.Errorf("unexpected argv: %v", argv)
		}
		if errs != nil {
			if err, ok := errs[page]; ok {
				return nil, err
			}
		}
		return ghResponse(200, "OK", headers[page], bodies[page]), nil
	}
}

func newTestService(status func(context.Context) githubconnect.Result, run func(context.Context, []string) ([]byte, error)) *Service {
	return newService(status, run)
}

func TestListPaginatesAcrossPagesUntilNoNext(t *testing.T) {
	headers := map[int]map[string]string{
		1: {"Link": linkNext(2)},
		2: {"Link": linkNext(3)},
		3: {},
	}
	bodies := map[int]string{
		1: "[" + repoJSON(1, "alice", "alpha", "User", "http://evil.example/a", true) + "]",
		2: "[" + repoJSON(2, "acme", "beta", "Organization", "git@github.com:acme/beta.git", false) + "]",
		3: "[]",
	}
	var argvs [][]string
	run := func(ctx context.Context, argv []string) ([]byte, error) {
		argvs = append(argvs, argv)
		return pageRunner(headers, bodies, nil)(ctx, argv)
	}
	s := newTestService(authStatus("alice"), run)

	res := s.List(context.Background(), "alice", false)
	if res.Outcome != outcomeOK || !res.Complete {
		t.Fatalf("outcome=%s complete=%v", res.Outcome, res.Complete)
	}
	if len(res.Repositories) != 2 {
		t.Fatalf("repos=%d", len(res.Repositories))
	}
	if got := res.Repositories[0].CloneURL; got != "https://github.com/alice/alpha.git" {
		t.Fatalf("clone url %q is not the canonical rebuilt URL", got)
	}
	if res.Repositories[1].Owner != "acme" || res.Repositories[1].OwnerType != "Organization" {
		t.Fatalf("later page repo missing: %+v", res.Repositories[1])
	}
	if len(argvs) != 3 {
		t.Fatalf("expected 3 page fetches, got %d", len(argvs))
	}
	for i, argv := range argvs {
		joined := strings.Join(argv, " ")
		if !strings.Contains(joined, fmt.Sprintf("page=%d", i+1)) {
			t.Fatalf("argv %d did not increment the page: %s", i+1, joined)
		}
		if strings.Contains(joined, "token") {
			t.Fatalf("credential-like value in argv: %s", joined)
		}
	}
}

func TestListPartialSSOAcrossPagesAndCache(t *testing.T) {
	for _, partialPage := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("page%d", partialPage), func(t *testing.T) {
			headers := map[int]map[string]string{
				1: {"Link": linkNext(2)},
				2: {"Link": linkNext(3)},
				3: {},
			}
			headers[partialPage]["X-GitHub-SSO"] = "partial-results; organizations=1,2"
			bodies := map[int]string{
				1: "[" + repoJSON(1, "alice", "alpha", "User", "", false) + "]",
				2: "[" + repoJSON(2, "example", "beta", "Organization", "", true) + "]",
				3: "[]",
			}
			calls := 0
			s := newTestService(authStatus("alice"), func(ctx context.Context, argv []string) ([]byte, error) {
				calls++
				return pageRunner(headers, bodies, nil)(ctx, argv)
			})
			for range 2 {
				result := s.List(context.Background(), "alice", false)
				if result.Outcome != outcomeOK || !result.Complete || !result.SSOPartial || len(result.Repositories) != 2 {
					t.Fatalf("partial SSO list %+v", result)
				}
				if result.Repositories[0].ID != 1 || result.Repositories[1].ID != 2 {
					t.Fatalf("repositories %+v", result.Repositories)
				}
			}
			if calls != 3 {
				t.Fatalf("expected three pages and a cache hit, got %d calls", calls)
			}
			delete(headers[partialPage], "X-GitHub-SSO")
			result := s.List(context.Background(), "alice", true)
			if result.Outcome != outcomeOK || result.SSOPartial || calls != 6 {
				t.Fatalf("refresh retained stale SSO state: %+v, calls=%d", result, calls)
			}
		})
	}
}

func TestListEmpty(t *testing.T) {
	s := newTestService(authStatus("alice"), pageRunner(nil, map[int]string{1: "[]"}, nil))
	res := s.List(context.Background(), "alice", false)
	if res.Outcome != outcomeOK || !res.Complete || len(res.Repositories) != 0 {
		t.Fatalf("res=%+v", res)
	}
	if res.Repositories == nil {
		t.Fatal("repositories must serialize as an empty array, not null")
	}
}

func TestListCacheAndRefreshBypass(t *testing.T) {
	calls := 0
	run := func(ctx context.Context, argv []string) ([]byte, error) {
		calls++
		return pageRunner(nil, map[int]string{1: "[" + repoJSON(1, "alice", "alpha", "User", "https://github.com/alice/alpha.git", false) + "]"}, nil)(ctx, argv)
	}
	s := newTestService(authStatus("alice"), run)

	if res := s.List(context.Background(), "alice", false); res.Outcome != outcomeOK {
		t.Fatal(res.Outcome)
	}
	if res := s.List(context.Background(), "alice", false); res.Outcome != outcomeOK {
		t.Fatal(res.Outcome)
	}
	if calls != 1 {
		t.Fatalf("cache miss refetched: calls=%d", calls)
	}
	if res := s.List(context.Background(), "alice", true); res.Outcome != outcomeOK {
		t.Fatal(res.Outcome)
	}
	if calls != 2 {
		t.Fatalf("refresh did not bypass cache: calls=%d", calls)
	}

	// An expired entry is not served.
	s.mu.Lock()
	e := s.cache["alice"]
	e.at = time.Now().Add(-2 * cacheTTL)
	s.cache["alice"] = e
	s.mu.Unlock()
	if res := s.List(context.Background(), "alice", false); res.Outcome != outcomeOK {
		t.Fatal(res.Outcome)
	}
	if calls != 3 {
		t.Fatalf("expired entry served: calls=%d", calls)
	}
}

func TestListCachedResultIsACopy(t *testing.T) {
	run := pageRunner(nil, map[int]string{1: "[" + repoJSON(1, "alice", "alpha", "User", "", false) + "]"}, nil)
	s := newTestService(authStatus("alice"), run)
	first := s.List(context.Background(), "alice", false)
	first.Repositories[0].Name = "mutated"
	second := s.List(context.Background(), "alice", false)
	if second.Repositories[0].Name != "alpha" {
		t.Fatal("caller mutation reached the cache")
	}
}

func TestListLaterFailureYieldsNoPartialAndNoCache(t *testing.T) {
	headers := map[int]map[string]string{1: {"Link": linkNext(2)}}
	bodies := map[int]string{1: "[" + repoJSON(1, "alice", "alpha", "User", "", false) + "]", 2: "[]"}
	failAtPage := 2
	var calls int
	run := func(ctx context.Context, argv []string) ([]byte, error) {
		calls++
		if failAtPage == 2 && strings.HasSuffix(argv[len(argv)-1], "&page=2") {
			// Nonzero exit: gh api still emitted the response on stdout.
			return ghResponse(403, "Forbidden", nil, "{}"), fmt.Errorf("gh exited 6")
		}
		return pageRunner(headers, bodies, nil)(ctx, argv)
	}
	s := newTestService(authStatus("alice"), run)

	res := s.List(context.Background(), "alice", false)
	if res.Outcome != outcomeForbidden || res.Complete || len(res.Repositories) != 0 {
		t.Fatalf("partial success leaked: %+v", res)
	}

	if calls != 2 {
		t.Fatalf("failure must occur after fetching page 1: calls=%d", calls)
	}

	// Page 2 succeeds now; the failed attempt must not have been cached.
	failAtPage = 0
	res = s.List(context.Background(), "alice", false)
	if res.Outcome != outcomeOK || !res.Complete || len(res.Repositories) != 1 {
		t.Fatalf("refetch after failure: %+v", res)
	}
	if calls != 4 {
		t.Fatalf("retry must fetch both pages again: calls=%d", calls)
	}
}

func TestListStructuredFailures(t *testing.T) {
	cases := []struct {
		name    string
		code    int
		headers map[string]string
		want    string
	}{
		{"sso", 403, map[string]string{"X-GitHub-SSO": "required"}, outcomeSSORequired},
		{"rate 429", 429, nil, outcomeRateLimited},
		{"rate 403 exhausted", 403, map[string]string{"X-RateLimit-Remaining": "0"}, outcomeRateLimited},
		{"forbidden unknown reason", 403, nil, outcomeForbidden},
		{"unauthorized", 401, nil, outcomeAuthRequired},
		{"server error", 502, nil, outcomeUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := func(context.Context, []string) ([]byte, error) {
				return ghResponse(tc.code, "", tc.headers, "{}"), nil
			}
			res := newTestService(authStatus("alice"), run).List(context.Background(), "alice", false)
			if res.Outcome != tc.want || res.Complete || len(res.Repositories) != 0 || res.Account != "alice" {
				t.Fatalf("got %+v want %s", res, tc.want)
			}
		})
	}
}

func TestListAccountChangedBeforeAndAfter(t *testing.T) {
	// Before: the active account is someone else.
	status := statusSeq(func(call int) githubconnect.Result {
		return authResult("bob")
	})
	var calls int
	run := func(context.Context, []string) ([]byte, error) { calls++; return nil, fmt.Errorf("must not run") }
	res := newTestService(status, run).List(context.Background(), "alice", false)
	if res.Outcome != outcomeAccountMoved || calls != 0 {
		t.Fatalf("res=%+v calls=%d", res, calls)
	}

	// After: the account moves between the list and the closing check.
	status = statusSeq(func(call int) githubconnect.Result {
		if call == 1 {
			return authResult("alice")
		}
		return authResult("bob")
	})
	run = pageRunner(nil, map[int]string{1: "[" + repoJSON(1, "alice", "alpha", "User", "", false) + "]"}, nil)
	s := newTestService(status, run)
	res = s.List(context.Background(), "alice", false)
	if res.Outcome != outcomeAccountMoved || res.Complete || len(res.Repositories) != 0 {
		t.Fatalf("res=%+v", res)
	}
	// The fetched data was discarded: no cache.
	if _, ok := s.cached("alice"); ok {
		t.Fatal("account-changed data was cached")
	}
}

func TestListAuthIssueOutcomes(t *testing.T) {
	cases := []struct {
		res  githubconnect.Result
		want string
	}{
		{githubconnect.Result{Outcome: githubconnect.OutcomeNoAccount}, outcomeAuthRequired},
		{githubconnect.Result{Outcome: githubconnect.OutcomeAuthIssue}, outcomeAuthRequired},
		{githubconnect.Result{Outcome: githubconnect.OutcomeTimeout}, outcomeCancelled},
		{githubconnect.Result{Outcome: githubconnect.OutcomeMissingCLI}, outcomeUnavailable},
		{githubconnect.Result{Outcome: githubconnect.OutcomeMalformed}, outcomeUnavailable},
	}
	for _, tc := range cases {
		status := func(context.Context) githubconnect.Result { return tc.res }
		res := newTestService(status, nil).List(context.Background(), "alice", false)
		if res.Outcome != tc.want {
			t.Fatalf("got %s want %s", res.Outcome, tc.want)
		}
	}
}

func TestListBounds(t *testing.T) {
	t.Run("page bound", func(t *testing.T) {
		headers := map[int]map[string]string{}
		bodies := map[int]string{}
		for p := 1; p <= maxPages+2; p++ {
			headers[p] = map[string]string{"Link": linkNext(p + 1)}
			bodies[p] = "[]"
		}
		s := newTestService(authStatus("alice"), pageRunner(headers, bodies, nil))
		if res := s.List(context.Background(), "alice", false); res.Outcome != outcomeLimit {
			t.Fatalf("outcome=%s", res.Outcome)
		}
	})
	t.Run("row bound", func(t *testing.T) {
		headers := map[int]map[string]string{1: {"Link": linkNext(2)}}
		var rowList []string
		for i := 0; i <= maxRows; i++ {
			rowList = append(rowList, repoJSON(int64(i+1), "alice", fmt.Sprintf("r%d", i), "User", "", false))
		}
		bodies := map[int]string{1: "[" + strings.Join(rowList, ",") + "]"}
		s := newTestService(authStatus("alice"), pageRunner(headers, bodies, nil))
		if res := s.List(context.Background(), "alice", false); res.Outcome != outcomeLimit {
			t.Fatalf("outcome=%s", res.Outcome)
		}
	})
	t.Run("aggregate bound", func(t *testing.T) {
		pageBytes := 9 << 20 // 4 pages exceed 32MiB total
		headers := map[int]map[string]string{1: {"Link": linkNext(2)}, 2: {"Link": linkNext(3)}, 3: {"Link": linkNext(4)}}
		bodies := map[int]string{1: strings.Repeat(" ", pageBytes), 2: strings.Repeat(" ", pageBytes), 3: strings.Repeat(" ", pageBytes), 4: "[]"}
		s := newTestService(authStatus("alice"), pageRunner(headers, bodies, nil))
		if res := s.List(context.Background(), "alice", false); res.Outcome != outcomeLimit {
			t.Fatalf("outcome=%s", res.Outcome)
		}
	})
	t.Run("single command output bound", func(t *testing.T) {
		run := func(context.Context, []string) ([]byte, error) {
			return ghResponse(200, "OK", nil, strings.Repeat(" ", maxCmdOutput+1)), nil
		}
		s := newTestService(authStatus("alice"), run)
		if res := s.List(context.Background(), "alice", false); res.Outcome != outcomeLimit {
			t.Fatalf("outcome=%s", res.Outcome)
		}
	})
}

func TestListCancellation(t *testing.T) {
	run := func(ctx context.Context, argv []string) ([]byte, error) {
		return nil, context.Canceled
	}
	res := newTestService(authStatus("alice"), run).List(context.Background(), "alice", false)
	if res.Outcome != outcomeCancelled {
		t.Fatalf("outcome=%s", res.Outcome)
	}
}

func TestListSecretsNeverSurface(t *testing.T) {
	run := func(context.Context, []string) ([]byte, error) {
		return nil, fmt.Errorf("gh: ghp_secrettoken123 in /home/alice/.config/gh/hosts.yml")
	}
	res := newTestService(authStatus("alice"), run).List(context.Background(), "alice", false)
	if res.Outcome != outcomeUnavailable {
		t.Fatalf("outcome=%s", res.Outcome)
	}
	b, _ := jsonMarshal(res)
	if strings.Contains(string(b), "ghp_secrettoken123") || strings.Contains(string(b), "hosts.yml") {
		t.Fatalf("secret material in result JSON: %s", b)
	}
}

func TestListMalformedBodies(t *testing.T) {
	for _, body := range []string{"", "not json", `{"not":"an array"}`, `[{"id":1}]`} {
		s := newTestService(authStatus("alice"), pageRunner(nil, map[int]string{1: body}, nil))
		if res := s.List(context.Background(), "alice", false); res.Outcome != outcomeMalformed {
			t.Fatalf("body %q: outcome=%s", body, res.Outcome)
		}
	}
}

func TestListInvalidRepoFields(t *testing.T) {
	bad := []string{
		`[{"id":0,"name":"a","full_name":"o/a","owner":{"login":"o","type":"User"}}]`,           // nonpositive id
		`[{"id":-5,"name":"a","full_name":"o/a","owner":{"login":"o","type":"User"}}]`,          // negative id
		`[{"id":1,"name":"a/b","full_name":"o/a/b","owner":{"login":"o","type":"User"}}]`,       // slash in name
		`[{"id":1,"name":"a","full_name":"wrong/a","owner":{"login":"o","type":"User"}}]`,       // full_name incoherent
		`[{"id":1,"name":"a","full_name":"o/a","owner":{"login":"","type":"User"}}]`,            // empty owner
		`[{"id":1,"name":"a b","full_name":"o/a b","owner":{"login":"o","type":"User"}}]`,       // space in name
		`[{"id":1,"name":"a","full_name":"o/a","owner":{"login":"o..x","type":"User"}}]`,        // bad owner charset
		`[{"id":1,"name":"../etc","full_name":"o/../etc","owner":{"login":"o","type":"User"}}]`, // traversal name
	}
	for _, body := range bad {
		s := newTestService(authStatus("alice"), pageRunner(nil, map[int]string{1: body}, nil))
		if res := s.List(context.Background(), "alice", false); res.Outcome != outcomeMalformed {
			t.Fatalf("body %s: outcome=%s", body, res.Outcome)
		}
	}
}

func TestRepositoryRenamedStableID(t *testing.T) {
	body := repoJSON(7, "alice", "renamed-later", "User", "javascript:alert(1)", false)
	run := func(context.Context, []string) ([]byte, error) {
		return ghResponse(200, "OK", nil, body), nil
	}
	res := newTestService(authStatus("alice"), run).Repository(context.Background(), "alice", 7)
	if res.Outcome != outcomeOK || res.Repository == nil {
		t.Fatalf("res=%+v", res)
	}
	if res.Repository.FullName != "alice/renamed-later" || res.Repository.CloneURL != "https://github.com/alice/renamed-later.git" {
		t.Fatalf("repo=%+v", res.Repository)
	}
}

func TestRepositoryIdMismatchAndFailures(t *testing.T) {
	// Body id differs from the requested id: malformed, never a silent swap.
	body := repoJSON(8, "alice", "a", "User", "", false)
	run := func(context.Context, []string) ([]byte, error) {
		return ghResponse(200, "OK", nil, body), nil
	}
	if res := newTestService(authStatus("alice"), run).Repository(context.Background(), "alice", 7); res.Outcome != outcomeMalformed {
		t.Fatalf("outcome=%s", res.Outcome)
	}

	notFound := func(context.Context, []string) ([]byte, error) {
		return ghResponse(404, "Not Found", nil, "{}"), nil
	}
	if res := newTestService(authStatus("alice"), notFound).Repository(context.Background(), "alice", 7); res.Outcome != outcomeNotFound {
		t.Fatalf("outcome=%s", res.Outcome)
	}

	// Account moves after the fetch: data discarded.
	status := statusSeq(func(call int) githubconnect.Result {
		if call == 1 {
			return authResult("alice")
		}
		return authResult("bob")
	})
	okRun := func(context.Context, []string) ([]byte, error) {
		return ghResponse(200, "OK", nil, repoJSON(7, "alice", "a", "User", "", false)), nil
	}
	if res := newTestService(status, okRun).Repository(context.Background(), "alice", 7); res.Outcome != outcomeAccountMoved || res.Repository != nil {
		t.Fatalf("res=%+v", res)
	}
}

func TestCacheEvictsPastKeyBound(t *testing.T) {
	s := newTestService(authStatus("alice"), nil)
	for i := 0; i < maxCacheKeys+1; i++ {
		s.store(fmt.Sprintf("user%d", i), []Repository{{ID: int64(i)}}, false)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cache) != maxCacheKeys {
		t.Fatalf("cache keys=%d", len(s.cache))
	}
	if _, ok := s.cache["user0"]; ok {
		t.Fatal("oldest key not evicted")
	}
	if _, ok := s.cache[fmt.Sprintf("user%d", maxCacheKeys)]; !ok {
		t.Fatal("newest key missing")
	}
}
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func TestListRejectsTruncatedHTTPBody(t *testing.T) {
	s := newTestService(authStatus("alice"), func(context.Context, []string) ([]byte, error) {
		return ghResponse(200, "OK", map[string]string{"Content-Length": "200"}, "[]"), nil
	})
	result := s.List(context.Background(), "alice", false)
	if result.Outcome != outcomeMalformed || result.Complete {
		t.Fatalf("truncated body reported as complete: %+v", result)
	}
}

func TestCachedListStillChecksActiveAccount(t *testing.T) {
	s := newTestService(authStatus("alice"), nil)
	s.store("alice", []Repository{{ID: 1, Name: "alpha"}}, false)
	s.status = authStatus("bob")
	result := s.List(context.Background(), "alice", false)
	if result.Outcome != outcomeAccountMoved || result.Complete || len(result.Repositories) != 0 {
		t.Fatalf("cached list escaped account check: %+v", result)
	}
}
