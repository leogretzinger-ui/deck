package tmux

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Option keys carrying Deck's pane identity. They are tmux user
// options, which is why they begin with '@'.
const (
	OptGeneration = "@noisedeck_generation"
	OptMarker     = "@noisedeck_marker"
)

// PaneInfo is the observed runtime identity of a session's pane.
//
// This is the tuple reconciliation compares. A session name, or a PID, or a
// command line alone is explicitly insufficient.
type PaneInfo struct {
	SessionID      string
	SessionName    string
	PaneID         string
	PID            int
	ProcessGroupID int
	StartTicks     int64
	Executable     string
	// Spawn* preserves the first complete physical tuple returned by the
	// new-session command. Later observations may describe the same pane after
	// an out-of-band respawn; controller publication must reject that drift.
	SpawnPID            int
	SpawnProcessGroupID int
	SpawnStartTicks     int64
	CurrentCommand      string
	Dead                bool
	ExitStatus          *int
	ExitSignal          *int
	Generation          int
	Marker              string
	// LaunchMarker is stamped in the tmux session environment by
	// CreateSession. It identifies a newly created pane before its ordinary
	// pane options have been written, so a partial launch can be rolled back
	// without trusting reusable tmux IDs alone.
	LaunchMarker string
	// InMode is tmux's own pane_in_mode flag: the pane is showing a tmux
	// view (copy-mode) instead of the application. While set, tmux routes
	// every key — send-keys and attached-client input alike — to the mode's
	// key table, so bytes written "to the pane" never reach the CLI. The hot
	// input path reads the same tmux flag through PaneInputState before user
	// input rather than silently losing keystrokes. It is tmux-owned
	// mode state, not provider output.
	InMode bool
}

// PaneInputState is the tmux-owned state that decides whether an attachment
// can truthfully report a write as sent. It deliberately excludes session and
// process identity: the hot input path needs the pane's current state, not a
// second reconciliation pass.
type PaneInputState struct {
	Dead   bool
	InMode bool
}

// PaneIdentity is the tmux-owned identity required for an atomic pane kill.
// Generation and Marker may both be zero only when LaunchMarker is present,
// which is the partial-Create state before pane options are written.
type PaneIdentity struct {
	SessionID    string
	PaneID       string
	PanePID      int
	Generation   int
	Marker       string
	LaunchMarker string
}

// SessionInfo is one entry from ListSessions.
type SessionInfo struct {
	Name    string
	ID      string
	Created int64
}

// HasSession reports whether the named session exists.
func (c *Client) HasSession(ctx context.Context, name string) (bool, error) {
	if err := ValidateSessionName(name); err != nil {
		return false, err
	}
	_, err := c.run(ctx, "has-session", "-t", target(name))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrNoSession) {
		return false, nil
	}
	// ErrNoServer propagates: "absent from a server we reached" and "we could
	// not reach the server" are different facts, and only the first is an
	// answer to the question asked.
	return false, err
}

// ListSessions returns every session on this socket.
func (c *Client) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	out, err := c.run(ctx, "list-sessions", "-F",
		"#{session_name}\t#{session_id}\t#{session_created}")
	if err != nil {
		if errors.Is(err, ErrNoSession) {
			return nil, nil // a reachable server with no sessions
		}
		// ErrNoServer is deliberately NOT flattened into an empty slice.
		// Reconciliation has to distinguish "the inventory is authoritatively
		// empty" from "we could not take an inventory": the first means every
		// recorded session is gone, the second means we know nothing. Returning
		// nil, nil for both would let a transient server outage look like every
		// session having exited.
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var sessions []SessionInfo
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			return nil, fmt.Errorf("tmux: unparseable list-sessions line %q", line)
		}
		created, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("tmux: unparseable session_created in %q: %w", line, err)
		}
		sessions = append(sessions, SessionInfo{Name: parts[0], ID: parts[1], Created: created})
	}
	return sessions, nil
}

// PaneRecord is one entry of the pane inventory.
type PaneRecord struct {
	SessionName string
	SessionID   string
	PaneID      string
	PID         int
	Dead        bool
	Generation  int
	Marker      string
	// ActivityAt is tmux's content-free window activity timestamp. Deck
	// creates one pane in one window per managed session, so the window stamp
	// is the pane's last observed PTY activity without reading its bytes.
	ActivityAt time.Time
}

// ListPanes enumerates every pane on this socket, across all sessions and
// windows.
//
// Unknown managed panes must be audited and left untouched. Per-session
// lookups only find panes already known to Deck, so reconciliation needs
// the whole inventory to find panes no recorded session claims.
//
// Like ListSessions, an unreachable server is an error rather than an empty
// inventory.
func (c *Client) ListPanes(ctx context.Context) ([]PaneRecord, error) {
	out, err := c.run(ctx, "list-panes", "-a", "-F",
		"#{session_name}\t#{session_id}\t#{pane_id}\t#{pane_pid}\t#{pane_dead}\t"+
			"#{@noisedeck_generation}\t#{@noisedeck_marker}\t#{window_activity}")
	if err != nil {
		if errors.Is(err, ErrNoSession) {
			return nil, nil
		}
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var panes []PaneRecord
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 8 {
			return nil, fmt.Errorf("tmux: unparseable list-panes line %q", line)
		}
		rec := PaneRecord{
			SessionName: parts[0],
			SessionID:   parts[1],
			PaneID:      parts[2],
			Dead:        parts[4] == "1",
			Marker:      parts[6],
		}
		if parts[3] != "" {
			if rec.PID, err = strconv.Atoi(parts[3]); err != nil {
				return nil, fmt.Errorf("tmux: unparseable pane_pid in %q: %w", line, err)
			}
		}
		if parts[5] != "" {
			if rec.Generation, err = strconv.Atoi(parts[5]); err != nil {
				return nil, fmt.Errorf("tmux: unparseable generation in %q: %w", line, err)
			}
		}
		if parts[7] != "" {
			seconds, parseErr := strconv.ParseInt(parts[7], 10, 64)
			if parseErr != nil {
				return nil, fmt.Errorf("tmux: unparseable window_activity in %q: %w", line, parseErr)
			}
			rec.ActivityAt = time.Unix(seconds, 0).UTC()
		}
		panes = append(panes, rec)
	}
	return panes, nil
}

func paneIdentity(info *PaneInfo) PaneIdentity {
	return PaneIdentity{
		SessionID: info.SessionID, PaneID: info.PaneID,
		PanePID:    info.PID,
		Generation: info.Generation, Marker: info.Marker,
		LaunchMarker: info.LaunchMarker,
	}
}

// paneFormat is one tab-separated record. pane_start_time is deliberately
// absent: tmux 3.6a does not provide it (it renders empty), so process start
// time comes from the host process table, which is also the value
// reconciliation needs for identity comparison.
const paneFormat = "#{session_id}\t#{session_name}\t#{pane_id}\t#{pane_pid}\t" +
	"#{pane_dead}\t#{pane_dead_status}\t#{pane_dead_signal}\t#{pane_current_command}\t" +
	"#{@noisedeck_generation}\t#{@noisedeck_marker}\t#{pane_in_mode}\t" +
	"#{E:" + launchMarkerEnv + "}"

// paneFieldCount is the number of tab-separated fields in paneFormat.
const paneFieldCount = 12

// PaneInfoForSession reads the identity tuple via a session-scoped target.
//
// This resolves the session's CURRENT pane, so it is for discovery only —
// finding the pane of a session whose pane id is not yet known, such as during
// startup reconciliation. Once a pane id is known, use PaneInfoByID: a
// session-scoped target silently follows the active pane if the provider opens
// another one.
func (c *Client) PaneInfoForSession(ctx context.Context, name string) (*PaneInfo, error) {
	if err := ValidateSessionName(name); err != nil {
		return nil, err
	}
	return c.paneInfo(ctx, target(name))
}

// PaneInfoByID reads the identity tuple for one exact pane.
func (c *Client) PaneInfoByID(ctx context.Context, paneID string) (*PaneInfo, error) {
	if err := ValidatePaneID(paneID); err != nil {
		return nil, err
	}
	return c.paneInfo(ctx, paneTarget(paneID))
}

// PaneInputState reads only the exact pane's current dead/view flags. Unlike
// PaneInfoByID it does not inspect the process table or rebuild the pane's
// identity tuple, so it is suitable for the input hot path.
func (c *Client) PaneInputState(ctx context.Context, paneID string) (PaneInputState, error) {
	if err := ValidatePaneID(paneID); err != nil {
		return PaneInputState{}, err
	}
	out, err := c.run(ctx, "display-message", "-p", "-t", paneTarget(paneID),
		"-F", "#{pane_dead}\t#{pane_in_mode}")
	if err != nil {
		return PaneInputState{}, err
	}
	parts := strings.Split(out, "\t")
	if len(parts) != 2 || (parts[0] != "0" && parts[0] != "1") ||
		(parts[1] != "0" && parts[1] != "1") {
		return PaneInputState{}, fmt.Errorf("tmux: unexpected pane input state %q", out)
	}
	return PaneInputState{Dead: parts[0] == "1", InMode: parts[1] == "1"}, nil
}

func (c *Client) paneInfo(ctx context.Context, tgt string) (*PaneInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	info, err := c.paneInfoOnce(ctx, tgt)
	if err != nil && ctx.Err() != nil {
		return nil, errors.Join(err, ctx.Err())
	}
	if err == nil || !paneObservationPending(err) || errors.Is(err, errPaneGroupPending) {
		return info, err
	}
	ctx, retryCancel := context.WithTimeout(ctx, spawnTransitionLimit)
	defer retryCancel()
	ticker := time.NewTicker(spawnTransitionPoll)
	defer ticker.Stop()
	pending := err
	for {
		// A dying process can disappear from /proc before tmux marks its pane
		// dead. Only a fresh tmux observation can confirm the pane's exit.
		select {
		case <-ctx.Done():
			return nil, errors.Join(pending, ctx.Err())
		case <-ticker.C:
		}
		info, err = c.paneInfoOnce(ctx, tgt)
		if err != nil && ctx.Err() != nil {
			return nil, errors.Join(err, pending, ctx.Err())
		}
		if err == nil || !paneObservationPending(err) || errors.Is(err, errPaneGroupPending) {
			return info, err
		}
		pending = err
	}
}

func (c *Client) paneInfoOnce(ctx context.Context, tgt string) (*PaneInfo, error) {
	out, err := c.run(ctx, "display-message", "-p", "-t", tgt, "-F", paneFormat)
	if err != nil {
		return nil, err
	}

	parts := strings.Split(out, "\t")
	if len(parts) != paneFieldCount {
		return nil, fmt.Errorf("tmux: expected %d pane fields, got %d in %q",
			paneFieldCount, len(parts), out)
	}

	// display-message does not fail for a target that matches nothing — it
	// succeeds and renders every format as empty. Without this guard the empty
	// record would be parsed as a real pane with pid 0, which is both a wrong
	// answer and, for anything that signals a process group, a dangerous one.
	if parts[0] == "" || parts[2] == "" {
		return nil, fmt.Errorf("%w: target %q resolved to no pane", ErrNoSession, tgt)
	}

	info := &PaneInfo{
		SessionID:      parts[0],
		SessionName:    parts[1],
		PaneID:         parts[2],
		Dead:           parts[4] == "1",
		CurrentCommand: parts[7],
		Marker:         parts[9],
		InMode:         parts[10] == "1",
		LaunchMarker:   parts[11],
	}
	if info.PID, err = strconv.Atoi(parts[3]); err != nil {
		return nil, fmt.Errorf("tmux: unparseable pane_pid %q: %w", parts[3], err)
	}
	// A dead pane reports either an exit status or a signal, never both —
	// observed on tmux 3.6a and relied on by the sessions table's
	// exit-code-xor-signal constraint.
	if parts[5] != "" {
		status, err := strconv.Atoi(parts[5])
		if err != nil {
			return nil, fmt.Errorf("tmux: unparseable pane_dead_status %q: %w", parts[5], err)
		}
		info.ExitStatus = &status
	}
	if parts[6] != "" {
		signal, err := parsePaneSignal(parts[6])
		if err != nil {
			return nil, fmt.Errorf("tmux: unparseable pane_dead_signal %q: %w", parts[6], err)
		}
		info.ExitSignal = &signal
	}
	if parts[8] != "" {
		gen, err := strconv.Atoi(parts[8])
		if err != nil {
			return nil, fmt.Errorf("tmux: unparseable generation marker %q: %w", parts[8], err)
		}
		info.Generation = gen
	}

	// A live pane's process details come from the host kernel. A dead pane has
	// no process, so these stay zero rather than being invented.
	if !info.Dead {
		pgid, ticks, exe, err := processIdentity(info.PID)
		if err != nil {
			// An unreadable process never supplies a successful partial identity.
			return nil, fmt.Errorf(
				"tmux: pane %s reports pid %d but its process identity could not be "+
					"read (it may have just exited): %w", info.PaneID, info.PID, err)
		}
		if pgid != info.PID {
			return nil, fmt.Errorf("%w: pane %s pid %d is still in group %d",
				errPaneGroupPending, info.PaneID, info.PID, pgid)
		}
		info.ProcessGroupID = pgid
		info.StartTicks = ticks
		info.Executable = exe
	}
	return info, nil
}

// SetPaneOption writes a tmux pane option.
func (c *Client) SetPaneOption(ctx context.Context, paneID, key, value string) error {
	if err := ValidatePaneID(paneID); err != nil {
		return err
	}
	if !strings.HasPrefix(key, "@") {
		return fmt.Errorf("tmux: %q is not a user option; it must begin with '@'", key)
	}
	_, err := c.run(ctx, "set-option", "-p", "-t", paneTarget(paneID), key, value)
	return err
}

// GetPaneOption reads a tmux pane option. A missing option reads as "".
func (c *Client) GetPaneOption(ctx context.Context, paneID, key string) (string, error) {
	if err := ValidatePaneID(paneID); err != nil {
		return "", err
	}
	if !strings.HasPrefix(key, "@") {
		return "", fmt.Errorf("tmux: %q is not a user option; it must begin with '@'", key)
	}
	return c.run(ctx, "display-message", "-p", "-t", paneTarget(paneID), "-F", "#{"+key+"}")
}

// DefaultTerminal reports the server's default-terminal option — the TERM
// value tmux gives every pane process. A caller constructing a pane
// environment from scratch must use THIS value, never the service's own
// TERM: the service may run under `dumb` or nothing at all, and a TUI
// launched with that loses the colours, alternate screen, and control-key
// handling it needs.
func (c *Client) DefaultTerminal(ctx context.Context) (string, error) {
	// show-options alone will not start a server, and callers need this
	// value BEFORE the first session exists. start-server on a cold socket
	// applies the server config (run passes -f on every invocation) and
	// exit-empty off keeps the empty server alive; on a warm socket it is a
	// no-op.
	v, err := c.run(ctx, "start-server", ";", "show-options", "-g", "-v", "default-terminal")
	if err != nil {
		return "", fmt.Errorf("tmux: read default-terminal: %w", err)
	}
	if v == "" {
		return "", errors.New("tmux: default-terminal read back empty")
	}
	return v, nil
}
