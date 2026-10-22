package tasks

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/project"
	"github.com/noisefloor-oss/deck/internal/provider"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// ErrInvalidPRD reports a user-selected PRD name that is not one of
// the eligible files, as ListPRDFiles decides them.
var ErrInvalidPRD = errors.New("not an eligible PRD file")

// ErrInvalidTaskCount reports a requested task count outside 1 to 50.
var ErrInvalidTaskCount = errors.New("task count must be between 1 and 50")

// ErrPRDExists reports an exclusive-create PRD write whose name is taken:
// an existing PRD is never overwritten by the upload path.
var ErrPRDExists = errors.New("PRD file already exists")

var ErrGenerationUnsupported = errors.New("PRD task generation is not supported on this platform; add tasks manually or use the Tasks CLI")

// Runner executes Taskmaster generation jobs as attachable managed tmux
// sessions with per-project serialization.
//
// The invariants:
//   - FIXED argv: tmux execs the internal descriptor handoff, which execs the
//     configured engine. No shell evaluates either boundary, and runtime
//     package installation is impossible.
//   - The run row is committed BEFORE the tmux session is created, and the
//     partial unique index (idx_taskmaster_one_running) — not application
//     logic — serializes generation per project.
//   - Exit state is recorded only from OBSERVED tmux evidence: a dead pane
//     with its exit status, or a pane provably gone. A watcher dying
//     unobserves nothing — the next Reconcile re-classifies from tmux.
//   - A job is a plain tmux session on the managed namespace; the session
//     reconciler audits it as an unknown pane and leaves it alone, so
//     generation can never disturb agent sessions.
type Runner struct {
	runs       *RunsRepository
	projects   *project.Repository
	tmux       *tmux.Client
	executable string
	namePrefix string
	log        zerolog.Logger

	mu       sync.Mutex
	watchers map[string]context.CancelFunc
	stopped  bool

	accessHooks   taskAccessHooks
	handoffPrefix []string
	providers     map[provider.Name]provider.Descriptor
}

func (r *Runner) WithProviders(providers map[provider.Name]provider.Descriptor) *Runner {
	r.providers = providers
	return r
}

// NewRunner wires the runner. executable is the configured, validated
// absolute noise host path (config taskmaster.executable); sessionPrefix
// namespaces the job sessions alongside provider sessions.
func NewRunner(runs *RunsRepository, projects *project.Repository, t *tmux.Client, executable, sessionPrefix string, log zerolog.Logger) *Runner {
	return &Runner{
		runs:       runs,
		projects:   projects,
		tmux:       t,
		executable: executable,
		namePrefix: sessionPrefix + "tm-",
		log:        log,
		watchers:   make(map[string]context.CancelFunc),
	}
}

// watchPoll is how often a live pane is re-observed; reconcileRetry is how
// often the janitor re-classifies running rows that have no live watcher
// (a server that was unreachable, a watcher that died with a deploy).
const (
	watchPoll      = time.Second
	reconcileRetry = 30 * time.Second
	launchGrace    = 60 * time.Second
)

// Generate starts one task-generation job for the project. The PRD must be one
// of the eligible files exactly as listed for the panel. The run row is
// returned in whatever state the launch reached: a job whose
// process died instantly already carries its observed exit. numTasks 0 asks
// for the engine's default count; otherwise it must be 1 to maxGenerateTasks.
func (r *Runner) Generate(ctx context.Context, projectID, prdName string, numTasks int, harness string) (*Run, error) {
	if numTasks < 0 || numTasks > maxGenerateTasks {
		return nil, fmt.Errorf("%w: %d", ErrInvalidTaskCount, numTasks)
	}
	p, err := r.projects.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, ErrGenerationUnsupported
	}
	prefix := r.handoffPrefix
	if len(prefix) == 0 {
		self, err := generationHandoffExecutable()
		if err != nil {
			return nil, err
		}
		prefix = []string{self, generationHandoffCommand}
	}
	root, err := openEngineTaskRoot(ctx, r.executable, p.CanonicalPath, r.accessHooks.rootAnchored)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// Generation appends to the repository-family store, so a project
	// without one has nothing to generate into; onboarding runs the engine's
	// own setup/init first. A read-only store would fail only after the
	// provider call, so refuse before the run row exists.
	if !root.initialized {
		return nil, ErrNotTaskmasterProject
	}
	if !root.writable {
		return nil, ErrTaskStateReadOnly
	}
	selection, err := r.generationHarness(root, harness)
	if err != nil {
		return nil, err
	}
	prds, err := root.listPRDFiles(taskAccessHooks{})
	if err != nil {
		return nil, err
	}
	eligible := false
	for _, name := range prds {
		if name == prdName {
			eligible = true
			break
		}
	}
	if !eligible {
		return nil, fmt.Errorf("%w: %q", ErrInvalidPRD, prdName)
	}
	prd, err := root.openFile(filepath.Join(root.docsDir, prdName), os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer prd.Close()
	prdInfo, err := prd.Stat()
	if err != nil {
		return nil, fmt.Errorf("taskmaster: stat PRD: %w", err)
	}
	if !prdInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %q is not a regular PRD file", ErrInvalidPRD, prdName)
	}
	if r.accessHooks.handoffReady != nil {
		r.accessHooks.handoffReady()
	}

	id, err := newRunID()
	if err != nil {
		return nil, err
	}
	sessionName := r.namePrefix + id[:8]
	// The engine receives only descriptors retained in its own process: the
	// project root as its working directory (store discovery anchors to it)
	// and the exact already-open PRD. Both remain usable across a service
	// restart.
	argv := generationEngineArgv(r.executable, numTasks, runtime.GOOS)
	run := &Run{
		ID:              id,
		ProjectID:       p.ID,
		Argv:            argv,
		PRD:             &prdName,
		Status:          RunRunning,
		StartedAt:       db.FormatTime(time.Now().UTC()),
		TmuxSessionName: &sessionName,
	}
	// Intent before action: the row exists before the process does, so a
	// crash between the two leaves a classifiable record, and the unique
	// index — not a lock — refuses a concurrent second generation.
	if err := r.runs.CreateRun(ctx, run); err != nil {
		return nil, err
	}
	handoff, err := newHandoffListener(filepath.Dir(r.tmux.SocketPath()), run.ID)
	if err != nil {
		note := err.Error()
		if _, uerr := r.runs.MarkExited(ctx, run.ID, nil, nil, &note, time.Now().UTC()); uerr != nil {
			return nil, errors.Join(err, uerr)
		}
		return nil, err
	}
	defer handoff.Close()
	launchArgv := append([]string{}, prefix...)
	launchArgv = append(launchArgv, r.executable, handoff.path)
	if selection != nil {
		launchArgv = append(launchArgv, strconv.Itoa(numTasks), selection.selector, selection.path)
	} else if numTasks > 0 {
		launchArgv = append(launchArgv, strconv.Itoa(numTasks))
	}

	pane, err := r.tmux.CreateSession(ctx, tmux.SessionSpec{
		Name:       sessionName,
		WorkDir:    "/",
		Argv:       launchArgv,
		Generation: 1,
		Marker:     run.ID,
	})
	if err != nil {
		note := fmt.Sprintf("tmux launch failed: %v", err)
		if _, uerr := r.runs.MarkExited(ctx, run.ID, nil, nil, &note, time.Now().UTC()); uerr != nil {
			return nil, errors.Join(err, uerr)
		}
		return nil, fmt.Errorf("taskmaster: launch: %w", err)
	}
	handoffOK, handoffErr := handoff.await(ctx, root.anchor, prd)
	if handoffErr != nil || !handoffOK {
		if current, observeErr := r.tmux.PaneInfoByID(ctx, pane.PaneID); observeErr == nil && current.Dead {
			if err := r.finalize(ctx, run.ID, pane.PaneID, current); err != nil {
				return nil, err
			}
			return r.runs.GetRun(ctx, run.ID)
		}
		r.startWatcher(run.ID, pane.PaneID)
		if handoffErr != nil {
			return nil, handoffErr
		}
		return nil, errors.New("taskmaster: generation process refused the descriptor handoff")
	}
	current, observeErr := r.tmux.PaneInfoByID(ctx, pane.PaneID)
	if observeErr == nil {
		pane = current
	}
	if pane.Dead {
		// An instantly-failing job (bad executable, bad argv) is not an
		// error of the request: its observed exit IS the answer.
		if err := r.finalize(ctx, run.ID, pane.PaneID, pane); err != nil {
			return nil, err
		}
		return r.runs.GetRun(ctx, run.ID)
	}
	r.startWatcher(run.ID, pane.PaneID)
	return run, nil
}

// Reconcile classifies every run a previous life (or a dead watcher) left
// recorded as running, against observed tmux state: the job record is only
// ever corrected by observation. Boot calls it once; the janitor
// repeats it for rows with no live watcher.
func (r *Runner) Reconcile(ctx context.Context) error {
	running, err := r.runs.ListRunning(ctx)
	if err != nil {
		return err
	}
	for _, run := range running {
		if r.hasWatcher(run.ID) {
			continue
		}
		if err := r.reconcileRun(ctx, run); err != nil {
			r.log.Warn().Err(err).Str("run", run.ID).
				Msg("taskmaster run reconciliation deferred")
		}
	}
	return nil
}

// reconcileRun finds the pane carrying the run's marker — the same join
// the session reconciler uses, never the session's
// CURRENT pane: a split window or a reused current pane says nothing
// about the pane the job was launched in.
func (r *Runner) reconcileRun(ctx context.Context, run *Run) error {
	started, err := time.Parse(db.TimeFormat, run.StartedAt)
	if err != nil {
		return err
	}
	// The run row precedes tmux's marker and the descriptor handoff;
	// classifying it during launch can discard the job's real exit.
	if time.Since(started) < launchGrace {
		return nil
	}
	panes, err := r.tmux.ListPanes(ctx)
	if err != nil {
		// ErrNoServer included: nothing is proven, the row stays running.
		return err
	}
	var mine *tmux.PaneRecord
	for i := range panes {
		if panes[i].Marker == run.ID {
			mine = &panes[i]
			break
		}
	}
	if mine == nil {
		// On a live server, no pane carrying the run's marker means the
		// pane is gone — with it the process, and any chance to observe
		// the exit. Recorded honestly: exited, status unobserved.
		note := "pane gone before exit status was observed"
		_, uerr := r.runs.MarkExited(ctx, run.ID, nil, nil, &note, time.Now().UTC())
		return uerr
	}
	info, err := r.tmux.PaneInfoByID(ctx, mine.PaneID)
	if errors.Is(err, tmux.ErrNoSession) {
		note := "pane gone before exit status was observed"
		_, uerr := r.runs.MarkExited(ctx, run.ID, nil, nil, &note, time.Now().UTC())
		return uerr
	}
	if err != nil {
		return err
	}
	if info.Dead {
		return r.finalize(ctx, run.ID, info.PaneID, info)
	}
	// Alive and provably this run's pane: resume watching.
	r.startWatcher(run.ID, info.PaneID)
	return nil
}

// Start launches the janitor: every reconcileRetry, running rows without a
// live watcher are re-classified from tmux. Watchers start on demand;
// Start exists so a server outage or a crashed watcher cannot strand a row.
func (r *Runner) Start(ctx context.Context) {
	go func() {
		tick := time.NewTicker(reconcileRetry)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if err := r.Reconcile(ctx); err != nil {
					r.log.Warn().Err(err).Msg("taskmaster janitor reconcile failed")
				}
			}
		}
	}()
}

// Shutdown stops every watcher and refuses new ones. Rows left running are
// the next boot's reconciliation set — shutdown unobserves nothing.
func (r *Runner) Shutdown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	for id, cancel := range r.watchers {
		cancel()
		delete(r.watchers, id)
	}
}

func (r *Runner) hasWatcher(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.watchers[id]
	return ok
}

func (r *Runner) startWatcher(runID, paneID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	if _, ok := r.watchers[runID]; ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.watchers[runID] = cancel
	go r.watch(ctx, runID, paneID)
}

func (r *Runner) dropWatcher(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.watchers, runID)
}

// watch polls the pane until the exit is observed and recorded. Context
// cancellation (shutdown) is the only other exit, and it records nothing.
func (r *Runner) watch(ctx context.Context, runID, paneID string) {
	defer r.dropWatcher(runID)
	tick := time.NewTicker(watchPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		done, err := r.checkExit(ctx, runID, paneID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			r.log.Warn().Err(err).Str("run", runID).Msg("taskmaster watcher observation failed")
			continue
		}
		if done {
			return
		}
	}
}

// checkExit performs one observation. done means the run reached a
// recorded terminal state; error means the observation itself failed and
// nothing was recorded.
func (r *Runner) checkExit(ctx context.Context, runID, paneID string) (bool, error) {
	info, err := r.tmux.PaneInfoByID(ctx, paneID)
	switch {
	case errors.Is(err, tmux.ErrNoSession):
		note := "pane gone before its exit status was observed"
		_, uerr := r.runs.MarkExited(ctx, runID, nil, nil, &note, time.Now().UTC())
		return true, uerr
	case errors.Is(err, tmux.ErrNoServer):
		// An unreachable server proves nothing about the process; the
		// janitor re-classifies if the watcher dies first.
		return false, nil
	case err != nil:
		return false, err
	}
	if info.Marker != runID {
		// The pane id is exact, so a marker change means tampering or a
		// respawn: this pane's future exit is no longer provably THIS
		// run's, and nothing else is provable either. Record nothing,
		// stop watching — the janitor's marker join re-classifies (no
		// pane carrying the marker = gone).
		r.log.Warn().Str("run", runID).Str("pane", paneID).
			Msg("taskmaster pane marker changed; watcher withdrawn, row left running for reconcile")
		return true, nil
	}
	if !info.Dead {
		return false, nil
	}
	return true, r.finalize(ctx, runID, paneID, info)
}

// finalize records an observed dead pane: exit status (or the honest
// absence of one), the captured scrollback as stdout, then the corpse
// pane is killed — the database row is now the only evidence needed.
//
// stdout/stderr: a terminal merges the streams by definition, so the
// capture is the merged output recorded as stdout; stderr carries only
// runner-side notes about missing evidence. The report keeps both fields
// so the API never pretends a separation a PTY does not have.
func (r *Runner) finalize(ctx context.Context, runID, paneID string, info *tmux.PaneInfo) error {
	info, replaced := r.awaitExitEvidence(ctx, paneID, info)
	var output string
	var captureErr error
	if !replaced {
		output, captureErr = r.tmux.CapturePaneHistory(ctx, paneID)
		// A respawn just before the capture puts the replacement's output in it.
		replaced = r.paneReplaced(ctx, paneID, info)
	}
	if replaced {
		// The pane now belongs to another process: its output and its
		// process are not this run's to record or to kill.
		r.log.Warn().Str("run", runID).Str("pane", paneID).
			Msg("taskmaster pane replaced before its exit status was observed; replacement left alone")
		note := "pane replaced before its exit status was observed"
		_, err := r.runs.MarkExited(ctx, runID, nil, nil, &note, time.Now().UTC())
		return err
	}
	exit := info.ExitStatus
	var stdout, stderr *string
	if captureErr != nil {
		r.log.Warn().Err(captureErr).Str("run", runID).Msg("taskmaster output capture failed")
		note := "exit observed but output capture failed"
		stderr = &note
	} else {
		stdout = &output
	}
	if exit == nil {
		var note string
		if info.ExitSignal != nil {
			note = fmt.Sprintf("terminated by signal %d; no exit status", *info.ExitSignal)
		} else {
			note = "pane died without an observable exit status"
		}
		stderr = &note
	}
	if _, err := r.runs.MarkExited(ctx, runID, exit, stdout, stderr, time.Now().UTC()); err != nil {
		return err
	}
	// A dead pane has no start time left to compare; its pid and marker
	// are the identity, checked by tmux in the same command as the kill.
	killed, err := r.tmux.KillPaneIfIdentity(ctx, tmux.PaneIdentity{
		SessionID: info.SessionID, PaneID: paneID, PanePID: info.PID,
		Generation: info.Generation, Marker: runID,
	})
	if err != nil || !killed {
		// The evidence is recorded; a lingering corpse is reconciler-audit
		// noise, not a correctness problem.
		r.log.Warn().Err(err).Str("run", runID).Bool("identity_matched", killed).
			Msg("taskmaster corpse pane kill skipped or failed")
	}
	return nil
}

// awaitExitEvidence gives tmux a moment to reap a pane it has just marked
// dead: tmux reports a pane dead when its pty closes and fills in the exit
// status only once it reaps the process. replaced reports that the pane
// was respawned meanwhile.
func (r *Runner) awaitExitEvidence(ctx context.Context, paneID string, dead *tmux.PaneInfo) (_ *tmux.PaneInfo, replaced bool) {
	for i := 0; i < 10 && dead.ExitStatus == nil && dead.ExitSignal == nil; i++ {
		select {
		case <-ctx.Done():
			return dead, false
		case <-time.After(100 * time.Millisecond):
		}
		again, err := r.tmux.PaneInfoByID(ctx, paneID)
		if err != nil {
			return dead, false
		}
		if !sameCorpse(dead, again) {
			return dead, true
		}
		dead = again
	}
	return dead, false
}

// paneReplaced re-reads the pane; an unreadable pane proves no respawn.
func (r *Runner) paneReplaced(ctx context.Context, paneID string, dead *tmux.PaneInfo) bool {
	again, err := r.tmux.PaneInfoByID(ctx, paneID)
	return err == nil && !sameCorpse(dead, again)
}

// sameCorpse reports whether again still shows the dead process first
// observed: a respawn leaves the pane live, or dead under a new pid.
func sameCorpse(dead, again *tmux.PaneInfo) bool {
	return again.Dead && again.SessionID == dead.SessionID && again.PID == dead.PID &&
		again.Generation == dead.Generation && again.Marker == dead.Marker
}

// newRunID returns 16 random bytes as hex — collision-free without a
// dependency, and safe as a tmux session-name component.
func newRunID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("taskmaster: generate run id: %w", err)
	}
	return fmt.Sprintf("%x", b), nil
}
