package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Attachment is one live tmux client: a `tmux attach-session` process on
// its own PTY, which is exactly what a person at a terminal would run.
//
// A tmux CLIENT — not a pipe to the pane — is the whole trick. tmux does
// the terminal emulation, redraw, alternate screen, and resize negotiation
// itself, so Deck transports bytes and nothing else (the authority
// invariant). It also isolates lifetimes for free: closing one attachment
// kills one tmux client, and the session and its provider process never
// notice.
type Attachment struct {
	pty    *os.File
	cmd    *exec.Cmd
	name   string
	paneID string
	client *Client
}

// Attach starts a tmux client attached to the named session, sized to the
// browser's terminal.
//
// -A is deliberately absent: this must attach to an EXISTING session and
// fail otherwise. Creating one here would produce a shell pane outside the
// session layer's knowledge — a shell standing in for the provider
// executable, which must never happen.
func (c *Client) Attach(name string, cols, rows uint16) (*Attachment, error) {
	if err := ValidateSessionName(name); err != nil {
		return nil, err
	}
	if cols == 0 || rows == 0 {
		return nil, fmt.Errorf("tmux: attach %q: zero terminal size", name)
	}
	c.observeAttach(AttachObservation{SessionName: name, Cols: cols, Rows: rows})

	// -u forces UTF-8, so the attachment is UTF-8 clean regardless of the
	// service's own locale environment. The client runs in its own process
	// group: a signal aimed at the service must not reach it, and killing
	// the attachment must not touch the session.
	cmd := exec.Command("tmux", "-S", c.socketPath, "-f", c.configPath,
		"-u", "attach-session", "-t", "="+name)
	cmd.Env = []string{
		"TERM=" + defaultAttachTerm,
		"LANG=C.UTF-8",
		"PATH=/usr/bin:/bin",
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, fmt.Errorf("tmux: attach %q: %w", name, err)
	}
	// The pane ID is recorded so the write observer can attribute every
	// write to its exact pane (the attachment outlives renames).
	info, err := c.paneInfo(context.Background(), target(name))
	if err != nil {
		_ = f.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, fmt.Errorf("tmux: attach %q: resolve pane: %w", name, err)
	}
	return &Attachment{pty: f, cmd: cmd, name: name, paneID: info.PaneID, client: c}, nil
}

// defaultAttachTerm is the TERM the attaching client advertises. It is a
// fixed capable value rather than the service's own TERM, which under a
// supervisor is routinely `dumb` and would strip the TUI of the colours,
// alternate screen, and control-key handling an attachment must carry.
const defaultAttachTerm = "screen-256color"

// Read returns terminal output. It blocks until bytes arrive or the client
// exits.
func (a *Attachment) Read(p []byte) (int, error) { return a.pty.Read(p) }

// Write delivers raw input to the terminal. Callers must have validated the
// controller's fencing token first — this layer transports bytes and
// enforces no policy.
func (a *Attachment) Write(p []byte) (int, error) { return a.pty.Write(p) }

// WriteWithin writes with a deadline, so a wedged terminal cannot pin the
// caller indefinitely.
//
// The proxy holds the session write gate across a write, and a takeover
// needs that gate: an unbounded write on a pane whose reader has stalled
// would block the user from taking control, which must always be
// available. A write that misses the deadline means the terminal is not
// draining, and the attachment is dropped — the same answer a browser that
// will not drain gets.
//
// A PTY is pollable, so the deadline is real. If the platform refuses one
// (os.ErrNoDeadline), the write proceeds unbounded rather than failing: a
// missing deadline is a weaker guarantee, not a reason to refuse input.
func (a *Attachment) WriteWithin(p []byte, d time.Duration) (int, error) {
	a.client.observeWrite(WriteObservation{PaneID: a.paneID, Data: p})
	if err := a.pty.SetWriteDeadline(time.Now().Add(d)); err != nil {
		if !errors.Is(err, os.ErrNoDeadline) {
			return 0, err
		}
		return a.pty.Write(p)
	}
	defer func() { _ = a.pty.SetWriteDeadline(time.Time{}) }()
	return a.pty.Write(p)
}

// Resize applies the browser's terminal dimensions. tmux renegotiates the
// pane size with the provider, which is why resize works at all without
// this product understanding the TUI.
func (a *Attachment) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return fmt.Errorf("tmux: resize %q: zero terminal size", a.name)
	}
	return pty.Setsize(a.pty, &pty.Winsize{Cols: cols, Rows: rows})
}

// Close ends this attachment and nothing else: the tmux client dies, the
// session and the provider process it holds keep running.
func (a *Attachment) Close() error {
	closeErr := a.pty.Close()
	if a.cmd.Process != nil {
		// The client is its own session leader, so this reaches the client
		// and cannot escape to the pane's provider.
		_ = a.cmd.Process.Kill()
		_, _ = a.cmd.Process.Wait()
	}
	return closeErr
}

// Pid reports the attaching client's process id, for diagnostics.
func (a *Attachment) Pid() string {
	if a.cmd.Process == nil {
		return ""
	}
	return strconv.Itoa(a.cmd.Process.Pid)
}

// PaneID returns the pane this tmux client was verified against when it
// attached. Browser scroll commands target that pane directly rather than
// rediscovering the session's active pane for every frame.
func (a *Attachment) PaneID() string { return a.paneID }
