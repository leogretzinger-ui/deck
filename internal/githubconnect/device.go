package githubconnect

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/noisefloor-oss/deck/internal/tmux"
)

// gh asks whether to set up Git credentials (default yes) before it prints
// the code, then waits for Enter before it polls GitHub. Deck answers both
// with Enter, as you would in the terminal, so sign-in finishes as soon as
// you approve the code on GitHub. Nothing else in the pane is interpreted.
var deviceCodeLine = regexp.MustCompile(`(?m)First copy your one-time code: ([0-9A-Z]{4}-[0-9A-Z]{4})[ \t]*$`)

const gitCredentialQuestion = "? Authenticate Git with your GitHub credentials? (Y/n)"

// watchBudget bounds how long Deck waits for gh to print the code; after
// it, the pane is still there behind Show details.
const (
	watchBudget   = 2 * time.Minute
	watchInterval = 250 * time.Millisecond
)

// deviceCode returns the one-time code gh printed, or "".
func deviceCode(screen string) string {
	if m := deviceCodeLine.FindStringSubmatch(screen); m != nil {
		return m[1]
	}
	return ""
}

// awaitsGitAnswer reports whether the last line gh printed is its
// unanswered Git credentials question.
func awaitsGitAnswer(screen string) bool {
	lines := strings.Split(strings.TrimRight(screen, " \t\r\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1]) == gitCredentialQuestion
}

// watch reads the attempt's own pane until gh prints the device code,
// records it, and lets gh start polling. It ends with the attempt.
func (m *Manager) watch(a *attempt) {
	ctx, cancel := context.WithTimeout(a.ctx, watchBudget)
	defer cancel()
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()
	answered := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		live, err := m.observeOwned(ctx, a)
		if errors.Is(err, tmux.ErrNoSession) || (err == nil && live.Dead) {
			return
		}
		if err != nil {
			continue
		}
		screen, err := m.tmux.CapturePaneJoined(ctx, a.info.PaneID)
		if err != nil {
			continue
		}
		if code := deviceCode(screen); code != "" {
			m.mu.Lock()
			a.code = code
			m.mu.Unlock()
			if err := m.pressEnter(ctx, a); err != nil {
				m.log.Warn().Str("attempt", a.id).Err(err).Msg("github connect could not continue sign-in")
			}
			return
		}
		// A failed send may still have reached gh, and the question stays on
		// screen until gh redraws, so the answer is sent at most once and a
		// failure leaves the rest of the sign-in to you.
		if !answered && awaitsGitAnswer(screen) {
			answered = true
			if err := m.pressEnter(ctx, a); err != nil {
				m.log.Warn().Str("attempt", a.id).Err(err).Msg("github connect could not answer the Git question")
				return
			}
		}
	}
}

// pressEnter sends one Enter to the attempt's exact pane, under the same
// workspace admission and session gate as every terminal write.
func (m *Manager) pressEnter(ctx context.Context, a *attempt) error {
	release, err := m.gate.Admit()
	if err != nil {
		return err
	}
	defer release()
	state := m.gate.Session(a.id)
	state.Lock()
	defer state.Unlock()
	m.mu.Lock()
	running := m.current == a && a.state == AttemptRunning
	m.mu.Unlock()
	if !running {
		return ErrAttemptNotRunning
	}
	live, err := m.observeOwned(ctx, a)
	if err != nil {
		return err
	}
	if live.Dead {
		return ErrAttemptNotRunning
	}
	return m.sendKey(ctx, a.info.PaneID, "Enter")
}
