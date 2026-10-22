package githubconnect

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/session"
	"github.com/noisefloor-oss/deck/internal/terminal"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// ghLoginArgv is the fixed native login argv. No token material is ever
// passed in argv. --clipboard=false: gh 2.101+ otherwise tries a clipboard
// that the runtime does not have and prints an install-xsel warning above
// the code.
var ghLoginArgv = []string{"gh", "auth", "login", "--hostname", Host, "--git-protocol", "https", "--web", "--clipboard=false"}

// ghLoginEnv stops gh opening a browser on the machine Deck runs on: the
// browser that shows the code opens GitHub itself.
var ghLoginEnv = []string{"GH_BROWSER=true"}

// maxAttempts caps how many attempt IDs one backend incarnation may admit.
// Each ID creates an inputgate entry that is never evicted and a lease.Local
// entry that lives as long as the incarnation, so the budget bounds those
// maps; IDs are never recycled.
const maxAttempts = 1024

// launchBudget bounds normal spawn, identity capture, and initial observation.
// On a lost spawn response, tmux recovery has its own finite timeout.
const launchBudget = 30 * time.Second

// Attempt states. Exit cannot establish authenticated — only a later native
// status read can — so there is no "succeeded" state here.
const (
	AttemptRunning     = "running"
	AttemptCancelled   = "cancelled"
	AttemptExited      = "exited"
	AttemptUnavailable = "unavailable"
)

// Manager owns the one current native GitHub login attempt. It is
// fixed-purpose: it launches exactly gh auth login --web into a Deck-owned
// tmux namespace, records the full observed pane identity, and cancels only
// that exact pane. It never reads credential files or writes project/session
// rows, and it reads only the device code from the pane; native gh status is
// the only authority on whether authentication happened.
type Manager struct {
	tmux *tmux.Client
	// gate is the SAME boot inputgate the session terminals use: explicit
	// cancellation takes workspace admission and the attempt's session gate
	// like any other terminal mutation, and a fenced workspace refuses new
	// attempts.
	gate *inputgate.Gate
	log  zerolog.Logger
	// statusFn reads native gh status; production uses CheckStatus. It is
	// a dependency, not a cache: Start consults it immediately before
	// launching so an existing account is never silently replaced.
	statusFn func(context.Context) Result
	// sendKey writes one key to a pane; production uses the tmux client.
	sendKey func(ctx context.Context, paneID, key string) error

	mu       sync.Mutex
	current  *attempt
	admitted int
	closed   bool
	// maxAttempts is the admission budget; tests shrink it.
	maxAttempts int
}

// attempt is one admitted login attempt and its full captured identity.
type attempt struct {
	id    string
	owner string
	state string
	// generation is 1: an attempt pane is created once and never
	// respawned by Deck. A dead pane ends the attempt; it is not replayed.
	generation int
	marker     string
	bootEpoch  string
	started    time.Time
	// info is the full observed identity at spawn: tmux session/pane ids,
	// pid, process group, start ticks, plus the generation and marker pane
	// options. Every later verify or kill uses this exact tuple.
	info *tmux.PaneInfo
	// code is the one-time device code read from the pane. It is
	// short-lived and not a credential, but it is never logged.
	code string
	// ctx is cancelled only by explicit Cancel (or backend exit). Browser
	// attachment contexts are derived from it, so closing a browser never
	// cancels the attempt and cancelling the attempt revokes every output
	// attachment.
	ctx    context.Context
	cancel context.CancelFunc
}

// AttemptMetadata is the safe projection of the current attempt. It carries
// no identity tuple and no pane state.
type AttemptMetadata struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	Generation int       `json:"generation"`
	StartedAt  time.Time `json:"started_at"`
	// Final reports that the attempt will not change again. An unavailable
	// attempt that is not final is one tmux could not be read for just now.
	Final bool `json:"final,omitempty"`
	// Code is the device code the user enters on GitHub, once gh prints it.
	Code string `json:"code,omitempty"`
}

// Sentinel refusals. They name the reason shown to the user; none carries
// provider output or credential detail.
var (
	// ErrUnauthenticated: no authenticated principal made the request.
	ErrUnauthenticated = errors.New("github connect: unauthenticated")
	// ErrNotOwner: the attempt belongs to a different principal.
	ErrNotOwner = errors.New("github connect: attempt owned by another user")
	// ErrUnknownAttempt: no such attempt is current — unknown ID, or a
	// cancelled/finished attempt whose ID must not be reused.
	ErrUnknownAttempt = errors.New("github connect: no current attempt")
	// ErrAttemptInProgress: one current attempt per manager; a live one
	// must be cancelled before starting another.
	ErrAttemptInProgress = errors.New("github connect: an attempt is already in progress")
	// ErrAlreadyConnected: native status reports an authenticated account;
	// Deck never silently replaces or switches accounts.
	ErrAlreadyConnected = errors.New("github connect: an account is already connected")
	// ErrExistingAccount: an account exists but is not usable; Deck never
	// silently replaces or switches accounts, environment-owned or not.
	ErrExistingAccount = errors.New("github connect: an existing account must be resolved natively before connecting")
	// ErrMissingCLI: the gh executable is absent, so login cannot run.
	ErrMissingCLI = errors.New("github connect: gh CLI not found")
	// ErrAttemptBudget: this backend incarnation has admitted too many
	// attempt IDs; they are never recycled.
	ErrAttemptBudget = errors.New("github connect: attempt budget exhausted for this backend incarnation")
	// ErrAttemptNotRunning: the attempt exists but is not live.
	ErrAttemptNotRunning = errors.New("github connect: attempt is not running")
	// ErrIdentityChanged: the captured process no longer owns the pane.
	ErrIdentityChanged = errors.New("github connect: login process identity changed; nothing was stopped")
	// ErrCancelUncertain: stopping the owned process could not be confirmed.
	ErrCancelUncertain = errors.New("github connect: cancellation could not be confirmed")
)

// NewManager wires the fixed-purpose login manager. The tmux client must be
// a Deck-owned namespace, never the user's default socket; the gate must
// be the boot inputgate so workspace fencing covers this surface too.
func NewManager(t *tmux.Client, gate *inputgate.Gate, log zerolog.Logger) *Manager {
	return &Manager{
		tmux:        t,
		gate:        gate,
		log:         log,
		statusFn:    CheckStatus,
		sendKey:     t.SendKey,
		maxAttempts: maxAttempts,
	}
}

// Status reads native gh status. It never starts a login.
func (m *Manager) Status(ctx context.Context) Result {
	return m.statusFn(ctx)
}

// Start launches an explicitly requested native login, preserving existing
// accounts and refusing to replace an attempt whose exit is unconfirmed
// unless Deck has given up on it and its owner asks again. A launch failure
// is never replayed automatically.
func (m *Manager) Start(ctx context.Context, principal string) (AttemptMetadata, error) {
	if principal == "" {
		return AttemptMetadata{}, ErrUnauthenticated
	}
	res := m.statusFn(ctx)
	switch res.Outcome {
	case OutcomeNoAccount:
		// No existing account needs replacement.
	case OutcomeAuthenticated:
		return AttemptMetadata{}, ErrAlreadyConnected
	case OutcomeAuthIssue:
		if res.Status == nil || res.Status.Account.Source != SourceStored || !res.Status.Account.SourceWriteable || !res.Status.Account.Active || res.Status.Account.State != StateError || res.Status.Account.Login == "" {
			return AttemptMetadata{}, ErrExistingAccount
		}
	case OutcomeMissingCLI:
		return AttemptMetadata{}, ErrMissingCLI
	default:
		return AttemptMetadata{}, fmt.Errorf("github connect: native status unreadable (%s)", res.Outcome)
	}

	// A fenced workspace refuses new attempts like any other terminal
	// mutation; admission is held across the native launch.
	release, err := m.gate.Admit()
	if err != nil {
		return AttemptMetadata{}, fmt.Errorf("github connect: %w", err)
	}
	defer release()

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return AttemptMetadata{}, ErrAttemptNotRunning
	}
	if m.current != nil {
		// Only a confirmed terminal attempt frees the slot for another owner.
		prior := m.refreshLocked(ctx, m.current)
		if prior.Status == AttemptRunning || prior.Status == AttemptUnavailable {
			if m.current.owner != principal {
				return AttemptMetadata{}, ErrNotOwner
			}
			if !prior.Final {
				return AttemptMetadata{}, ErrAttemptInProgress
			}
		}
	}
	if m.admitted >= m.maxAttempts {
		return AttemptMetadata{}, ErrAttemptBudget
	}

	id, err := randomID()
	if err != nil {
		return AttemptMetadata{}, err
	}
	marker, err := randomID()
	if err != nil {
		return AttemptMetadata{}, err
	}
	// The boot epoch is sampled before the spawn: start ticks are
	// boot-relative, and an unreadable epoch would create an identity
	// tuple that can never be safely verified.
	epoch, err := tmux.BootID()
	if err != nil {
		return AttemptMetadata{}, fmt.Errorf("github connect: read boot epoch: %w", err)
	}
	name := "deck-ghconnect-" + id[:12]
	// gh auth login is indifferent to the working directory, but tmux
	// requires one: use this backend's own cwd.
	wd, err := os.Getwd()
	if err != nil {
		return AttemptMetadata{}, fmt.Errorf("github connect: working directory: %w", err)
	}
	// Do not detach a request that ended before launch admission.
	if err := ctx.Err(); err != nil {
		return AttemptMetadata{}, fmt.Errorf("github connect: start request ended before launch: %w", err)
	}
	m.admitted++
	actx, cancel := context.WithCancel(context.Background())
	a := &attempt{id: id, owner: principal, state: AttemptUnavailable,
		generation: 1, marker: marker, bootEpoch: epoch, started: time.Now().UTC(), ctx: actx, cancel: cancel}
	m.current = a
	// Browser disconnects must not interrupt an admitted spawn or its capture.
	launchCtx, launchCancel := context.WithTimeout(context.WithoutCancel(ctx), launchBudget)
	defer launchCancel()
	// A surviving tmux server can send gh to an old home and Keychain.
	loginEnv := append([]string{}, ghLoginEnv...)
	for _, name := range []string{"HOME", "GH_CONFIG_DIR", "XDG_CONFIG_HOME"} {
		loginEnv = append(loginEnv, name+"="+os.Getenv(name))
	}
	info, err := m.tmux.CreateSession(launchCtx, tmux.SessionSpec{
		Name:                 name,
		WorkDir:              wd,
		Argv:                 ghLoginArgv,
		Env:                  loginEnv,
		Generation:           1,
		Marker:               marker,
		Cols:                 80,
		Rows:                 24,
		RetainPartialOnError: true,
	})
	a.info = info
	if err != nil {
		// A failed response can follow a successful spawn. Preserve the process
		// and retain an unavailable attempt: only its owner asking again may
		// launch another, and a pane whose full identity was not captured is
		// never signalled.
		cancel()
		return AttemptMetadata{}, fmt.Errorf("github connect: launch could not be confirmed: %w", err)
	}
	a.state = AttemptRunning
	m.log.Info().Str("attempt", id).Msg("github connect attempt started")
	go m.watch(a)
	return m.refreshLocked(launchCtx, a), nil
}

// Current returns the current attempt's metadata for its owner only. A
// foreign owner is told nothing.
func (m *Manager) Current(ctx context.Context, principal string) (AttemptMetadata, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.owner != principal {
		return AttemptMetadata{}, false
	}
	return m.refreshLocked(ctx, m.current), true
}

// AttachContext returns the attempt's lifecycle context for an output
// attachment. Browser contexts derive from it: closing the browser never
// cancels the attempt, and cancelling the attempt revokes the attachment.
func (m *Manager) AttachContext(principal, id string) (context.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.current
	if a == nil || a.id != id {
		return nil, ErrUnknownAttempt
	}
	if a.owner != principal {
		return nil, ErrNotOwner
	}
	if a.state != AttemptRunning {
		return nil, ErrAttemptNotRunning
	}
	return a.ctx, nil
}

// Cancel performs the explicit cancellation. Unknown IDs are refused before
// any gate allocation; a foreign owner is refused; the workspace must admit
// the mutation and the attempt's session gate is held across it. The output
// attachment is revoked (the attempt context is cancelled), then the exact
// captured pane identity is killed — a pane that no longer matches the
// captured tuple is never killed.
func (m *Manager) Cancel(ctx context.Context, principal, id string) error {
	m.mu.Lock()
	a := m.current
	if a == nil || a.id != id || a.state != AttemptRunning {
		m.mu.Unlock()
		return ErrUnknownAttempt
	}
	if a.owner != principal {
		m.mu.Unlock()
		return ErrNotOwner
	}
	m.mu.Unlock()

	release, err := m.gate.Admit()
	if err != nil {
		return fmt.Errorf("github connect: cancel: %w", err)
	}
	defer release()
	state := m.gate.Session(a.id)
	state.Lock()
	defer state.Unlock()

	m.mu.Lock()
	if m.current != a || a.state != AttemptRunning {
		m.mu.Unlock()
		return ErrUnknownAttempt
	}
	m.mu.Unlock()

	// Every observed identity field is checked before the atomic tmux kill.
	live, err := m.observeOwned(ctx, a)
	if err != nil {
		if errors.Is(err, tmux.ErrNoSession) {
			m.finishAttempt(a, AttemptExited)
			return ErrAttemptNotRunning
		}
		if errors.Is(err, terminal.ErrTargetIdentityMismatch) {
			m.finishAttempt(a, AttemptUnavailable)
			return ErrIdentityChanged
		}
		return ErrCancelUncertain
	}
	if live.Dead {
		m.finishAttempt(a, AttemptExited)
		return ErrAttemptNotRunning
	}
	a.cancel()
	applied, err := m.tmux.KillPaneIfIdentity(ctx, tmux.PaneIdentity{
		SessionID: a.info.SessionID, PaneID: a.info.PaneID, PanePID: a.info.PID,
		Generation: a.generation, Marker: a.marker,
	})
	if err != nil {
		m.finishAttempt(a, AttemptUnavailable)
		return ErrCancelUncertain
	}
	if !applied {
		m.finishAttempt(a, AttemptUnavailable)
		return ErrIdentityChanged
	}
	m.finishAttempt(a, AttemptCancelled)
	return nil
}

// Close revokes browser attachment authority without signalling native processes.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.current != nil {
		m.current.cancel()
		m.current.state = AttemptUnavailable
	}
}

func (m *Manager) finishAttempt(a *attempt, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a.cancel()
	a.state = state
}

// refreshLocked reports process state from tmux, never from native output.
// A transient observation failure does not make another launch safe.
func (m *Manager) refreshLocked(ctx context.Context, a *attempt) AttemptMetadata {
	meta := a.meta()
	if a.state != AttemptRunning {
		return meta
	}
	live, err := m.observeOwned(ctx, a)
	switch {
	case errors.Is(err, tmux.ErrNoSession):
		a.state = AttemptExited
		a.cancel()
	case errors.Is(err, terminal.ErrTargetIdentityMismatch):
		a.state = AttemptUnavailable
		a.cancel()
	case err != nil:
		meta.Status = AttemptUnavailable
		return meta
	case live.Dead:
		a.state = AttemptExited
		a.cancel()
	}
	return a.meta()
}

func (m *Manager) observeOwned(ctx context.Context, a *attempt) (*tmux.PaneInfo, error) {
	if err := m.tmux.VerifySocket(); err != nil {
		return nil, err
	}
	if err := m.tmux.VerifySecurity(); err != nil {
		return nil, err
	}
	live, err := m.tmux.PaneInfoForSession(ctx, a.info.SessionName)
	if err != nil {
		return nil, err
	}
	if err := session.VerifyIdentity(a.sessionRecord(), live); err != nil {
		return nil, terminal.ErrTargetIdentityMismatch
	}
	return live, nil
}

// lookupCurrent resolves the one current attempt by ID for target
// verification: a stale or unknown ID is absent, a cancelled one has no
// live terminal to attach to.
func (m *Manager) lookupCurrent(id string) (*attempt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.current
	if a == nil || a.id != id {
		return nil, terminal.ErrTargetAbsent
	}
	if a.state != AttemptRunning {
		return nil, terminal.ErrTargetNotRunning
	}
	return a, nil
}

func (a *attempt) meta() AttemptMetadata {
	meta := AttemptMetadata{ID: a.id, Status: a.state, Generation: a.generation, StartedAt: a.started, Final: a.state != AttemptRunning}
	if a.state == AttemptRunning {
		meta.Code = a.code
	}
	return meta
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("github connect: randomness unavailable: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
