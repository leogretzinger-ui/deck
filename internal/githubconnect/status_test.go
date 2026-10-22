package githubconnect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner exercises the decoder boundary without invoking real gh.
type fakeRunner struct {
	stdout []byte
	err    error
	argv   []string
	calls  int
}

func (f *fakeRunner) run(ctx context.Context, argv []string) ([]byte, error) {
	f.calls++
	f.argv = argv
	return f.stdout, f.err
}

func check(t *testing.T, stdout []byte, err error) Result {
	t.Helper()
	return checkStatus(context.Background(), (&fakeRunner{stdout: stdout, err: err}).run)
}

func TestArgvIsFixedAndNeverRequestsTokens(t *testing.T) {
	f := &fakeRunner{stdout: []byte(`{"hosts":{}}`)}
	checkStatus(context.Background(), f.run)
	want := []string{"gh", "auth", "status", "--active", "--hostname", "github.com", "--json", "hosts"}
	if len(f.argv) != len(want) {
		t.Fatalf("argv = %v, want %v", f.argv, want)
	}
	for i := range want {
		if f.argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, f.argv[i], want[i])
		}
	}
	for _, a := range f.argv {
		if strings.Contains(strings.ToLower(a), "token") {
			t.Fatalf("argv requests token material: %v", f.argv)
		}
	}
}

// Mirrors gh 2.102.0's exported shape: buildEntry rewrites the internal
// oauth_token source into the hosts.yml path; entry order is active first.
// The private path below is synthetic.
const storedFixture = `{
  "hosts": {
    "github.com": [
      {
        "active": true,
        "gitProtocol": "https",
        "host": "github.com",
        "login": "alice",
        "state": "success",
        "tokenSource": "/home/synthuser/.config/gh/hosts.yml"
      }
    ]
  }
}`

func entryJSON(fields ...string) string {
	return `{"hosts":{"github.com":[{` + strings.Join(fields, ",") + `}]}}`
}

func field(k, v string) string { return `"` + k + `": "` + v + `"` }
func boolField(k string, v bool) string {
	if v {
		return `"` + k + `": true`
	}
	return `"` + k + `": false`
}

func TestRealSuccessStoredFixture(t *testing.T) {
	f := &fakeRunner{stdout: []byte(storedFixture)}
	res := checkStatus(context.Background(), f.run)
	if res.Outcome != OutcomeAuthenticated {
		t.Fatalf("outcome = %v, want authenticated", res.Outcome)
	}
	a := res.Status.Account
	if a.Login != "alice" || a.GitProtocol != "https" || !a.Active ||
		a.State != StateSuccess || a.Source != SourceStored || !a.SourceWriteable {
		t.Fatalf("account = %+v", a)
	}
}

func TestPrivateTokenPathNeverSurfaces(t *testing.T) {
	syntheticPath := "/home/synthuser/.config/gh/hosts.yml"
	fixtures := []string{storedFixture,
		entryJSON(boolField("active", true), field("host", "github.com"),
			field("login", "alice"), field("state", "success"),
			field("tokenSource", "keyring"),
			field("error", "boom at "+syntheticPath))}
	for _, fix := range fixtures {
		f := &fakeRunner{stdout: []byte(fix)}
		res := checkStatus(context.Background(), f.run)
		for _, s := range resultStrings(res) {
			if strings.Contains(s, syntheticPath) || strings.Contains(s, "hosts.yml") {
				t.Fatalf("private path surfaced in %q", s)
			}
		}
	}
}

func TestEnvironmentSourceClassification(t *testing.T) {
	for _, env := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		fix := entryJSON(boolField("active", true), field("host", "github.com"),
			field("login", "alice"), field("state", "success"), field("tokenSource", env))
		res := check(t, []byte(fix), nil)
		if res.Outcome != OutcomeAuthenticated {
			t.Fatalf("%s: outcome = %v", env, res.Outcome)
		}
		a := res.Status.Account
		if a.Source != SourceEnvironment || a.SourceWriteable {
			t.Fatalf("%s: account = %+v", env, a)
		}
	}
}

func TestUnknownSourcePreservesAuthenticationButRefusesReplacement(t *testing.T) {
	fix := entryJSON(boolField("active", true), field("host", "github.com"),
		field("login", "alice"), field("state", "success"), field("tokenSource", "mystery"))
	res := check(t, []byte(fix), nil)
	if res.Outcome != OutcomeAuthenticated {
		t.Fatalf("outcome = %v, want authenticated with unknown source", res.Outcome)
	}
	if res.Status == nil || res.Status.Account.Source != SourceUnknown {
		t.Fatalf("status = %+v, want unknown source classification", res.Status)
	}
	if res.Status.Account.SourceWriteable {
		t.Fatal("unknown credential source must not allow replacement")
	}
	if res.Status.Account.Login != "alice" {
		t.Fatalf("login lost on unknown source: %+v", res.Status.Account)
	}
}

// Exit 0 with auth problems is gh's documented --json behavior; only the
// state fields decide.
func TestExitZeroNonSuccessStates(t *testing.T) {
	cases := []struct {
		name string
		fix  string
	}{
		{"timeout", entryJSON(boolField("active", true), field("host", "github.com"),
			field("login", "alice"), field("state", "timeout"),
			field("tokenSource", "keyring"), field("error", "net timeout"))},
		{"error", entryJSON(boolField("active", true), field("host", "github.com"),
			field("login", "alice"), field("state", "error"),
			field("tokenSource", "keyring"), field("error", "bad credentials"))},
		{"legacy-active", entryJSON(boolField("active", true), field("host", "github.com"),
			field("login", "alice"), field("state", "active"), field("tokenSource", "keyring"))},
		{"legacy-loggedin", entryJSON(boolField("active", true), field("host", "github.com"),
			field("login", "alice"), field("state", "loggedin"), field("tokenSource", "keyring"))},
		{"empty-state", entryJSON(boolField("active", true), field("host", "github.com"),
			field("login", "alice"), field("state", ""), field("tokenSource", "keyring"))},
		{"missing-login", entryJSON(boolField("active", true), field("host", "github.com"),
			field("state", "success"), field("tokenSource", "keyring"))},
		{"inactive-only", entryJSON(boolField("active", false), field("host", "github.com"),
			field("login", "alice"), field("state", "success"), field("tokenSource", "keyring"))},
		{"missing-active", entryJSON(field("host", "github.com"),
			field("login", "alice"), field("state", "success"), field("tokenSource", "keyring"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := check(t, []byte(tc.fix), nil)
			if res.Outcome != OutcomeAuthIssue {
				t.Fatalf("outcome = %v, want auth-issue", res.Outcome)
			}
			if res.Status == nil {
				t.Fatalf("auth issue must carry classified status")
			}
		})
	}
}

func TestEnvAccountAuthErrorKeepsSourceClassification(t *testing.T) {
	// gh reports environment-sourced tokens as error when the API rejects
	// them; Deck must still know the auth is environment-owned so the UI
	// explains instead of offering to overwrite login.
	fix := entryJSON(boolField("active", true), field("host", "github.com"),
		field("state", "error"), field("tokenSource", "GH_TOKEN"),
		field("error", "HTTP 401"))
	res := check(t, []byte(fix), nil)
	if res.Outcome != OutcomeAuthIssue {
		t.Fatalf("outcome = %v, want auth-issue", res.Outcome)
	}
	a := res.Status.Account
	if a.Source != SourceEnvironment || a.SourceWriteable {
		t.Fatalf("account = %+v, want environment-owned", a)
	}
}

func TestEmptyHostsIsNoAccountNotMalformed(t *testing.T) {
	for _, fix := range []string{`{"hosts":{}}`, `{"hosts":{"github.com":[]}}`,
		// A healthy account on a different host means no account for ours.
		`{"hosts":{"ghe.example.com":[{"active":true,"host":"ghe.example.com","login":"alice","state":"success","tokenSource":"keyring"}]}}`} {
		res := check(t, []byte(fix), nil)
		if res.Outcome != OutcomeNoAccount {
			t.Fatalf("%s: outcome = %v, want no-account", fix, res.Outcome)
		}
	}
}

func TestActiveSelectionAndAmbiguity(t *testing.T) {
	// Inactive entry precedes the active one: selection follows the
	// marking, not position.
	fix := `{"hosts":{"github.com":[
		{"active":false,"host":"github.com","login":"bob","state":"success","tokenSource":"keyring"},
		{"active":true,"host":"github.com","login":"alice","state":"success","tokenSource":"keyring"}]}}`
	res := check(t, []byte(fix), nil)
	if res.Outcome != OutcomeAuthenticated || res.Status.Account.Login != "alice" {
		t.Fatalf("res = %+v, want authenticated alice", res)
	}

	// Two entries marked active is a gh-side defect: refuse, don't pick.
	ambig := `{"hosts":{"github.com":[
		{"active":true,"host":"github.com","login":"alice","state":"success","tokenSource":"keyring"},
		{"active":true,"host":"github.com","login":"bob","state":"success","tokenSource":"keyring"}]}}`
	if res := check(t, []byte(ambig), nil); res.Outcome != OutcomeAuthIssue {
		t.Fatalf("ambiguous active: outcome = %v, want auth-issue", res.Outcome)
	}
}

func TestUnknownSecretFieldsNeverSurface(t *testing.T) {
	fix := strings.Replace(storedFixture,
		`"tokenSource": "/home/synthuser/.config/gh/hosts.yml"`,
		`"tokenSource": "/home/synthuser/.config/gh/hosts.yml", "token": "ghp_SYNTHETICSECRET123", "password": "hunter2"`, 1)
	res := check(t, []byte(fix), nil)
	if res.Outcome != OutcomeAuthenticated {
		t.Fatalf("outcome = %v, want authenticated", res.Outcome)
	}
	for _, s := range resultStrings(res) {
		if strings.Contains(s, "ghp_SYNTHETICSECRET") || strings.Contains(s, "hunter2") {
			t.Fatalf("secret leaked into result: %s", s)
		}
	}
}

func TestMalformedOutput(t *testing.T) {
	for _, out := range []string{"", "not json", `{"hosts":`, `[]`, "null", `{}`} {
		if res := check(t, []byte(out), nil); res.Outcome != OutcomeMalformed {
			t.Fatalf("stdout %q: outcome = %v, want malformed", out, res.Outcome)
		}
	}
}

func resultStrings(res Result) []string {
	out := []string{res.Outcome.String()}
	if res.Status != nil {
		a := res.Status.Account
		out = append(out, a.Login, a.GitProtocol, a.State.String(),
			a.Source.String(), fmtBool(a.Active), fmtBool(a.SourceWriteable))
	}
	return out
}

func fmtBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestCancellation(t *testing.T) {
	f := &fakeRunner{err: context.Canceled}
	if res := checkStatus(context.Background(), f.run); res.Outcome != OutcomeTimeout {
		t.Fatalf("cancelled run: outcome = %v, want timeout", res.Outcome)
	}
	f = &fakeRunner{err: errors.New("gh auth status failed: signal: killed")}
	if res := checkStatus(context.Background(), f.run); res.Outcome != OutcomeRunError {
		t.Fatalf("killed run: outcome = %v, want run-error", res.Outcome)
	}
}

// The exec boundary itself: a fake gh executable written by the test.
func writeFakeGh(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "gh")
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return p
}

func TestExecBoundaryRunsWithInheritedEnv(t *testing.T) {
	writeFakeGh(t, `test "$GH_TOKEN" = gho_SYNTHETICENV || exit 9
printf '%s' '`+storedFixture+`'`)
	// Native credential precedence: an env token reaches the child.
	t.Setenv("GH_TOKEN", "gho_SYNTHETICENV")
	res := CheckStatus(context.Background())
	if res.Outcome != OutcomeAuthenticated {
		t.Fatalf("outcome = %v, want authenticated", res.Outcome)
	}
}

func TestExecBoundaryMissingCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if res := CheckStatus(context.Background()); res.Outcome != OutcomeMissingCLI {
		t.Fatalf("outcome = %v, want missing-cli", res.Outcome)
	}
}

func TestExecBoundaryOversized(t *testing.T) {
	writeFakeGh(t, `head -c 200000 /dev/zero | tr '\0' '{'`)
	if res := CheckStatus(context.Background()); res.Outcome != OutcomeOversized {
		t.Fatalf("outcome = %v, want oversized", res.Outcome)
	}
}

func TestExecBoundaryTimeout(t *testing.T) {
	writeFakeGh(t, "sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if res := CheckStatus(ctx); res.Outcome != OutcomeTimeout {
		t.Fatalf("outcome = %v, want timeout", res.Outcome)
	}
}

func TestExecBoundaryNonzeroExit(t *testing.T) {
	writeFakeGh(t, `echo "boom" >&2; exit 4`)
	if res := CheckStatus(context.Background()); res.Outcome != OutcomeRunError {
		t.Fatalf("outcome = %v, want run-error", res.Outcome)
	}
}

func TestExecBoundaryIgnoresStderrSecrets(t *testing.T) {
	// stderr carrying secret-looking diagnostics must not affect a valid
	// stdout decode and must never be captured into the result.
	writeFakeGh(t, `echo 'warning: token gho_STDERRSECRET may be stale' >&2; printf '%s' '`+storedFixture+`'`)
	res := CheckStatus(context.Background())
	if res.Outcome != OutcomeAuthenticated {
		t.Fatalf("outcome = %v, want authenticated", res.Outcome)
	}
	for _, s := range resultStrings(res) {
		if strings.Contains(s, "STDERRSECRET") {
			t.Fatalf("stderr secret surfaced: %s", s)
		}
	}
}
