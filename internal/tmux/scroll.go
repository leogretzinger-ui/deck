package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// CapturePane returns the pane's visible contents. It exists for tests and
// manual diagnostics.
//
// Product code must never parse this to infer provider state — deciding
// whether a CLI is busy, idle, or at a prompt from its output is forbidden.
func (c *Client) CapturePane(ctx context.Context, paneID string) (string, error) {
	if err := ValidatePaneID(paneID); err != nil {
		return "", err
	}
	return c.run(ctx, "capture-pane", "-p", "-t", paneTarget(paneID))
}

// CapturePaneJoined returns the pane's visible contents with wrapped lines
// joined, so a narrow pane cannot split one. It exists to read the device
// code from Deck's own GitHub sign-in pane; the prohibition above applies.
func (c *Client) CapturePaneJoined(ctx context.Context, paneID string) (string, error) {
	if err := ValidatePaneID(paneID); err != nil {
		return "", err
	}
	return c.run(ctx, "capture-pane", "-p", "-J", "-t", paneTarget(paneID))
}

// CapturePaneHistory returns the pane's full scrollback, not just the
// visible screen. It exists for managed BATCH jobs (Taskmaster generation)
// whose recorded output is the product data the user asked for. The
// prohibition above applies unchanged to provider panes: this must never be
// used to read an agent CLI's output.
func (c *Client) CapturePaneHistory(ctx context.Context, paneID string) (string, error) {
	if err := ValidatePaneID(paneID); err != nil {
		return "", err
	}
	return c.run(ctx, "capture-pane", "-p", "-S", "-", "-t", paneTarget(paneID))
}

// CapturePaneTail returns the last `lines` lines of a pane's output,
// scrollback included. It exists so the session API can hand a dead pane's
// final output to the user's recovery card: what the provider printed
// before it stopped is the user's explanation, and Deck carries those
// bytes without reading them. The prohibition above is unchanged — product
// code never decides anything from this text, and a live provider pane is
// never captured through it.
func (c *Client) CapturePaneTail(ctx context.Context, paneID string, lines int) (string, error) {
	if err := ValidatePaneID(paneID); err != nil {
		return "", err
	}
	if lines < 1 {
		lines = 1
	}
	return c.run(ctx, "capture-pane", "-p", "-S", "-"+strconv.Itoa(lines), "-E", "-", "-t", paneTarget(paneID))
}

// MaxCopyModeScrollLines bounds one scroll command. It exists so a caller
// relaying browser events can never turn one frame into an unbounded tmux
// repeat count.
const MaxCopyModeScrollLines = 500

// clampScrollLines normalizes a requested line count into [1, max].
func clampScrollLines(lines int) int {
	if lines < 1 {
		return 1
	}
	if lines > MaxCopyModeScrollLines {
		return MaxCopyModeScrollLines
	}
	return lines
}

// CopyModeScrollUp enters tmux copy-mode on the pane (a no-op when the pane
// is already in a mode) and scrolls the view up through the pane's history.
//
// This is the authority-invariant scrollback mechanism: tmux owns the pane
// and its history, so scrolling is a tmux view command, not a parallel
// browser-side buffer. Entry uses -e, so scrolling back down to the bottom
// returns the pane to the live TUI automatically.
func (c *Client) CopyModeScrollUp(ctx context.Context, paneID string, lines int) error {
	_, err := c.CopyModeScrollUpState(ctx, paneID, lines)
	return err
}

// CopyModeScrollUpState performs CopyModeScrollUp and returns tmux's
// post-command pane_in_mode state from the same tmux invocation.
func (c *Client) CopyModeScrollUpState(ctx context.Context, paneID string, lines int) (bool, error) {
	if err := ValidatePaneID(paneID); err != nil {
		return false, err
	}
	n := clampScrollLines(lines)
	out, err := c.run(ctx,
		"copy-mode", "-e", "-t", paneTarget(paneID), ";",
		"send-keys", "-X", "-N", strconv.Itoa(n), "-t", paneTarget(paneID), "scroll-up", ";",
		"display-message", "-p", "-t", paneTarget(paneID), "#{pane_in_mode}")
	if err != nil {
		return false, err
	}
	return parsePaneInMode(out)
}

// CopyModeScrollDown scrolls the copy-mode view down. When the pane is not
// in a mode it does nothing: scrolling down past live output has no
// meaning, and `send-keys -X` outside a mode is an error, so the command is
// guarded server-side with tmux's own pane_in_mode flag (tmux-owned state —
// reading it is not provider-output inference).
//
// Reaching the bottom exits copy-mode when entry used -e (CopyModeScrollUp
// always does), which is what hands the wheel back to the TUI.
func (c *Client) CopyModeScrollDown(ctx context.Context, paneID string, lines int) error {
	_, err := c.CopyModeScrollDownState(ctx, paneID, lines)
	return err
}

// CopyModeScrollDownState performs CopyModeScrollDown and returns tmux's
// post-command pane_in_mode state from the same tmux invocation.
func (c *Client) CopyModeScrollDownState(ctx context.Context, paneID string, lines int) (bool, error) {
	if err := ValidatePaneID(paneID); err != nil {
		return false, err
	}
	n := clampScrollLines(lines)
	// The embedded command string is composed ONLY of a validated pane id
	// (%N) and a clamped integer — no caller-supplied text reaches tmux's
	// command parser.
	out, err := c.run(ctx,
		"if-shell", "-F", "-t", paneTarget(paneID), "#{pane_in_mode}",
		fmt.Sprintf("send-keys -X -N %d -t %s scroll-down", n, paneID), ";",
		"display-message", "-p", "-t", paneTarget(paneID), "#{pane_in_mode}")
	if err != nil {
		return false, err
	}
	return parsePaneInMode(out)
}

// ExitCopyMode leaves the pane's tmux view mode, if one is open, so
// subsequent input reaches the application instead of the mode's key table.
// It is a guarded no-op when the pane is not in a mode — `send-keys -X`
// outside a mode is an error, and racing a user's own exit must not
// fail a write. The guard reads tmux's own pane_in_mode flag server-side,
// atomically with the cancel.
func (c *Client) ExitCopyMode(ctx context.Context, paneID string) error {
	_, err := c.ExitCopyModeState(ctx, paneID)
	return err
}

// ExitCopyModeState performs ExitCopyMode and returns tmux's post-command
// pane_in_mode state from the same tmux invocation.
func (c *Client) ExitCopyModeState(ctx context.Context, paneID string) (bool, error) {
	if err := ValidatePaneID(paneID); err != nil {
		return false, err
	}
	// The embedded command string is composed ONLY of a validated pane id
	// (%N) — no caller-supplied text reaches tmux's command parser.
	out, err := c.run(ctx,
		"if-shell", "-F", "-t", paneTarget(paneID), "#{pane_in_mode}",
		fmt.Sprintf("send-keys -X -t %s cancel", paneID), ";",
		"display-message", "-p", "-t", paneTarget(paneID), "#{pane_in_mode}")
	if err != nil {
		return false, err
	}
	return parsePaneInMode(out)
}

func parsePaneInMode(out string) (bool, error) {
	switch strings.TrimSpace(out) {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("tmux: unexpected pane_in_mode value %q", out)
	}
}
