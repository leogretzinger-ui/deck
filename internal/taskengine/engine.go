// Package taskengine drives the pinned Tasks engine CLI inside a repository
// family: claims, runs, task worktrees and statuses, always through the
// engine and always under its fencing tokens.
package taskengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Engine drives the pinned Tasks engine CLI inside the repository family.
// The controller never opens the family's SQLite store: every claim, run and
// worktree operation passes through the engine, so fencing survives a lease
// that outlives a controller crash. Task state is observed through the
// engine's own JSON output, the only supported read path.
type Engine struct {
	// Binary is the pinned noise-tasks executable.
	Binary string
	// Family is the repository-family checkout: the working tree whose git
	// common dir owns the task store.
	Family string
	// Tag is the family tag task branches live under (tasks/<tag>/<id>).
	Tag string
	// Owner is the claim and run owner identity for controller operations.
	Owner string
	// Prefix is inserted before every subcommand: the unified `noise`
	// executable dispatches the engine as `noise tasks <command>`.
	Prefix []string
	// Env replaces the engine process environment when non-nil.
	Env []string
	// ClaimTTL is the lifetime a new claim asks for. Zero keeps the
	// engine's default.
	ClaimTTL time.Duration
	// Run replaces the process runner in tests. It receives the family
	// checkout as Dir of the real runner would; tests interpret argv.
	Run func(ctx context.Context, args ...string) ([]byte, error)
}

// run executes an engine subcommand with the family checkout as cwd, so the
// engine resolves the family's store by discovery rather than a caller-named
// database path.
func (e Engine) run(ctx context.Context, args ...string) ([]byte, error) {
	if len(e.Prefix) > 0 {
		args = append(append([]string(nil), e.Prefix...), args...)
	}
	var out []byte
	var err error
	if e.Run != nil {
		out, err = e.Run(ctx, args...)
	} else {
		privateArgs, cleanup, prepareErr := engineTokenFiles(args)
		if prepareErr != nil {
			return nil, e.wrap(args, prepareErr, nil)
		}
		defer cleanup()
		cmd := exec.CommandContext(ctx, e.Binary, privateArgs...)
		cmd.WaitDelay = time.Second
		cmd.Dir = e.Family
		if e.Env != nil {
			cmd.Env = e.Env
		}
		// Output otherwise truncates stderr before wrap can redact a token
		// crossing os/exec's prefix/suffix boundary.
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err = cmd.Output()
		if err != nil && len(bytes.TrimSpace(stderr.Bytes())) != 0 {
			out = stderr.Bytes()
		}
	}
	if err != nil {
		return nil, e.wrap(args, err, out)
	}
	return out, nil
}

func (e Engine) wrap(args []string, err error, out []byte) error {
	said := ""
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		said = strings.TrimSpace(string(exitErr.Stderr))
	}
	if said == "" {
		said = strings.TrimSpace(string(out))
	}
	redact := engineTokenRedactor(args)
	said = redact(said)
	if len(said) > 2000 {
		said = said[:2000]
	}
	return &engineCommandError{
		message: fmt.Sprintf("noise-tasks %s: %s: %s", redact(strings.Join(args, " ")), redact(err.Error()), said),
		cause:   err,
	}
}

// Task is the engine's observation of one task. The pinned engine
// serializes the task id as a JSON string.
type Task struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Revision int64  `json:"revision"`
	Claim    *struct {
		Owner      string `json:"owner"`
		AcquiredAt string `json:"acquiredAt"`
		ExpiresAt  string `json:"expiresAt"`
	} `json:"claim"`
}

// Show reads one task through the engine.
func (e Engine) Show(ctx context.Context, id int64) (Task, error) {
	raw, err := e.run(ctx, "show", "-json", "-tag", e.Tag, strconv.FormatInt(id, 10))
	if err != nil {
		return Task{}, err
	}
	var task Task
	if err := json.Unmarshal(raw, &task); err != nil {
		return Task{}, fmt.Errorf("engine show %d: %w", id, err)
	}
	return task, nil
}

// RunID identifies one engine orchestrator run of a task. The pinned
// engine serializes the task reference as a JSON string.
type RunID struct {
	RunID  int64  `json:"runId"`
	TaskID string `json:"task"`
	Status string `json:"status"`
}

// Runs lists the engine runs of one task as the engine reports them.
func (e Engine) Runs(ctx context.Context, taskID int64) ([]RunID, error) {
	raw, err := e.run(ctx, "run", "list", "-json", "-task", strconv.FormatInt(taskID, 10))
	if err != nil {
		return nil, err
	}
	var runs []RunID
	if err := json.Unmarshal(raw, &runs); err != nil {
		return nil, fmt.Errorf("engine run list %d: %w", taskID, err)
	}
	return runs, nil
}

// LiveRun is the run the engine currently allows to act for the task. A
// retryable run still counts: the engine accepts its fencing tokens again.
func (e Engine) LiveRun(ctx context.Context, taskID int64) (RunID, bool, error) {
	runs, err := e.Runs(ctx, taskID)
	if err != nil {
		return RunID{}, false, err
	}
	for _, r := range runs {
		if r.Status == "running" || r.Status == "retryable" {
			return r, true, nil
		}
	}
	return RunID{}, false, nil
}

// ClaimReceipt is a claim with its fencing token.
type ClaimReceipt struct {
	Token     int64  `json:"token"`
	ExpiresAt string `json:"expiresAt"`
	Revision  int64  `json:"revision"`
}

// ErrClaimConflict reports a live claim held by another owner. Claiming is
// the engine's authority gate, so this is "not lawfully driveable", not an
// engine failure.
var ErrClaimConflict = errors.New("engine claim: a live claim is held by another owner")

// Claim acquires the task claim for this owner.
func (e Engine) Claim(ctx context.Context, taskID, revision int64, requestID string) (ClaimReceipt, error) {
	args := []string{"claim", "-json", "-tag", e.Tag, "-owner", e.Owner,
		"-revision", strconv.FormatInt(revision, 10), "-request-id", requestID}
	if e.ClaimTTL > 0 {
		args = append(args, "-ttl", strconv.FormatInt(int64(e.ClaimTTL.Seconds()), 10))
	}
	raw, err := e.run(ctx, append(args, strconv.FormatInt(taskID, 10))...)
	if err != nil {
		if claimConflict(err) {
			return ClaimReceipt{}, ErrClaimConflict
		}
		return ClaimReceipt{}, err
	}
	var receipt ClaimReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return ClaimReceipt{}, fmt.Errorf("engine claim %d: %w", taskID, err)
	}
	return receipt, nil
}

func claimConflict(err error) bool {
	msg := err.Error()
	for _, marker := range []string{"claim is not live", "claim conflict", "already claimed", "run-conflict", "nonterminal run", "kind=claim"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	// The pinned engine reports refusal as pretty-printed structured JSON;
	// whitespace between the key, colon, and value must not matter.
	compact := strings.NewReplacer(" ", "", "\t", "", "\n", "").Replace(msg)
	return strings.Contains(compact, `kind":"claim`)
}

// StartRunReceipt is a new engine run attempt with its fencing tokens.
type StartRunReceipt struct {
	RunID        int64  `json:"runId"`
	Attempt      int64  `json:"attempt"`
	AttemptToken int64  `json:"attemptToken"`
	ExpiresAt    string `json:"expiresAt"`
}

// StartRun creates a durable orchestrator run for the task under the claim.
func (e Engine) StartRun(ctx context.Context, taskID, claimToken int64, requestID string, ttl time.Duration) (StartRunReceipt, error) {
	raw, err := e.run(ctx, "run", "start", "-json", "-tag", e.Tag, "-owner", e.Owner,
		"-claim-token", strconv.FormatInt(claimToken, 10), "-request-id", requestID,
		"-ttl", strconv.FormatInt(int64(ttl.Seconds()), 10), strconv.FormatInt(taskID, 10))
	if err != nil {
		return StartRunReceipt{}, err
	}
	var receipt StartRunReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return StartRunReceipt{}, fmt.Errorf("engine run start %d: %w", taskID, err)
	}
	return receipt, nil
}

// RenewReceipt carries the rotated fencing tokens a renewal returns. The
// old tokens die at renewal, so callers must persist these before their
// next run-scoped command. Both expiries are required: an empty receipt
// is a malformed response, never authority to continue.
type RenewReceipt struct {
	ClaimToken       int64  `json:"claimToken"`
	AttemptToken     int64  `json:"attemptToken"`
	ClaimExpiresAt   string `json:"claimExpiresAt"`
	AttemptExpiresAt string `json:"attemptExpiresAt"`
}

// Valid reports whether the receipt carries the rotated authority a
// renewal must return.
func (r RenewReceipt) Valid() bool {
	return r.ClaimToken > 0 && r.AttemptToken > 0 &&
		r.ClaimExpiresAt != "" && r.AttemptExpiresAt != ""
}

// RenewRun extends the run attempt lease and returns the rotated tokens.
// The TTL names the controller's whole repair bound: a renewal may shorten
// a lease, so the default must never leave one shorter than declared.
func (e Engine) RenewRun(ctx context.Context, runID, claimToken, attempt, attemptToken int64, requestID string, ttl time.Duration) (RenewReceipt, error) {
	raw, err := e.run(ctx, "run", "renew", "-json", "-owner", e.Owner,
		"-claim-token", strconv.FormatInt(claimToken, 10),
		"-attempt", strconv.FormatInt(attempt, 10),
		"-attempt-token", strconv.FormatInt(attemptToken, 10),
		"-request-id", requestID,
		"-ttl", strconv.FormatInt(int64(ttl.Seconds()), 10),
		strconv.FormatInt(runID, 10))
	if err != nil {
		return RenewReceipt{}, err
	}
	var receipt RenewReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return RenewReceipt{}, fmt.Errorf("engine run renew %d: %w", runID, err)
	}
	return receipt, nil
}

// ClaimLive reports whether the observed task claim is unexpired.
func ClaimLive(task Task) bool {
	return task.Claim != nil && task.Claim.ExpiresAt != "" &&
		!time.Now().After(mustParseExpiry(task.Claim.ExpiresAt))
}

func mustParseExpiry(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		// An unreadable expiry is treated as live: the controller waits
		// rather than acting on a claim it cannot bound.
		return time.Now().Add(time.Hour)
	}
	return parsed
}

// RetryReceipt is the pinned engine's run-retry response: a start-shaped
// receipt naming the new attempt. The claim token is not rotated by a
// retry, so the caller keeps the one it supplied.
type RetryReceipt struct {
	RunID        int64  `json:"runId"`
	Attempt      int64  `json:"attempt"`
	AttemptToken int64  `json:"attemptToken"`
	ExpiresAt    string `json:"expiresAt"`
}

// Valid reports whether the receipt carries the rotated attempt fence a
// retry must return.
func (r RetryReceipt) Valid() bool {
	return r.RunID > 0 && r.Attempt > 0 && r.AttemptToken > 0 && r.ExpiresAt != ""
}

// RetryRun allocates the next attempt under an existing run, preserving
// the task binding and its worktree lineage — the supported path from a
// retryable run, unlike cancelling and starting a parallel lineage.
func (e Engine) RetryRun(ctx context.Context, runID, claimToken, attempt, attemptToken int64, requestID string, ttl time.Duration) (RetryReceipt, error) {
	raw, err := e.run(ctx, "run", "retry", "-json", "-owner", e.Owner,
		"-claim-token", strconv.FormatInt(claimToken, 10),
		"-attempt", strconv.FormatInt(attempt, 10),
		"-attempt-token", strconv.FormatInt(attemptToken, 10),
		"-request-id", requestID,
		"-ttl", strconv.FormatInt(int64(ttl.Seconds()), 10),
		strconv.FormatInt(runID, 10))
	if err != nil {
		return RetryReceipt{}, err
	}
	var receipt RetryReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return RetryReceipt{}, fmt.Errorf("engine run retry %d: %w", runID, err)
	}
	return receipt, nil
}

// PruneWorktrees runs the engine's own worktree prune, which proves the
// removal of a cancelled run's worktree before a new run provisions the
// same task path.
func (e Engine) PruneWorktrees(ctx context.Context) error {
	_, err := e.run(ctx, "worktree", "prune", "-json")
	return err
}

// ForceCancelRun terminates a run without its fencing tokens — the engine's
// explicit orchestrator escape hatch. It is the only supported way a new
// claim can exist while an abandoned run lineage is nonterminal.
func (e Engine) ForceCancelRun(ctx context.Context, runID int64, requestID string) error {
	_, err := e.run(ctx, "run", "cancel", "-json", "-force",
		"-request-id", requestID, strconv.FormatInt(runID, 10))
	return err
}

// CancelRun terminates a run under its own fencing tokens, the supported way
// to end a run whose work never started or has been given up.
func (e Engine) CancelRun(ctx context.Context, runID, claimToken, attempt, attemptToken int64, requestID string) error {
	_, err := e.run(ctx, "run", "cancel", "-json", "-owner", e.Owner,
		"-claim-token", strconv.FormatInt(claimToken, 10),
		"-attempt", strconv.FormatInt(attempt, 10),
		"-attempt-token", strconv.FormatInt(attemptToken, 10),
		"-request-id", requestID, strconv.FormatInt(runID, 10))
	return err
}

// Release drops a task claim under its fencing token. The engine refuses
// while a nonterminal run still holds the task.
func (e Engine) Release(ctx context.Context, taskID, claimToken, revision int64) error {
	_, err := e.run(ctx, "release", "-json", "-tag", e.Tag, "-owner", e.Owner,
		"-token", strconv.FormatInt(claimToken, 10),
		"-revision", strconv.FormatInt(revision, 10), strconv.FormatInt(taskID, 10))
	return err
}

// SetStatus writes a task status at an exact revision. With a nonzero
// claimToken it writes under that claim; with zero it is the unclaimed
// write, which the engine refuses while another owner holds a live claim.
func (e Engine) SetStatus(ctx context.Context, taskID, claimToken, revision int64, status string) error {
	args := []string{"set-status", "-json", "-tag", e.Tag,
		"-revision", strconv.FormatInt(revision, 10)}
	if claimToken != 0 {
		args = append(args, "-owner", e.Owner, "-token", strconv.FormatInt(claimToken, 10))
	}
	_, err := e.run(ctx, append(args, strconv.FormatInt(taskID, 10), status)...)
	return err
}

// WorktreeReceipt describes the engine-bound task worktree.
type WorktreeReceipt struct {
	SagaID     int64  `json:"sagaId"`
	Status     string `json:"status"`
	RunID      int64  `json:"runId"`
	Path       string `json:"path"`
	Branch     string `json:"branch"`
	BaseCommit string `json:"baseCommit"`
}

// ProvisionWorktree binds the task worktree for the run. The engine owns the
// worktree and its branch; provisioning an already-bound task reconciles
// with the run's tokens rather than creating a second tree.
func (e Engine) ProvisionWorktree(ctx context.Context, runID, claimToken, attempt, attemptToken int64, requestID string) (WorktreeReceipt, error) {
	raw, err := e.run(ctx, "worktree", "provision", "-json", "-owner", e.Owner,
		"-claim-token", strconv.FormatInt(claimToken, 10),
		"-attempt", strconv.FormatInt(attempt, 10),
		"-attempt-token", strconv.FormatInt(attemptToken, 10),
		"-request-id", requestID, strconv.FormatInt(runID, 10))
	if err != nil {
		return WorktreeReceipt{}, err
	}
	var receipt WorktreeReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return WorktreeReceipt{}, fmt.Errorf("engine worktree provision %d: %w", runID, err)
	}
	return receipt, nil
}

func (e Engine) RemoveWorktree(ctx context.Context, runID, claimToken, attempt, attemptToken int64, requestID string) error {
	raw, err := e.run(ctx, "worktree", "remove", "-json", "-owner", e.Owner,
		"-claim-token", strconv.FormatInt(claimToken, 10),
		"-attempt", strconv.FormatInt(attempt, 10),
		"-attempt-token", strconv.FormatInt(attemptToken, 10),
		"-request-id", requestID, strconv.FormatInt(runID, 10))
	if err != nil {
		return err
	}
	var receipt WorktreeReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return fmt.Errorf("engine worktree remove %d: %w", runID, err)
	}
	if receipt.Status != "removed" || receipt.RunID != runID {
		return fmt.Errorf("engine worktree remove %d did not confirm removal", runID)
	}
	return nil
}

// FinishRun closes a run attempt under its fencing tokens. TaskStatus and
// revision publish a task status in the same transaction; empty leaves the
// task status alone.
func (e Engine) FinishRun(ctx context.Context, runID, claimToken, attempt, attemptToken int64, outcome, taskStatus string, revision int64, requestID string) error {
	args := []string{"run", "finish", "-json", "-owner", e.Owner,
		"-claim-token", strconv.FormatInt(claimToken, 10),
		"-attempt", strconv.FormatInt(attempt, 10),
		"-attempt-token", strconv.FormatInt(attemptToken, 10),
		"-outcome", outcome, "-request-id", requestID}
	if taskStatus != "" {
		args = append(args, "-task-status", taskStatus, "-revision", strconv.FormatInt(revision, 10))
	}
	args = append(args, strconv.FormatInt(runID, 10))
	_, err := e.run(ctx, args...)
	return err
}

// BranchName is the task-bound branch for a task id under the family tag.
func (e Engine) BranchName(taskID int64) string {
	return "tasks/" + e.Tag + "/" + strconv.FormatInt(taskID, 10)
}

// TaskFromBranch parses a task id out of a family branch name. Any other
// shape is not task-bound work and yields false: the admission contract
// treats such a run as having no task.
func (e Engine) TaskFromBranch(branch string) (int64, bool) {
	prefix := "tasks/" + e.Tag + "/"
	if !strings.HasPrefix(branch, prefix) {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(branch, prefix), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// ConfirmRemovedWorktree checks the engine's durable removal receipt before a
// caller deletes the branch retained by that removal saga.
func (e Engine) ConfirmRemovedWorktree(ctx context.Context, runID int64, path, branch string) error {
	raw, err := e.run(ctx, "worktree", "list", "-json")
	if err != nil {
		return err
	}
	var inventory struct {
		Entries []struct {
			RunID     int64  `json:"runId"`
			Operation string `json:"operation"`
			Status    string `json:"status"`
			Active    bool   `json:"active"`
			Path      string `json:"path"`
			Branch    string `json:"branch"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &inventory); err != nil {
		return err
	}
	confirmed := false
	for _, entry := range inventory.Entries {
		if entry.RunID == runID && entry.Active {
			return fmt.Errorf("engine still has an active worktree for run %d", runID)
		}
		if entry.RunID == runID && entry.Operation == "remove" && entry.Status == "removed" && entry.Path == path && entry.Branch == branch {
			confirmed = true
		}
	}
	if !confirmed {
		return fmt.Errorf("engine did not confirm worktree removal for run %d", runID)
	}
	return nil
}
