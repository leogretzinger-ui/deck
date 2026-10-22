// Package lane opens and closes task lanes for the tutorial: one task, its
// own branch and worktree, driven through the pinned Tasks engine with the
// same claim, run and worktree sequence an agent follows by hand. The
// fencing tokens the engine returns stay in the control database and never
// reach a response, a log line or the browser.
package lane

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/project"
	"github.com/noisefloor-oss/deck/internal/taskengine"
)

const (
	// Owner is the claim and run owner identity of every lane Deck opens.
	Owner = "deck-tutorial"
	// LeaseTTL is how long a claim and a run attempt stay valid between
	// renewals.
	LeaseTTL = time.Hour
	// operationTimeout bounds one start or close, which finishes even if the
	// browser that asked goes away halfway.
	operationTimeout = 5 * time.Minute
)

// Projects is the project registry the lane reads and extends.
type Projects interface {
	Get(ctx context.Context, id string) (*project.Project, error)
	List(ctx context.Context) ([]*project.Project, error)
	Register(ctx context.Context, name, path string, allowedRoots []string, requested project.GitIdentity) (*project.Project, *project.ReplacedGitIdentity, error)
	LiveSessions(ctx context.Context, projectID string) ([]project.LiveSession, error)
}

// Config wires a Service.
type Config struct {
	Store    *Store
	Projects Projects
	// Engine returns the engine for one repository family and tag.
	Engine func(family, tag string) taskengine.Engine
	Git    Git
	GH     GH
	// RemoveProject unregisters a project and stops its sessions.
	RemoveProject func(ctx context.Context, id string) error
	// StopSessions stops and removes a project's sessions and leaves the
	// project registered.
	StopSessions func(ctx context.Context, id string) error
	// HoldSessions refuses new sessions in a project until the returned
	// function is called, once those already being created have been recorded.
	HoldSessions func(id string) (release func())
	AllowedRoots []string
	Log          zerolog.Logger
	Now          func() time.Time
	// RenewEvery is how long a lane waits between renewals.
	RenewEvery time.Duration
}

// Service owns the lane lifecycle.
type Service struct {
	cfg   Config
	locks sync.Map
}

func NewService(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RenewEvery <= 0 {
		cfg.RenewEvery = LeaseTTL / 6
	}
	if cfg.HoldSessions == nil {
		cfg.HoldSessions = func(string) func() { return func() {} }
	}
	if cfg.Git == nil {
		cfg.Git = execGit{}
	}
	if cfg.GH == nil {
		cfg.GH = execGH
	}
	return &Service{cfg: cfg}
}

var (
	// ErrInvalid reports a request the lane cannot act on.
	ErrInvalid = errors.New("lane: invalid request")
	// ErrNotReady reports a lane that is still being set up or is closing.
	ErrNotReady = errors.New("lane: the lane is not ready")
	// ErrClaimed reports a task another owner holds.
	ErrClaimed = errors.New("lane: the task is claimed by someone else")
	// ErrStale reports a task that changed after the caller read it.
	ErrStale = errors.New("lane: the task changed since it was read")
	// ErrSameAgent reports a review asked of the agent that wrote the change.
	ErrSameAgent = errors.New("a review needs a different agent than the one that wrote the change")
	// ErrNotOpen reports a task that can no longer start a lane.
	ErrNotOpen = errors.New("this task cannot start a lane")
)

// StepError names the step a start or close stopped at.
type StepError struct {
	Step string
	Err  error
}

func (e *StepError) Error() string { return "could not " + e.Step + ": " + e.Err.Error() }
func (e *StepError) Unwrap() error { return e.Err }

// WorkAtRiskError refuses a close that would lose work.
type WorkAtRiskError struct {
	Dirty  int
	Detail string
}

func (e *WorkAtRiskError) Error() string {
	if e.Dirty > 0 {
		return fmt.Sprintf("the worktree has %d uncommitted %s; commit and push them first", e.Dirty, plural(e.Dirty, "file", "files"))
	}
	return "the worktree has work that is not pushed: " + e.Detail + "; push it first"
}

// SessionsLiveError refuses a close while sessions still run in the lane.
type SessionsLiveError struct{ Labels []string }

func (e *SessionsLiveError) Error() string {
	return "these sessions are still running in the lane: " + strings.Join(e.Labels, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// View is a lane as the browser sees it. It carries no capability.
type View struct {
	Tag             string `json:"tag"`
	Task            int64  `json:"task"`
	Agent           string `json:"agent"`
	ProjectID       string `json:"project_id"`
	LaneProjectID   string `json:"lane_project_id"`
	Branch          string `json:"branch"`
	Phase           string `json:"phase"`
	FailedStep      string `json:"failed_step,omitempty"`
	ReviewRequested bool   `json:"review_requested"`
	ReviewAgent     string `json:"review_agent,omitempty"`
	// ReviewRequestedAt identifies one review request; a later request is a
	// different review.
	ReviewRequestedAt string `json:"review_requested_at,omitempty"`
}

func viewOf(l *Lane) *View {
	requestedAt := ""
	if !l.ReviewRequestedAt.IsZero() {
		requestedAt = l.ReviewRequestedAt.UTC().Format(time.RFC3339Nano)
	}
	return &View{
		ReviewRequestedAt: requestedAt,
		Tag:               l.Tag, Task: l.TaskID, Agent: l.Agent, ProjectID: l.ProjectID,
		LaneProjectID: l.LaneProjectID, Branch: l.Branch, Phase: l.Phase,
		FailedStep:      l.FailedStep,
		ReviewRequested: !l.ReviewRequestedAt.IsZero(), ReviewAgent: l.ReviewAgent,
	}
}

func (s *Service) lock(projectID, tag string, taskID int64) func() {
	key := projectID + "\x00" + tag + "\x00" + strconv.FormatInt(taskID, 10)
	m, _ := s.locks.LoadOrStore(key, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func requestID(l *Lane, step string) string {
	return fmt.Sprintf("deck-lane-%d-%s", l.ID, step)
}

// List reads the lanes recorded for a project.
func (s *Service) List(ctx context.Context, projectID string) ([]*View, error) {
	if _, err := s.cfg.Projects.Get(ctx, projectID); err != nil {
		return nil, err
	}
	lanes, err := s.cfg.Store.ListProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]*View, 0, len(lanes))
	for _, l := range lanes {
		out = append(out, viewOf(l))
	}
	return out, nil
}

// StartInput names the task a lane is for and the agent that will write in it.
type StartInput struct {
	Tag    string
	TaskID int64
	Agent  string
}

func (in StartInput) valid() error {
	if strings.TrimSpace(in.Tag) == "" || in.TaskID <= 0 || strings.TrimSpace(in.Agent) == "" {
		return fmt.Errorf("%w: a tag, a task and an agent are required", ErrInvalid)
	}
	return nil
}

// Start opens the lane for a task, or returns the one already open. It runs
// the engine sequence: claim, mark in progress, run start, worktree
// provision; then registers the worktree as a project. A failed step is
// undone as far as the engine allows and named in the error.
func (s *Service) Start(parent context.Context, projectID string, in StartInput) (*View, error) {
	if err := in.valid(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), operationTimeout)
	defer cancel()
	unlock := s.lock(projectID, in.Tag, in.TaskID)
	defer unlock()

	p, err := s.cfg.Projects.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	l, err := s.cfg.Store.Get(ctx, projectID, in.Tag, in.TaskID)
	switch {
	case err == nil && l.Phase == PhaseOpen:
		return viewOf(l), nil
	case err == nil && l.Phase == PhaseRemoved:
		return nil, ErrNotReady
	case err == nil && l.Phase == PhaseFailed:
		if rbErr := s.rollback(ctx, p, l); rbErr != nil {
			return nil, &StepError{Step: "clean up the earlier attempt", Err: rbErr}
		}
		l = nil
	case errors.Is(err, ErrNoLane):
		l = nil
	case err != nil:
		return nil, err
	}
	if l == nil {
		now := s.cfg.Now()
		l = &Lane{ProjectID: projectID, Tag: in.Tag, TaskID: in.TaskID, Agent: in.Agent, RenewedAt: now, CreatedAt: now}
		if err := s.cfg.Store.Create(ctx, l); err != nil {
			if errors.Is(err, ErrSourceGone) {
				return nil, project.ErrNotFound
			}
			return nil, err
		}
	}
	if err := s.advance(ctx, p, l); err != nil {
		return nil, err
	}
	return viewOf(l), nil
}

// advance runs whichever steps the lane has not completed. A lane found
// half-started after a restart resumes where it stopped.
func (s *Service) advance(ctx context.Context, p *project.Project, l *Lane) error {
	eng := s.cfg.Engine(p.CanonicalPath, l.Tag)
	step := func(name string, run func() error) error {
		if err := run(); err != nil {
			return s.fail(ctx, p, l, name, err)
		}
		return nil
	}
	if err := step("prepare the repository", func() error {
		return s.cfg.Git.EnsureWorktreesIgnored(ctx, p.CanonicalPath)
	}); err != nil {
		return err
	}
	if l.ClaimToken == 0 {
		// The revision the claim asks for is stored before the command is
		// sent, so a claim whose response was lost is replayed with the same
		// inputs, here or after a restart.
		if l.PendingOp != "claim" {
			var task taskengine.Task
			if err := step("read the task", func() (err error) {
				task, err = eng.Show(ctx, l.TaskID)
				if err != nil {
					return err
				}
				if task.Status != "pending" && task.Status != "in-progress" {
					return fmt.Errorf("%w: its status is %s", ErrNotOpen, task.Status)
				}
				return nil
			}); err != nil {
				return err
			}
			l.PendingOp, l.PendingRequestID, l.PendingRevision, l.PriorStatus = "claim", requestID(l, "claim"), task.Revision, task.Status
			if err := s.save(ctx, l); err != nil {
				return err
			}
		}
		var receipt taskengine.ClaimReceipt
		if err := step("claim the task", func() error {
			return replayOnce(func() (err error) {
				receipt, err = eng.Claim(ctx, l.TaskID, l.PendingRevision, l.PendingRequestID)
				return err
			})
		}); err != nil {
			return err
		}
		l.ClaimToken, l.Phase = receipt.Token, PhaseClaimed
		l.PendingOp, l.PendingRequestID, l.PendingRevision = "", "", 0
		if err := s.save(ctx, l); err != nil {
			return err
		}
	}
	if !l.StatusDone {
		if err := step("mark the task in progress", func() error {
			task, err := eng.Show(ctx, l.TaskID)
			if err != nil {
				return err
			}
			if task.Status == "in-progress" {
				return nil
			}
			return eng.SetStatus(ctx, l.TaskID, l.ClaimToken, task.Revision, "in-progress")
		}); err != nil {
			return err
		}
		l.StatusDone = true
		if err := s.save(ctx, l); err != nil {
			return err
		}
	}
	if l.RunID == 0 {
		var run taskengine.StartRunReceipt
		if err := step("start the run", func() error {
			return replayOnce(func() (err error) {
				run, err = eng.StartRun(ctx, l.TaskID, l.ClaimToken, requestID(l, "run"), LeaseTTL)
				return err
			})
		}); err != nil {
			return err
		}
		l.RunID, l.Attempt, l.AttemptToken, l.Phase = run.RunID, run.Attempt, run.AttemptToken, PhaseRunning
		l.RenewedAt = s.cfg.Now()
		if err := s.save(ctx, l); err != nil {
			return err
		}
	}
	if l.Branch == "" {
		var tree taskengine.WorktreeReceipt
		if err := step("create the worktree", func() error {
			return replayOnce(func() (err error) {
				tree, err = eng.ProvisionWorktree(ctx, l.RunID, l.ClaimToken, l.Attempt, l.AttemptToken, requestID(l, "worktree"))
				if err == nil && (tree.Status != "bound" || tree.Path == "" || tree.Branch == "") {
					err = errors.New("the engine did not confirm the worktree")
				}
				return err
			})
		}); err != nil {
			return err
		}
		l.Branch, l.WorktreePath, l.BaseCommit, l.Phase = tree.Branch, tree.Path, tree.BaseCommit, PhaseBound
		if err := s.save(ctx, l); err != nil {
			return err
		}
	}
	if l.LaneProjectID == "" {
		var lane *project.Project
		if err := step("register the worktree as a project", func() (err error) {
			lane, err = s.register(ctx, p, l)
			return err
		}); err != nil {
			return err
		}
		l.LaneProjectID, l.Phase = lane.ID, PhaseOpen
		if err := s.save(ctx, l); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) save(ctx context.Context, l *Lane) error {
	if err := s.cfg.Store.Save(ctx, l); err != nil {
		return &StepError{Step: "record the lane", Err: err}
	}
	return nil
}

func (s *Service) register(ctx context.Context, source *project.Project, l *Lane) (*project.Project, error) {
	name := fmt.Sprintf("%s (task %d)", source.Name, l.TaskID)
	lane, _, err := s.cfg.Projects.Register(ctx, name, l.WorktreePath, s.cfg.AllowedRoots,
		project.GitIdentity{Name: source.GitAuthorName, Email: source.GitAuthorEmail})
	if errors.Is(err, project.ErrDuplicatePath) {
		all, listErr := s.cfg.Projects.List(ctx)
		if listErr != nil {
			return nil, listErr
		}
		canonical, canonErr := project.Canonicalize(l.WorktreePath)
		if canonErr != nil {
			return nil, canonErr
		}
		for _, candidate := range all {
			if candidate.CanonicalPath == canonical {
				return candidate, nil
			}
		}
	}
	return lane, err
}

// refused reports an answer from the engine that says the command did not
// commit: it prints a structured refusal, which an unreachable engine or a
// lost response never does.
func refused(err error) bool {
	return err != nil && (errors.Is(err, taskengine.ErrClaimConflict) || engineKind(err, ""))
}

// replayOnce sends a startup command again after an error, with the same
// request id and inputs. The engine answers a repeated request with the
// receipt of the one that committed, so a lost response is recovered; a
// refusal is final and not repeated.
func replayOnce(send func() error) error {
	err := send()
	if err == nil || refused(err) {
		return err
	}
	return send()
}

// fail records the step that stopped a start, undoes what it can, and
// returns the error for the caller.
func (s *Service) fail(ctx context.Context, p *project.Project, l *Lane, step string, cause error) error {
	cause = classify(cause)
	l.Phase, l.FailedStep = PhaseFailed, step
	if saveErr := s.cfg.Store.Save(ctx, l); saveErr != nil {
		return &StepError{Step: step, Err: cause}
	}
	if err := s.rollback(ctx, p, l); err != nil {
		return &StepError{Step: step, Err: fmt.Errorf("%w. Cleaning up also failed (%w); the task stays claimed until its lease runs out, and noise tasks hygiene reports it", cause, err)}
	}
	if errors.Is(cause, ErrClaimed) || errors.Is(cause, ErrNotOpen) {
		return cause
	}
	return &StepError{Step: step, Err: cause}
}

// rollback undoes a failed start in reverse order. Each completed undo is
// recorded, so a second attempt continues where the first stopped. A
// worktree that holds work is never removed.
func (s *Service) rollback(ctx context.Context, p *project.Project, l *Lane) error {
	eng := s.cfg.Engine(p.CanonicalPath, l.Tag)
	if err := s.settleRenewal(ctx, p, l); err != nil {
		return err
	}
	if err := s.reconcile(ctx, p, l); err != nil {
		return err
	}
	if l.LaneProjectID != "" {
		if err := s.cfg.RemoveProject(ctx, l.LaneProjectID); err != nil && !errors.Is(err, project.ErrNotFound) {
			return err
		}
		l.LaneProjectID = ""
		if err := s.cfg.Store.Save(ctx, l); err != nil {
			return err
		}
	}
	if l.Branch != "" {
		state, err := s.cfg.Git.Inspect(ctx, l.WorktreePath, l.Branch, l.BaseCommit)
		if err != nil {
			return err
		}
		if state.Dirty > 0 || state.Unpushed {
			return fmt.Errorf("the worktree at %s holds work, so it was left in place", l.WorktreePath)
		}
		if err := eng.RemoveWorktree(ctx, l.RunID, l.ClaimToken, l.Attempt, l.AttemptToken, requestID(l, "undo-worktree")); err != nil {
			return err
		}
		// The engine refuses to provision a task whose branch exists, so an
		// abandoned branch that holds nothing is removed to make a retry possible.
		if err := s.cfg.Git.DeleteBranch(ctx, p.CanonicalPath, l.Branch, false); err != nil {
			s.cfg.Log.Warn().Err(err).Msg("abandoned lane branch was kept")
		}
		l.Branch, l.WorktreePath, l.BaseCommit = "", "", ""
		if err := s.cfg.Store.Save(ctx, l); err != nil {
			return err
		}
	}
	if l.RunID != 0 {
		if err := eng.CancelRun(ctx, l.RunID, l.ClaimToken, l.Attempt, l.AttemptToken, requestID(l, "undo-run")); err != nil {
			return err
		}
		l.RunID, l.Attempt, l.AttemptToken = 0, 0, 0
		if err := s.cfg.Store.Save(ctx, l); err != nil {
			return err
		}
	}
	if l.ClaimToken != 0 {
		// A release or a status write may have committed with its response
		// lost, and a dead token is refused forever. The engine's own view of
		// the claim decides what is left to do: a claim held by anyone else, or
		// none, is not ours to release.
		task, err := eng.Show(ctx, l.TaskID)
		if err != nil {
			return err
		}
		ours := task.Claim != nil && task.Claim.Owner == Owner
		if l.PriorStatus != "" && l.PriorStatus != "in-progress" && task.Status == "in-progress" && (ours || task.Claim == nil) {
			token := int64(0)
			if ours {
				token = l.ClaimToken
			}
			if err := eng.SetStatus(ctx, l.TaskID, token, task.Revision, l.PriorStatus); err != nil {
				return err
			}
			if task, err = eng.Show(ctx, l.TaskID); err != nil {
				return err
			}
		}
		if task.Claim != nil && task.Claim.Owner == Owner {
			if err := eng.Release(ctx, l.TaskID, l.ClaimToken, task.Revision); err != nil {
				return err
			}
		}
		l.ClaimToken, l.PriorStatus, l.StatusDone = 0, "", false
		if err := s.cfg.Store.Save(ctx, l); err != nil {
			return err
		}
	}
	return s.cfg.Store.Delete(ctx, l.ID)
}

// reconcile learns what a failed startup command did. A command whose
// response never arrived may have committed, and undoing a start without its
// tokens is impossible, so the same command is sent again with its stored
// request id and inputs and whatever comes back is recorded. A final refusal
// means nothing was committed. Any other failure leaves the lane recorded for
// the next attempt.
func (s *Service) reconcile(ctx context.Context, p *project.Project, l *Lane) error {
	eng := s.cfg.Engine(p.CanonicalPath, l.Tag)
	switch {
	case l.ClaimToken == 0 && l.PendingOp == "claim":
		receipt, err := eng.Claim(ctx, l.TaskID, l.PendingRevision, l.PendingRequestID)
		switch {
		case err == nil:
			l.ClaimToken = receipt.Token
		case refused(err):
		default:
			return err
		}
		l.PendingOp, l.PendingRequestID, l.PendingRevision = "", "", 0
		return s.cfg.Store.Save(ctx, l)
	case l.ClaimToken != 0 && l.RunID == 0 && l.FailedStep == "start the run":
		run, err := eng.StartRun(ctx, l.TaskID, l.ClaimToken, requestID(l, "run"), LeaseTTL)
		if refused(err) {
			return nil
		}
		if err != nil {
			return err
		}
		l.RunID, l.Attempt, l.AttemptToken = run.RunID, run.Attempt, run.AttemptToken
		return s.cfg.Store.Save(ctx, l)
	case l.RunID != 0 && l.Branch == "" && l.FailedStep == "create the worktree":
		tree, err := eng.ProvisionWorktree(ctx, l.RunID, l.ClaimToken, l.Attempt, l.AttemptToken, requestID(l, "worktree"))
		if refused(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if tree.Status == "bound" && tree.Path != "" && tree.Branch != "" {
			l.Branch, l.WorktreePath, l.BaseCommit = tree.Branch, tree.Path, tree.BaseCommit
			return s.cfg.Store.Save(ctx, l)
		}
	}
	return nil
}

// classify turns the engine's refusals into the errors callers match on.
func classify(err error) error {
	switch {
	case errors.Is(err, taskengine.ErrClaimConflict):
		return ErrClaimed
	case engineKind(err, "stale-write"):
		return ErrStale
	}
	return err
}

// engineKind reports whether the engine refused with this structured kind.
// The engine pretty-prints its refusals, so whitespace must not matter.
func engineKind(err error, kind string) bool {
	compact := strings.NewReplacer(" ", "", "\t", "", "\n", "").Replace(err.Error())
	return strings.Contains(compact, `kind":"`+kind)
}

// DoneInput names the task to finish.
type DoneInput struct {
	Tag    string
	TaskID int64
	// Revision is the task revision the caller saw. When set, a task that has
	// moved on is refused. It applies to a task without a lane.
	Revision *int64
	// StopSessions allows the close to stop the sessions running in the lane.
	StopSessions bool
}

// DoneResult says what closing did.
type DoneResult struct {
	Status string `json:"status"`
	// LaneClosed is set when a lane was removed with the task.
	LaneClosed bool   `json:"lane_closed"`
	Branch     string `json:"branch,omitempty"`
	// BranchKept is set when the local branch stayed in the repository because
	// Git could not show it was safe to delete.
	BranchKept bool `json:"branch_kept,omitempty"`
	// Warning names a leftover that did not stop the close.
	Warning string `json:"warning,omitempty"`
}

// Done marks a task done. A task without a lane gets a fenced status write.
// A task with a lane is closed in the order the task workflow requires: the
// worktree is removed under the lane's tokens, then the run finishes with the
// task status in the same engine transaction. It refuses, changing nothing,
// while the worktree holds uncommitted or unpushed work.
func (s *Service) Done(parent context.Context, projectID string, in DoneInput) (*DoneResult, error) {
	if strings.TrimSpace(in.Tag) == "" || in.TaskID <= 0 {
		return nil, fmt.Errorf("%w: a tag and a task are required", ErrInvalid)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), operationTimeout)
	defer cancel()
	unlock := s.lock(projectID, in.Tag, in.TaskID)
	defer unlock()

	p, err := s.cfg.Projects.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	l, err := s.cfg.Store.Get(ctx, projectID, in.Tag, in.TaskID)
	if errors.Is(err, ErrNoLane) {
		return s.markDone(ctx, p, in)
	}
	if err != nil {
		return nil, err
	}
	if l.Phase != PhaseOpen && l.Phase != PhaseRemoved {
		return nil, ErrNotReady
	}
	return s.closeLane(ctx, p, l, in)
}

func (s *Service) markDone(ctx context.Context, p *project.Project, in DoneInput) (*DoneResult, error) {
	eng := s.cfg.Engine(p.CanonicalPath, in.Tag)
	task, err := eng.Show(ctx, in.TaskID)
	if err != nil {
		return nil, &StepError{Step: "read the task", Err: err}
	}
	if task.Status == "done" {
		return &DoneResult{Status: "done"}, nil
	}
	if in.Revision != nil && *in.Revision != task.Revision {
		return nil, ErrStale
	}
	if err := eng.SetStatus(ctx, in.TaskID, 0, task.Revision, "done"); err != nil {
		if cause := classify(err); errors.Is(cause, ErrStale) {
			return nil, cause
		}
		return nil, &StepError{Step: "mark the task done", Err: err}
	}
	return &DoneResult{Status: "done"}, nil
}

func (s *Service) closeLane(ctx context.Context, p *project.Project, l *Lane, in DoneInput) (*DoneResult, error) {
	eng := s.cfg.Engine(p.CanonicalPath, l.Tag)
	if err := s.settleRenewal(ctx, p, l); err != nil {
		return nil, &StepError{Step: "renew the lease", Err: err}
	}
	if l.LaneProjectID != "" {
		// No session may start in the lane while it closes, so the check for
		// live sessions below stays true until the worktree is gone.
		defer s.cfg.HoldSessions(l.LaneProjectID)()
	}
	var state WorktreeState
	if l.Phase == PhaseOpen {
		var err error
		if state, err = s.inspectForClose(ctx, l); err != nil {
			return nil, err
		}
		live, err := s.cfg.Projects.LiveSessions(ctx, l.LaneProjectID)
		if err != nil {
			return nil, err
		}
		if len(live) > 0 {
			if !in.StopSessions {
				return nil, &SessionsLiveError{Labels: sessionLabels(live)}
			}
			// Agents write until they stop, so they stop before the worktree
			// is looked at for the last time and before it can be deleted.
			if err := s.cfg.StopSessions(ctx, l.LaneProjectID); err != nil {
				return nil, &StepError{Step: "stop the lane's sessions", Err: err}
			}
			if state, err = s.inspectForClose(ctx, l); err != nil {
				return nil, err
			}
			if again, err := s.cfg.Projects.LiveSessions(ctx, l.LaneProjectID); err != nil {
				return nil, err
			} else if len(again) > 0 {
				return nil, &SessionsLiveError{Labels: sessionLabels(again)}
			}
		}
		if err := eng.RemoveWorktree(ctx, l.RunID, l.ClaimToken, l.Attempt, l.AttemptToken, requestID(l, "remove")); err != nil {
			return nil, &StepError{Step: "remove the worktree", Err: err}
		}
		l.Phase = PhaseRemoved
		if err := s.save(ctx, l); err != nil {
			return nil, err
		}
	}
	if l.PendingOp != "finish" {
		task, err := eng.Show(ctx, l.TaskID)
		if err != nil {
			return nil, &StepError{Step: "read the task", Err: err}
		}
		l.PendingOp, l.PendingRequestID, l.PendingRevision = "finish", requestID(l, "finish"), task.Revision
		if err := s.save(ctx, l); err != nil {
			return nil, err
		}
	}
	if err := eng.FinishRun(ctx, l.RunID, l.ClaimToken, l.Attempt, l.AttemptToken, "succeeded", "done", l.PendingRevision, l.PendingRequestID); err != nil {
		return nil, &StepError{Step: "finish the run", Err: classify(err)}
	}
	result := &DoneResult{Status: "done", LaneClosed: true, Branch: l.Branch}
	if err := s.cfg.Git.DeleteBranch(ctx, p.CanonicalPath, l.Branch, state.Synced); err != nil {
		result.BranchKept = true
	}
	if l.LaneProjectID != "" {
		if err := s.cfg.RemoveProject(ctx, l.LaneProjectID); err != nil && !errors.Is(err, project.ErrNotFound) {
			s.cfg.Log.Warn().Err(err).Msg("lane project could not be removed after the lane closed")
			result.Warning = "The lane's project is still in the sidebar. Remove it from its row menu."
		}
	}
	if err := s.cfg.Store.Delete(ctx, l.ID); err != nil {
		return nil, err
	}
	return result, nil
}

func sessionLabels(live []project.LiveSession) []string {
	labels := make([]string, 0, len(live))
	for _, session := range live {
		labels = append(labels, session.Label)
	}
	return labels
}

func (s *Service) inspectForClose(ctx context.Context, l *Lane) (WorktreeState, error) {
	state, err := s.cfg.Git.Inspect(ctx, l.WorktreePath, l.Branch, l.BaseCommit)
	if err != nil {
		return state, &StepError{Step: "inspect the worktree", Err: err}
	}
	if state.Dirty > 0 || state.Unpushed {
		return state, &WorkAtRiskError{Dirty: state.Dirty, Detail: state.Detail}
	}
	return state, nil
}

// RenewDue renews the lease of every lane whose last renewal is old enough,
// and finishes any renewal an earlier attempt left unrecorded.
func (s *Service) RenewDue(ctx context.Context) {
	lanes, err := s.cfg.Store.ListRenewable(ctx)
	if err != nil {
		s.cfg.Log.Warn().Err(err).Msg("lane renewal could not list lanes")
		return
	}
	for _, l := range lanes {
		if l.PendingOp != "renew" && s.cfg.Now().Sub(l.RenewedAt) < s.cfg.RenewEvery {
			continue
		}
		s.renew(ctx, l.ProjectID, l.Tag, l.TaskID)
	}
}

func (s *Service) renew(ctx context.Context, projectID, tag string, taskID int64) {
	unlock := s.lock(projectID, tag, taskID)
	defer unlock()
	l, err := s.cfg.Store.Get(ctx, projectID, tag, taskID)
	if err != nil || l.RunID == 0 || l.Phase == PhaseFailed || l.PendingOp == "finish" {
		return
	}
	log := s.cfg.Log.With().Int64("task", taskID).Int64("run", l.RunID).Logger()
	p, err := s.cfg.Projects.Get(ctx, projectID)
	if err != nil {
		log.Warn().Err(err).Msg("lane renewal skipped: project unavailable")
		return
	}
	if err := s.renewLocked(ctx, p, l); err != nil {
		log.Warn().Err(err).Msg("lane renewal failed")
	}
}

// settleRenewal completes a renewal that may have committed without being
// recorded, because the stored tokens are dead if it did.
func (s *Service) settleRenewal(ctx context.Context, p *project.Project, l *Lane) error {
	if l.PendingOp != "renew" {
		return nil
	}
	return s.renewLocked(ctx, p, l)
}

// renewLocked renews under the lane's lock. The request id is stored before
// the command runs and kept until the rotated tokens are stored, so a retry
// replays the same command with the same inputs and gets the same receipt.
func (s *Service) renewLocked(ctx context.Context, p *project.Project, l *Lane) error {
	now := s.cfg.Now()
	if l.PendingOp != "renew" {
		l.PendingOp, l.PendingRequestID = "renew", fmt.Sprintf("deck-lane-%d-renew-%d", l.ID, now.UnixNano())
		if err := s.cfg.Store.Save(ctx, l); err != nil {
			return err
		}
	}
	receipt, err := s.cfg.Engine(p.CanonicalPath, l.Tag).RenewRun(ctx, l.RunID, l.ClaimToken, l.Attempt, l.AttemptToken, l.PendingRequestID, LeaseTTL)
	if err != nil {
		return err
	}
	if !receipt.Valid() {
		return errors.New("the engine returned an incomplete renewal receipt")
	}
	l.ClaimToken, l.AttemptToken, l.RenewedAt = receipt.ClaimToken, receipt.AttemptToken, now
	l.PendingOp, l.PendingRequestID = "", ""
	for attempt := 0; attempt < 3; attempt++ {
		if err = s.cfg.Store.Save(ctx, l); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("the lease was renewed but the new tokens could not be stored; the next attempt replays it: %w", err)
}

// Run renews leases until ctx ends. The lane's lock keeps a renewal and a
// close from using the same tokens.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.RenewDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
