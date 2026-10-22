package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// serverConfigFormat is applied when the tmux server starts, via `-f`.
//
// remain-on-exit MUST be established this way rather than with a set-option
// call after new-session. Measured on tmux 3.6a: creating a session whose
// command exits immediately and then setting the option loses the session 8
// times out of 10 — the pane is gone before the option lands, taking the
// recorded exit evidence with it. Applied as server config it is set before
// any pane runs: 0 losses in the same test.
//
// exit-empty off keeps the server alive once its last session is killed, so
// the global option survives rather than being silently lost with the server.
//
// status off removes tmux's default status line. The provider CLI draws its
// own UI; the green bar is redundant chrome whose right edge truncates
// mid-content and reads as a rendering defect.
//
// history-limit is the pane scrollback depth copy-mode scrolls through
// (NOISEDECK_TMUX_SCROLLBACK_LINES). It must be in force BEFORE a pane is created:
// tmux reads it at pane creation and never re-reads it, so setting it later
// leaves existing panes at the 2000-line default.
//
// terminal-features adds `sync` for the attaching client, whose TERM is
// screen-256color. tmux already batches one pane redraw between
// synchronized-update markers, but only for a client whose terminal claims the
// capability, and tmux's own defaults grant screen* nothing but `title`.
// Measured both ways on this config: with the feature the attached client
// receives the markers around a resize repaint; without it, none are sent at
// all. Unbatched, the browser renders every intermediate state of a provider's
// full-screen repaint — the whole-screen redraw operators report against
// inline CLIs like omp and pi, which rewrite their entire transcript rather
// than one alternate screen. The markers are DEC private mode 2026 from tmux
// 3.4; older tmux emits an iTerm2 DCS form that a browser ignores.
const serverConfigFormat = `set-option -g remain-on-exit on
set-option -g exit-empty off
set-option -g status off
set-option -g history-limit %d
set-option -as terminal-features ",screen*:sync"
`

// DefaultScrollbackLines is the pane history depth used when the caller does
// not configure one. It matches the browser terminal's own scrollback
// (web/src/terminal/term.ts).
const DefaultScrollbackLines = 2000

// verifySocketDir requires the directory to be owned by the runtime user
// with mode 0700. Startup fails on mismatch rather than proceeding, because
// a group- or world-accessible socket lets any local process drive the
// user's agent sessions.
//
// Ownership is checked as well as mode. A 0700 directory owned by somebody
// else is not protection — its owner can still read, traverse, and replace the
// socket inside it, so mode alone satisfies the letter of the requirement
// while leaving the threat it names wide open.
func verifySocketDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("tmux: stat socket directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("tmux: %q is not a directory", dir)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		return fmt.Errorf(
			"tmux: socket directory %q has mode %04o, want 0700: a group- or "+
				"world-accessible tmux socket lets other local processes control "+
				"agent sessions", dir, perm)
	}
	return verifyOwnedByRuntimeUser(dir, info)
}

// VerifySocket checks the socket file itself once it exists. The directory
// check alone is not sufficient — the socket's own mode and owner govern who
// may connect to a running server.
func (c *Client) VerifySocket() error {
	info, err := os.Stat(c.socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil // no server yet; nothing to verify
	}
	if err != nil {
		return fmt.Errorf("tmux: stat socket: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf(
			"tmux: socket %q has mode %04o, want no group or other access",
			c.socketPath, perm)
	}
	return verifyOwnedByRuntimeUser(c.socketPath, info)
}

// VerifySecurity re-checks the socket directory and the socket. It is the
// call readiness should make, since permissions can change after startup.
func (c *Client) VerifySecurity() error {
	if err := verifySocketDir(filepath.Dir(c.socketPath)); err != nil {
		return err
	}
	return c.VerifySocket()
}

// serverOptions is the global option list this client requires, in one
// place so the -f config, the inline set-option prefixes, and the read-back
// verification cannot drift apart.
//
// The three application points are NOT equivalent for history-limit: tmux
// sizes a pane's history once, at pane creation, so the option governs
// panes created AFTER it is in force. An existing pane keeps the depth it
// was born with — including across respawn-pane, which reuses the pane
// (measured on tmux 3.6a; see RespawnPane).
func (c *Client) serverOptions() [][2]string {
	return [][2]string{
		{"remain-on-exit", "on"},
		{"exit-empty", "off"},
		{"status", "off"},
		{"history-limit", strconv.Itoa(c.scrollbackLines)},
	}
}

// setOptionArgs renders serverOptions as an ordered set-option command
// prefix for a compound tmux invocation.
func (c *Client) setOptionArgs() []string {
	var args []string
	for _, opt := range c.serverOptions() {
		args = append(args, "set-option", "-g", opt[0], opt[1], ";")
	}
	return args
}

// verifyServerOptions reads back the options that must be in force.
//
// The `-f` config alone is not enough: tmux applies a config file ONLY when it
// starts a server, and silently ignores it otherwise. Measured on tmux 3.6a —
// against a server started without our config, passing `-f` had no effect
// (remain-on-exit stayed off) and an immediately-exiting session vanished,
// exactly the failure the config exists to prevent. That server can exist for
// mundane reasons: a previous Deck build, a crashed run, or a
// leftover socket.
//
// So the options are set explicitly when a server is already up, and then read
// back. Setting without verifying would leave the same silent failure one typo
// away.
func (c *Client) verifyServerOptions(ctx context.Context) error {
	for _, opt := range c.serverOptions() {
		if opt[0] == "status" {
			// status is cosmetic chrome, not exit-evidence correctness; a
			// mismatch would not lose data, so it is set but not fatal here.
			continue
		}
		got, err := c.run(ctx, "show-options", "-g", "-v", opt[0])
		if err != nil {
			return fmt.Errorf("tmux: read back global %s: %w", opt[0], err)
		}
		if got != opt[1] {
			return fmt.Errorf(
				"tmux: global %s is %q, want %q: a server running without the "+
					"required options loses exit evidence (remain-on-exit, "+
					"exit-empty) or scrollback depth (history-limit)",
				opt[0], got, opt[1])
		}
	}
	return nil
}
