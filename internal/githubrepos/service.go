// Package githubrepos lists GitHub repositories through the native gh CLI.
// Deck never interprets provider protocol beyond the documented HTTP
// semantics gh api already surfaces: status codes, response headers, and
// JSON bodies. Credential values never appear in argv and stderr is never
// captured.
package githubrepos

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/noisefloor-oss/deck/internal/githubconnect"
)

// Outcome strings for the structured JSON contract.
const (
	outcomeOK           = "ok"
	outcomeAuthRequired = "authentication-required"
	outcomeAccountMoved = "account-changed"
	outcomeRateLimited  = "rate-limited"
	outcomeSSORequired  = "sso-required"
	outcomeForbidden    = "forbidden"
	outcomeUnavailable  = "unavailable"
	outcomeNotFound     = "not-found"
	outcomeLimit        = "limit"
	outcomeCancelled    = "cancelled"
	outcomeMalformed    = "malformed"
)

// Bounds for the whole list operation.
const (
	// opTimeout bounds one List or Repository call, status checks included.
	opTimeout = 60 * time.Second
	// maxPages bounds manual pagination even if Link keeps offering next.
	maxPages = 100
	// maxRows bounds the aggregated result set.
	maxRows = 10000
	// maxAggregateBytes bounds summed gh api stdout across the operation.
	maxAggregateBytes = 32 << 20
	// maxCmdOutput bounds a single command's captured stdout.
	maxCmdOutput = 8 << 20
	// cacheTTL is how long a completed list stays cacheable.
	cacheTTL = 60 * time.Second
	// maxCacheKeys bounds the per-account cache.
	maxCacheKeys = 4
)

// runFunc is the command boundary; production uses execRun.
type runFunc func(ctx context.Context, argv []string) (stdout []byte, err error)

// statusFunc reads the active native account; production uses
// githubconnect.CheckStatus.
type statusFunc func(ctx context.Context) githubconnect.Result

// Repository is the allowlisted projection of a GitHub repository. The
// clone URL is rebuilt canonically from the validated owner and name; the
// server-supplied clone URL is never trusted or carried through.
type Repository struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Owner       string `json:"owner"`
	OwnerType   string `json:"owner_type"`
	Private     bool   `json:"private"`
	Description string `json:"description"`
	CloneURL    string `json:"clone_url"`
}

// ListResult is the JSON contract for the list endpoint. Failures carry no
// repositories and never claim completeness.
type ListResult struct {
	Outcome      string       `json:"outcome"`
	Account      string       `json:"account"`
	Repositories []Repository `json:"repositories"`
	Complete     bool         `json:"complete"`
	SSOPartial   bool         `json:"ssoPartial"`
}

// RepositoryResult is the JSON contract for the single-repository endpoint.
type RepositoryResult struct {
	Outcome    string      `json:"outcome"`
	Account    string      `json:"account"`
	Repository *Repository `json:"repository,omitempty"`
}

// AccountResult is the JSON contract for the account endpoint: the active
// account's profile name and its GitHub noreply commit address. Failures
// carry neither.
type AccountResult struct {
	Outcome string `json:"outcome"`
	Account string `json:"account"`
	Name    string `json:"name,omitempty"`
	Email   string `json:"email,omitempty"`
}

type cacheEntry struct {
	repos      []Repository
	ssoPartial bool
	at         time.Time
}

// Service lists repositories through native gh and caches completed lists
// per account. The mutex guards only the cache map, never network calls.
type Service struct {
	status statusFunc
	run    runFunc

	mu    sync.Mutex
	cache map[string]cacheEntry
	order []string
}

// NewService builds the production service over the native gh CLI.
func NewService() *Service {
	return newService(githubconnect.CheckStatus, execRun)
}

func newService(status statusFunc, run runFunc) *Service {
	return &Service{status: status, run: run, cache: map[string]cacheEntry{}}
}

// errGHNotFound reports a missing gh executable without wrapping stderr.
var errGHNotFound = errors.New("gh executable not found")

// errGHOversized reports command output past the capture bound.
var errGHOversized = errors.New("gh api output exceeded capture bound")

// listPageArgv is fixed except for the page number. No credential value is
// ever requested or passed in argv; gh inherits the environment and applies
// its own credential precedence.
func listPageArgv(page int) []string {
	return []string{
		"gh", "api", "--hostname", githubconnect.Host, "--method", "GET", "--include",
		fmt.Sprintf("/user/repos?affiliation=owner,collaborator,organization_member&sort=full_name&per_page=100&page=%d", page),
	}
}

func repositoryArgv(id int64) []string {
	return []string{"gh", "api", "--hostname", githubconnect.Host, "--method", "GET", "--include", fmt.Sprintf("/repositories/%d", id)}
}

var userArgv = []string{"gh", "api", "--hostname", githubconnect.Host, "--method", "GET", "--include", "/user"}

// checkAccount verifies the active native account matches the expected
// login. Expected-account mismatches and unusable auth states are structured
// outcomes, never run errors.
func checkAccount(res githubconnect.Result, expected string) string {
	switch res.Outcome {
	case githubconnect.OutcomeAuthenticated:
		if res.Status == nil || res.Status.Account.Login != expected {
			return outcomeAccountMoved
		}
		return outcomeOK
	case githubconnect.OutcomeNoAccount, githubconnect.OutcomeAuthIssue:
		return outcomeAuthRequired
	case githubconnect.OutcomeTimeout:
		return outcomeCancelled
	}
	return outcomeUnavailable
}

func listFailure(outcome, account string) ListResult {
	return ListResult{Outcome: outcome, Account: account, Repositories: []Repository{}, Complete: false}
}

// List lists the active account's repositories, paginating manually until
// Link stops offering rel="next". It never follows the Link URL: the next
// page is the same fixed path with an incremented, bounded page number.
// The active native account is checked before and after; a change discards
// the fetched data.
func (s *Service) List(ctx context.Context, expectedAccount string, refresh bool) ListResult {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	if out := checkAccount(s.status(ctx), expectedAccount); out != outcomeOK {
		return listFailure(out, expectedAccount)
	}

	if !refresh {
		if entry, ok := s.cached(expectedAccount); ok {
			return ListResult{Outcome: outcomeOK, Account: expectedAccount, Repositories: entry.repos, Complete: true, SSOPartial: entry.ssoPartial}
		}
	}

	var repos []Repository
	ssoPartial := false
	aggregate := 0
	for page := 1; page <= maxPages; page++ {
		stdout, err := s.run(ctx, listPageArgv(page))
		if err != nil {
			// gh exited nonzero but may still have emitted the response on
			// stdout; its documented status and headers classify the failure.
			return listFailure(runErrorOutcome(ctx, err, stdout), expectedAccount)
		}
		aggregate += len(stdout)
		if aggregate > maxAggregateBytes {
			return listFailure(outcomeLimit, expectedAccount)
		}
		resp, out := decodeIncluded(stdout)
		if out != outcomeOK {
			return listFailure(out, expectedAccount)
		}
		sso, _, _ := strings.Cut(resp.Header.Get("X-GitHub-SSO"), ";")
		ssoPartial = ssoPartial || strings.EqualFold(strings.TrimSpace(sso), "partial-results")
		var pageRepos []Repository
		pageRepos, out = decodeRepoArray(resp.body)
		if out != outcomeOK {
			return listFailure(out, expectedAccount)
		}
		if len(repos)+len(pageRepos) > maxRows {
			return listFailure(outcomeLimit, expectedAccount)
		}
		repos = append(repos, pageRepos...)
		if !hasNextPage(resp.Header) {
			// A later failure would have returned above; only reaching the
			// last page makes the set complete.
			return s.finishList(ctx, expectedAccount, repos, ssoPartial)
		}
	}
	// Link kept offering next past the page bound: refuse to claim complete.
	return listFailure(outcomeLimit, expectedAccount)
}

// finishList rechecks the account, caches the completed set, and returns a
// copied result.
func (s *Service) finishList(ctx context.Context, account string, repos []Repository, ssoPartial bool) ListResult {
	if out := checkAccount(s.status(ctx), account); out != outcomeOK {
		// The account moved under us: discard the fetched data.
		return listFailure(out, account)
	}
	s.store(account, repos, ssoPartial)
	out := make([]Repository, len(repos))
	copy(out, repos)
	return ListResult{Outcome: outcomeOK, Account: account, Repositories: out, Complete: true, SSOPartial: ssoPartial}
}

// Repository fetches one repository by stable id, allowing a rename: the
// id is the identity, the full name is whatever the account renamed it to.
// A body id that does not match the requested id is malformed.
func (s *Service) Repository(ctx context.Context, expectedAccount string, id int64) RepositoryResult {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	if out := checkAccount(s.status(ctx), expectedAccount); out != outcomeOK {
		return RepositoryResult{Outcome: out, Account: expectedAccount}
	}

	stdout, err := s.run(ctx, repositoryArgv(id))
	if err != nil {
		return RepositoryResult{Outcome: runErrorOutcome(ctx, err, stdout), Account: expectedAccount}
	}
	resp, out := decodeIncluded(stdout)
	if out != outcomeOK {
		return RepositoryResult{Outcome: out, Account: expectedAccount}
	}
	var repo Repository
	repo, out = decodeRepoObject(resp.body)
	if out != outcomeOK {
		return RepositoryResult{Outcome: out, Account: expectedAccount}
	}
	if repo.ID != id {
		return RepositoryResult{Outcome: outcomeMalformed, Account: expectedAccount}
	}
	if out := checkAccount(s.status(ctx), expectedAccount); out != outcomeOK {
		return RepositoryResult{Outcome: out, Account: expectedAccount}
	}
	return RepositoryResult{Outcome: outcomeOK, Account: expectedAccount, Repository: &repo}
}

// Account reads the active account's public profile. The commit address is
// the account's noreply form, never its private email; a blank or unusable
// profile name falls back to the login.
func (s *Service) Account(ctx context.Context) AccountResult {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	first := s.status(ctx)
	login := ""
	if first.Outcome == githubconnect.OutcomeAuthenticated && first.Status != nil {
		login = first.Status.Account.Login
	}
	if out := checkAccount(first, login); out != outcomeOK || login == "" {
		if out == outcomeOK {
			out = outcomeAuthRequired
		}
		return AccountResult{Outcome: out}
	}

	stdout, err := s.run(ctx, userArgv)
	if err != nil {
		return AccountResult{Outcome: runErrorOutcome(ctx, err, stdout), Account: login}
	}
	resp, out := decodeIncluded(stdout)
	if out != outcomeOK {
		return AccountResult{Outcome: out, Account: login}
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if json.Unmarshal(resp.body, &user) != nil || user.ID <= 0 || !validOwner(user.Login) {
		return AccountResult{Outcome: outcomeMalformed, Account: login}
	}
	if !strings.EqualFold(user.Login, login) {
		return AccountResult{Outcome: outcomeAccountMoved, Account: login}
	}
	if out := checkAccount(s.status(ctx), login); out != outcomeOK {
		return AccountResult{Outcome: out, Account: login}
	}
	name := strings.TrimSpace(user.Name)
	if name == "" || len(name) > 256 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		name = user.Login
	}
	return AccountResult{
		Outcome: outcomeOK,
		Account: login,
		Name:    name,
		Email:   fmt.Sprintf("%d+%s@users.noreply.github.com", user.ID, user.Login),
	}
}

// cached returns a copied, fresh entry under the cache mutex.
func (s *Service) cached(account string) (cacheEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[account]
	if !ok || time.Since(e.at) > cacheTTL {
		return cacheEntry{}, false
	}
	out := make([]Repository, len(e.repos))
	copy(out, e.repos)
	e.repos = out
	return e, true
}

// store inserts a completed list, evicting past the key bound.
func (s *Service) store(account string, repos []Repository, ssoPartial bool) {
	cp := make([]Repository, len(repos))
	copy(cp, repos)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.cache[account]; !exists {
		s.order = append(s.order, account)
	}
	s.cache[account] = cacheEntry{repos: cp, ssoPartial: ssoPartial, at: time.Now()}
	for len(s.order) > maxCacheKeys {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.cache, oldest)
	}
}

// runErrorOutcome classifies a nonzero-exit run. gh api may still have
// emitted the response status line and headers on stdout; those classify
// 401/403/404/429 exactly like a clean run. Anything else falls back to
// the run-error mapping; stderr never reaches this path.
func runErrorOutcome(ctx context.Context, err error, stdout []byte) string {
	if len(stdout) > maxCmdOutput {
		return outcomeLimit
	}
	if resp, out := decodeIncluded(stdout); resp != nil && out != outcomeOK {
		return out
	}
	return classifyRunError(ctx, err)
}

// classifyRunError maps a command failure onto the structured outcomes.
// stderr never reaches this map: the runner captures stdout only.
func classifyRunError(ctx context.Context, err error) string {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return outcomeCancelled
	}
	if errors.Is(err, errGHOversized) {
		return outcomeLimit
	}
	return outcomeUnavailable
}

type includedResponse struct {
	Header http.Header
	body   []byte
}

// decodeIncluded parses gh api --include stdout: status line, headers, then
// body. On nonzero exit gh may still have emitted the response headers on
// stdout; those are classified the same way.
func decodeIncluded(stdout []byte) (*includedResponse, string) {
	if len(stdout) > maxCmdOutput {
		return nil, outcomeLimit
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(stdout)), nil)
	if err != nil || resp == nil {
		return nil, outcomeMalformed
	}
	defer func() { _ = resp.Body.Close() }()
	decoded := &includedResponse{Header: resp.Header}
	if out := classifyStatus(resp.StatusCode, resp.Header); out != outcomeOK {
		return decoded, out
	}
	decoded.body, err = io.ReadAll(resp.Body)
	if err != nil {
		return decoded, outcomeMalformed
	}
	return decoded, outcomeOK
}

// classifyStatus maps documented GitHub HTTP semantics onto outcomes.
func classifyStatus(code int, h http.Header) string {
	switch code {
	case http.StatusOK:
		return outcomeOK
	case http.StatusUnauthorized:
		return outcomeAuthRequired
	case http.StatusNotFound:
		return outcomeNotFound
	case http.StatusTooManyRequests:
		return outcomeRateLimited
	case http.StatusForbidden:
		if h.Get("X-GitHub-SSO") != "" {
			return outcomeSSORequired
		}
		if h.Get("X-RateLimit-Remaining") == "0" {
			return outcomeRateLimited
		}
		return outcomeForbidden
	default:
		return outcomeUnavailable
	}
}

// hasNextPage reports whether the Link header offers rel="next". The URL
// itself is never read or followed.
func hasNextPage(h http.Header) bool {
	link := h.Get("Link")
	if link == "" {
		return false
	}
	for _, seg := range splitLink(link) {
		if bytes.Contains(seg, []byte(`rel="next"`)) || bytes.Contains(seg, []byte("rel=next")) {
			return true
		}
	}
	return false
}

// splitLink splits a comma-separated Link header on commas outside <>.
func splitLink(link string) [][]byte {
	var segs [][]byte
	depth := 0
	start := 0
	for i := 0; i < len(link); i++ {
		switch link[i] {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				segs = append(segs, []byte(link[start:i]))
				start = i + 1
			}
		}
	}
	segs = append(segs, []byte(link[start:]))
	return segs
}

// decodedRepo mirrors only the allowlisted fields. clone_url from the
// server is deliberately absent: the canonical URL is rebuilt from the
// validated owner and name.
type decodedRepo struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Private     bool   `json:"private"`
	Description string `json:"description"`
	Owner       struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`
}

// decodeRepoArray decodes a page body.
func decodeRepoArray(body []byte) ([]Repository, string) {
	var raw []decodedRepo
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return nil, outcomeMalformed
	}
	repos := make([]Repository, 0, len(raw))
	for _, r := range raw {
		repo, out := projectRepo(r)
		if out != outcomeOK {
			return nil, out
		}
		repos = append(repos, repo)
	}
	return repos, outcomeOK
}

// decodeRepoObject decodes a single-repository body.
func decodeRepoObject(body []byte) (Repository, string) {
	var raw decodedRepo
	if err := json.Unmarshal(body, &raw); err != nil {
		return Repository{}, outcomeMalformed
	}
	return projectRepo(raw)
}

// projectRepo validates the decoded fields and builds the canonical
// HTTPS clone URL. It never trusts a server-supplied URL.
func projectRepo(r decodedRepo) (Repository, string) {
	if r.ID <= 0 || r.ID > 9007199254740991 || !validOwner(r.Owner.Login) || !validName(r.Name) || r.FullName != r.Owner.Login+"/"+r.Name {
		return Repository{}, outcomeMalformed
	}
	return Repository{
		ID:          r.ID,
		Name:        r.Name,
		FullName:    r.FullName,
		Owner:       r.Owner.Login,
		OwnerType:   r.Owner.Type,
		Private:     r.Private,
		Description: r.Description,
		CloneURL:    "https://github.com/" + r.Owner.Login + "/" + r.Name + ".git",
	}, outcomeOK
}

// validOwner accepts GitHub user and organization logins: alphanumerics and
// single hyphens, not ending in a hyphen.
func validOwner(s string) bool {
	if len(s) == 0 || len(s) > 39 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// validName accepts GitHub repository names: alphanumerics, hyphens,
// underscores, and dots. Dot-prefixed repositories such as .github are valid.
func validName(s string) bool {
	if len(s) == 0 || len(s) > 100 || s == "." || s == ".." {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

// execRun captures stdout only. stderr is discarded: gh diagnostics there
// can echo credential paths or token hints, and nothing here needs them.
// cmd.Env is left nil so the child inherits os.Environ and gh's own
// credential precedence applies natively.
func execRun(ctx context.Context, argv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var buf bytes.Buffer
	lw := &limitedWriter{w: &buf, n: maxCmdOutput}
	cmd.Stdout = lw
	cmd.Stderr = nil
	// A grandchild inheriting the stdout pipe would otherwise hold Run open
	// past the kill; abandon the pipe copy once the context is done.
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", errGHNotFound, argv[0])
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if lw.trunc {
			return nil, errGHOversized
		}
		return buf.Bytes(), fmt.Errorf("gh api failed: %w", err)
	}
	if lw.trunc {
		return nil, errGHOversized
	}
	return buf.Bytes(), nil
}

// limitedWriter caps total bytes; writes past the bound stop being recorded
// (the child keeps writing into the discarded remainder, then we fail).
type limitedWriter struct {
	w     *bytes.Buffer
	n     int
	trunc bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		if len(p) > room {
			l.w.Write(p[:room])
			l.trunc = true
		} else {
			l.w.Write(p)
		}
	} else if len(p) > 0 {
		l.trunc = true
	}
	return len(p), nil
}
