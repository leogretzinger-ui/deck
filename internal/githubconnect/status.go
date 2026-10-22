// Package githubconnect reads authentication status through the native GitHub CLI.
// Raw diagnostics and credential source paths never leave the decoder.
package githubconnect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

// Host is the only host Deck connects for now.
const Host = "github.com"

// maxOutput bounds captured stdout so a runaway gh cannot exhaust memory.
const maxOutput = 64 * 1024

// defaultTimeout bounds the whole status run.
const defaultTimeout = 10 * time.Second

// CredentialSource classifies where gh got the account's token, without
// carrying the raw tokenSource string (which may be a private file path).
type CredentialSource int

const (
	// SourceUnknown: a source gh's own rules do not recognize. The account
	// is reported as-is; callers must refuse destructive auth actions.
	SourceUnknown CredentialSource = iota
	// SourceEnvironment: GH_TOKEN/GITHUB_TOKEN in the environment. Deck
	// must not offer overwriting login for it.
	SourceEnvironment
	// SourceStored: keyring or a config file on disk — writeable by gh.
	SourceStored
)

// EntryState is gh's per-account state, decoded from the JSON "state" field.
type EntryState int

const (
	StateUnknown EntryState = iota
	StateSuccess
	StateTimeout
	StateError
)

func (s EntryState) String() string {
	switch s {
	case StateSuccess:
		return "success"
	case StateTimeout:
		return "timeout"
	case StateError:
		return "error"
	}
	return "unknown-state"
}

func (c CredentialSource) String() string {
	switch c {
	case SourceEnvironment:
		return "environment"
	case SourceStored:
		return "stored"
	}
	return "unknown"
}

// Account carries only the nonsecret fields Deck needs from gh auth status.
type Account struct {
	Login       string
	GitProtocol string
	// Active is true when gh marked this entry as the host's active account.
	Active bool
	State  EntryState
	// Source classifies the credential origin; the raw tokenSource string
	// (a private path for stored credentials) never leaves the decoder.
	Source CredentialSource
	// SourceWriteable permits replacing only a recognized stored credential.
	// Unknown and environment-owned sources require you to configure them.
	SourceWriteable bool
}

// Status is the decoded, allowlisted result for the requested host.
type Status struct {
	Account Account
}

// Outcome classifies why the status query ended the way it did. Exit code 0
// from gh does not imply an authenticated account, so authentication is a
// separate outcome from the run itself succeeding.
type Outcome int

const (
	// OutcomeAuthenticated: an active, state=success account for the
	// requested host. Credential source controls which actions are available.
	OutcomeAuthenticated Outcome = iota
	// OutcomeMissingCLI: the gh executable was not found.
	OutcomeMissingCLI
	// OutcomeNoAccount: gh ran but reported no account for the host
	// (including the documented empty {"hosts":{}} shape).
	OutcomeNoAccount
	// OutcomeAuthIssue: an account was reported but is not usable — wrong
	// state, missing login, wrong host, or an ambiguous active marking. Status carries the classified account so
	// callers can explain (e.g. environment-owned auth) without retrying.
	OutcomeAuthIssue
	// OutcomeMalformed: output was not valid status JSON or lacked the
	// hosts object entirely.
	OutcomeMalformed
	// OutcomeOversized: stdout exceeded the capture bound.
	OutcomeOversized
	// OutcomeTimeout: the context deadline or cancellation fired.
	OutcomeTimeout
	// OutcomeRunError: gh failed to run or exited nonzero for reasons the
	// other outcomes do not cover.
	OutcomeRunError
)

func (o Outcome) String() string {
	switch o {
	case OutcomeAuthenticated:
		return "authenticated"
	case OutcomeMissingCLI:
		return "missing-cli"
	case OutcomeNoAccount:
		return "no-account"
	case OutcomeAuthIssue:
		return "auth-issue"
	case OutcomeMalformed:
		return "malformed"
	case OutcomeOversized:
		return "oversized"
	case OutcomeTimeout:
		return "timeout"
	case OutcomeRunError:
		return "run-error"
	}
	return "unknown"
}

// Result is the outcome plus decoded data. Status is populated for
// authenticated and auth-issue outcomes; auth-issue Status always carries
// the credential-source classification even when the account is invalid.
type Result struct {
	Outcome Outcome
	Status  *Status
}

// runner is the command boundary; production uses execRun.
type runner func(ctx context.Context, argv []string) (stdout []byte, err error)

// ghStatusArgv is fixed: no tokens are ever requested or passed in argv.
var ghStatusArgv = []string{"gh", "auth", "status", "--active", "--hostname", Host, "--json", "hosts"}

// errGHNotFound reports a missing gh executable without wrapping stderr.
var errGHNotFound = errors.New("gh executable not found")

// errGHOversized reports output past the capture bound.
var errGHOversized = errors.New("gh auth status output exceeded capture bound")

// CheckStatus runs gh auth status and decodes the documented hosts JSON.
func CheckStatus(ctx context.Context) Result {
	return checkStatus(ctx, execRun)
}

func checkStatus(ctx context.Context, run runner) Result {
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	stdout, err := run(ctx, ghStatusArgv)
	if err != nil {
		// The injected command boundary may also report cancellation directly.
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Result{Outcome: OutcomeTimeout}
		}
		if errors.Is(err, errGHNotFound) {
			return Result{Outcome: OutcomeMissingCLI}
		}
		if errors.Is(err, errGHOversized) {
			return Result{Outcome: OutcomeOversized}
		}
		return Result{Outcome: OutcomeRunError}
	}

	acct, outcome := decode(stdout)
	if outcome == OutcomeAuthenticated || outcome == OutcomeAuthIssue {
		return Result{Outcome: outcome, Status: &Status{Account: acct}}
	}
	return Result{Outcome: outcome}
}

// execRun captures stdout only. stderr is discarded: gh diagnostics there
// can echo credential paths or token hints, and nothing in this package
// needs them. cmd.Env is left nil so the child inherits os.Environ and gh's
// own credential precedence applies natively.
func execRun(ctx context.Context, argv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var buf bytes.Buffer
	lw := &limitedWriter{w: &buf, n: maxOutput}
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
		return nil, fmt.Errorf("gh auth status failed: %w", err)
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

// decodedEntry mirrors only the allowlisted fields; unknown JSON keys
// (including gh's omitted "token" and any secret-looking extra) are dropped
// by the decoder. tokenSource is consumed for classification only.
type decodedEntry struct {
	Active      *bool  `json:"active"`
	GitProtocol string `json:"gitProtocol"`
	Host        string `json:"host"`
	Login       string `json:"login"`
	State       string `json:"state"`
	TokenSource string `json:"tokenSource"`
}

type decodedHosts struct {
	Hosts *map[string][]decodedEntry `json:"hosts"`
}

// classifySource maps gh's tokenSource onto the safe CredentialSource
// classification. gh emits an environment variable name (GH_TOKEN or
// GITHUB_TOKEN), "keyring", or a filesystem path (its internal
// "oauth_token" source is rewritten to the hosts.yml path before export).
// It never surfaces the path itself.
func classifySource(tokenSource string) CredentialSource {
	switch {
	case tokenSource == "GH_TOKEN" || tokenSource == "GITHUB_TOKEN": // #nosec G101 -- public environment variable names, not credential values.
		return SourceEnvironment
	case tokenSource == "keyring":
		return SourceStored
	case filepath.Base(tokenSource) == "hosts.yml" && filepath.Dir(tokenSource) != ".":
		return SourceStored
	}
	return SourceUnknown
}

func decode(stdout []byte) (Account, Outcome) {
	var d decodedHosts
	if len(stdout) == 0 || string(stdout) == "null" {
		return Account{}, OutcomeMalformed
	}
	if err := json.Unmarshal(stdout, &d); err != nil {
		return Account{}, OutcomeMalformed
	}
	// The hosts object must be present, even when empty: gh's documented
	// logged-out shape is {"hosts":{}}, while a missing key means we are
	// not looking at gh auth status output at all.
	if d.Hosts == nil {
		return Account{}, OutcomeMalformed
	}
	entries := (*d.Hosts)[Host]
	if len(entries) == 0 {
		return Account{}, OutcomeNoAccount
	}

	// gh puts the active entry first, but select on the marking rather
	// than trust position; ambiguity is a defect, not a winner.
	var active []decodedEntry
	for _, e := range entries {
		if e.Active != nil && *e.Active {
			active = append(active, e)
		}
	}
	if len(active) == 0 {
		e := entries[0]
		return accountOf(e), OutcomeAuthIssue
	}
	if len(active) > 1 {
		return Account{Active: true}, OutcomeAuthIssue
	}
	e := active[0]
	acct := accountOf(e)
	if e.Login == "" || e.Host != Host || e.State != "success" {
		return acct, OutcomeAuthIssue
	}
	return acct, OutcomeAuthenticated
}

func accountOf(e decodedEntry) Account {
	src := classifySource(e.TokenSource)
	return Account{
		Login:           e.Login,
		GitProtocol:     e.GitProtocol,
		Active:          e.Active != nil && *e.Active,
		State:           decodeState(e.State),
		Source:          src,
		SourceWriteable: src == SourceStored,
	}
}

func decodeState(s string) EntryState {
	switch s {
	case "success":
		return StateSuccess
	case "timeout":
		return StateTimeout
	case "error":
		return StateError
	}
	return StateUnknown
}
