package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// SessionSpec describes a session to create.
type SessionSpec struct {
	Name    string
	WorkDir string
	// Argv[0] is the executable, run directly as the pane command. tmux
	// must not launch a shell and type the command into it.
	Argv []string
	Env  []string
	// Generation and Marker are written as pane options so a pane can later
	// be proven to be the one we launched.
	Generation int
	Marker     string
	// Cols and Rows size the detached window at creation. Zero leaves the
	// tmux default (80x24) — a size the pane keeps until a client attaches,
	// which lays out every headless line at a width no user is viewing.
	Cols uint16
	Rows uint16
	// RetainPartialOnError transfers rollback ownership to a caller that has a
	// durable action fence. Ordinary launches leave this false and retain the
	// tmux client's immediate rollback behavior.
	RetainPartialOnError bool
}

const launchMarkerEnv = "NOISEDECK_TMUX_LAUNCH_MARKER"

// CreateSession launches a detached session whose pane command is the given
// executable, run directly, and records its identity markers.
func (c *Client) CreateSession(ctx context.Context, spec SessionSpec) (*PaneInfo, error) {
	if err := ValidateSessionName(spec.Name); err != nil {
		return nil, err
	}
	if len(spec.Argv) == 0 {
		return nil, errors.New("tmux: SessionSpec.Argv is required")
	}
	if spec.WorkDir == "" {
		return nil, errors.New("tmux: SessionSpec.WorkDir is required")
	}
	if spec.Generation < 1 {
		return nil, errors.New("tmux: SessionSpec.Generation must be >= 1")
	}
	if spec.Marker == "" {
		return nil, errors.New("tmux: SessionSpec.Marker is required")
	}
	if info, err := os.Stat(spec.WorkDir); err != nil {
		return nil, fmt.Errorf("tmux: working directory %q: %w", spec.WorkDir, err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("tmux: working directory %q is not a directory", spec.WorkDir)
	}

	// Options and creation go in ONE tmux invocation, as an ordered command
	// list. Two invocations leave a window in which a server can appear
	// between them — and a server that already exists ignores `-f`, so the
	// second call would create a session on an unconfigured server. Measured
	// on tmux 3.6a, this form works on a cold socket (tmux starts the server
	// for the list, applying `-f`) and on a warm unconfigured one.
	//
	// `-P -F` returns the created session and pane ids from new-session itself.
	// Resolving them afterwards through a session target would let the provider
	// open a window first and hand us the wrong pane. The launch marker is also
	// stamped in the new session's environment by this physical command, so a
	// later setup failure can identify the pane before its user options exist.
	args := append(c.setOptionArgs(),
		"new-session", "-d", "-P", "-F", "#{session_id}\t#{pane_id}\t#{pane_pid}",
		"-s", spec.Name, "-c", spec.WorkDir,
	)
	if spec.Cols > 0 && spec.Rows > 0 {
		args = append(args, "-x", strconv.Itoa(int(spec.Cols)), "-y", strconv.Itoa(int(spec.Rows)))
	}
	for _, kv := range spec.Env {
		args = append(args, "-e", kv)
	}
	args = append(args, "-e", launchMarkerEnv+"="+spec.Marker)
	// Everything after "--" is the command and its arguments. tmux execs it
	// directly; no shell parses any of it.
	args = append(args, "--")
	args = append(args, spec.Argv...)

	created, runErr := c.run(ctx, args...)
	commandAccepted := runErr == nil
	if commandAccepted && c.testHooks != nil && c.testHooks.afterSpawnCommand != nil {
		runErr = c.testHooks.afterSpawnCommand("create")
	}
	partial, parseErr := createdPaneInfo(created, spec)
	if runErr == nil && parseErr != nil {
		runErr = parseErr
	}
	if runErr != nil {
		partial, observeErr := c.recoverCreatedPane(ctx, spec, partial)
		if partial == nil {
			return nil, errors.Join(runErr, observeErr)
		}
		if spec.RetainPartialOnError {
			return partial, errors.Join(runErr, observeErr)
		}
		return partial, c.rollbackPartialSpawn(ctx, partial, errors.Join(runErr, observeErr))
	}
	paneID := partial.PaneID
	fail := func(cause error) (*PaneInfo, error) {
		if spec.RetainPartialOnError {
			return partial, cause
		}
		return partial, c.rollbackPartialSpawn(ctx, partial, cause)
	}
	if observed, observeErr := c.observeSpawnedPane(ctx, paneID); observeErr == nil {
		partial = preserveSpawnOriginObservation(partial, observed)
	} else {
		return fail(fmt.Errorf("tmux: observe newly created pane: %w", observeErr))
	}
	if c.testHooks != nil && c.testHooks.afterSpawn != nil {
		if err := c.testHooks.afterSpawn("create"); err != nil {
			return fail(err)
		}
	}
	if err := c.verifyServerOptions(ctx); err != nil {
		return fail(err)
	}

	// Markers are set immediately after creation. Unlike remain-on-exit, this
	// is safe against an instantly-exiting command: remain-on-exit is already
	// active, so the pane persists as dead and still accepts option writes.
	if err := c.SetPaneOption(ctx, paneID, OptGeneration, strconv.Itoa(spec.Generation)); err != nil {
		return fail(fmt.Errorf("tmux: record generation marker: %w", err))
	}
	if c.testHooks != nil && c.testHooks.beforeMarkerWrite != nil {
		if err := c.testHooks.beforeMarkerWrite("create"); err != nil {
			return fail(err)
		}
	}
	if err := c.SetPaneOption(ctx, paneID, OptMarker, spec.Marker); err != nil {
		return fail(fmt.Errorf("tmux: record identity marker: %w", err))
	}
	partial.Generation = spec.Generation
	partial.Marker = spec.Marker
	if c.testHooks != nil && c.testHooks.afterMarkers != nil {
		if err := c.testHooks.afterMarkers("create"); err != nil {
			return fail(err)
		}
	}
	info, err := c.observeSpawnedPane(ctx, paneID)
	if err != nil {
		return fail(err)
	}
	return preserveSpawnOriginObservation(partial, info), nil
}

// RespawnPane restarts a dead pane with a new command, and records the new
// generation and marker.
//
// Native resume creates a NEW generation and new identity markers, which
// invalidates every outstanding controller and release fencing token. This
// function records them; enforcing the invalidation is the session layer's
// job.
//
// The setOptionArgs prefix below configures a warm server exactly as
// CreateSession does, but its history-limit element CANNOT change THIS
// pane's scrollback depth: respawn-pane reuses the existing pane, and tmux
// sizes a pane's history once at pane creation (measured on tmux 3.6a — a
// pane born at limit 2000 read 2000 after a respawn under limit 4321). A
// respawned pane keeps its original depth; the option in the prefix governs
// only panes created later on this server.
func (c *Client) RespawnPane(ctx context.Context, paneID string, spec SessionSpec) (*PaneInfo, error) {
	if err := ValidatePaneID(paneID); err != nil {
		return nil, err
	}
	if len(spec.Argv) == 0 {
		return nil, errors.New("tmux: SessionSpec.Argv is required")
	}
	if spec.Generation < 1 {
		return nil, errors.New("tmux: SessionSpec.Generation must be >= 1")
	}
	if spec.Marker == "" {
		return nil, errors.New("tmux: SessionSpec.Marker is required")
	}
	partial, err := c.PaneInfoByID(ctx, paneID)
	if err != nil {
		return nil, fmt.Errorf("tmux: observe pane before respawn: %w", err)
	}
	if partial.Generation == spec.Generation || partial.Marker == spec.Marker {
		return nil, errors.New("tmux: respawn requires a new generation and marker")
	}
	fail := func(cause error) (*PaneInfo, error) {
		return partial, c.rollbackPartialSpawn(ctx, partial, cause)
	}
	// The launch marker travels with the process, not the pane it occupies.
	// It is read back through #{E:...}, which resolves in the SESSION
	// environment, so respawn-pane's own -e cannot republish it: that flag
	// reaches only the spawned process. Without the explicit set-environment a
	// respawned pane answers identity probes with its predecessor's marker.
	args := append(c.setOptionArgs(),
		"set-environment", "-t", partial.SessionName, launchMarkerEnv, spec.Marker,
		";", "respawn-pane", "-t", paneTarget(paneID))
	if spec.WorkDir != "" {
		args = append(args, "-c", spec.WorkDir)
	}
	for _, kv := range spec.Env {
		args = append(args, "-e", kv)
	}
	args = append(args, "-e", launchMarkerEnv+"="+spec.Marker)
	args = append(args, "--")
	args = append(args, spec.Argv...)
	args = append(args,
		";", "set-option", "-p", "-t", paneTarget(paneID), OptMarker, spec.Marker,
		";", "set-option", "-p", "-t", paneTarget(paneID), OptGeneration, strconv.Itoa(spec.Generation),
	)
	if c.testHooks != nil && c.testHooks.spawnArgs != nil {
		args = c.testHooks.spawnArgs("respawn", args)
	}

	out, runErr := c.run(ctx, args...)
	if c.testHooks != nil && c.testHooks.spawnRunResult != nil {
		_, runErr = c.testHooks.spawnRunResult("respawn", out, runErr)
	}
	if runErr == nil && c.testHooks != nil && c.testHooks.afterSpawnCommand != nil {
		runErr = c.testHooks.afterSpawnCommand("respawn")
	}
	if runErr != nil {
		observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
		observed, observeErr := c.PaneInfoByID(observeCtx, paneID)
		cancel()
		if observeErr != nil {
			return nil, errors.Join(runErr,
				fmt.Errorf("tmux: observe pane after respawn error: %w", observeErr))
		}
		observed = preserveProcessIdentity(partial, observed)
		if observed.SessionID != partial.SessionID || observed.PaneID != partial.PaneID {
			return nil, errors.Join(runErr, errors.New("tmux: pane changed while classifying respawn error"))
		}
		if observed.Marker == spec.Marker {
			partial = markSpawnOrigin(observed)
			return fail(runErr)
		}
		if observed.Generation == partial.Generation && observed.Marker == partial.Marker {
			if samePreRespawnState(partial, observed) {
				return nil, runErr
			}
			partial = markSpawnOrigin(observed)
			return fail(errors.Join(runErr,
				errors.New("tmux: respawn changed pane state before publishing its marker")))
		}
		return nil, errors.Join(runErr, fmt.Errorf(
			"tmux: respawn error left unrecognized pane identity gen=%d marker=%q",
			observed.Generation, observed.Marker))
	}
	if observed, observeErr := c.observeSpawnedPane(ctx, paneID); observeErr == nil {
		partial = markSpawnOrigin(observed)
	} else {
		return fail(fmt.Errorf("tmux: observe newly respawned pane: %w", observeErr))
	}
	if c.testHooks != nil && c.testHooks.afterSpawn != nil {
		if err := c.testHooks.afterSpawn("respawn"); err != nil {
			return fail(err)
		}
	}
	if partial.Generation != spec.Generation || partial.Marker != spec.Marker {
		return fail(fmt.Errorf("tmux: respawn identity did not publish: got gen=%d marker=%q",
			partial.Generation, partial.Marker))
	}
	return partial, nil
}

func samePreRespawnState(a, b *PaneInfo) bool {
	return a.SessionID == b.SessionID && a.PaneID == b.PaneID &&
		a.PID == b.PID && a.ProcessGroupID == b.ProcessGroupID &&
		a.StartTicks == b.StartTicks && a.Dead == b.Dead &&
		a.Generation == b.Generation && a.Marker == b.Marker &&
		equalOptionalInt(a.ExitStatus, b.ExitStatus) && equalOptionalInt(a.ExitSignal, b.ExitSignal)
}

func equalOptionalInt(a, b *int) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
