package lane

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/project"
)

func TestStartRunsTheEngineSequenceAndRegistersTheWorktree(t *testing.T) {
	h := newHarness(t)
	view := h.start()

	want := []string{"show", "claim", "show", "set-status", "run start", "worktree provision"}
	if got := h.engine.log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("engine calls = %v, want %v", got, want)
	}
	if view.Phase != PhaseOpen || view.Branch != "tasks/main/1" || view.LaneProjectID == "" || view.Agent != "claude" {
		t.Fatalf("view = %+v", view)
	}
	lane, err := h.projects.Get(context.Background(), view.LaneProjectID)
	if err != nil || lane.CanonicalPath != h.engine.worktree {
		t.Fatalf("lane project = %+v, %v; worktree %q", lane, err, h.engine.worktree)
	}
	if h.engine.status != "in-progress" {
		t.Fatalf("task status = %q, want in-progress", h.engine.status)
	}
	ignored, _ := os.ReadFile(filepath.Join(h.repo, ".git", "info", "exclude"))
	if !strings.Contains(string(ignored), ".worktrees/") {
		t.Fatalf("worktrees directory is not ignored locally: %q", ignored)
	}
}

func TestStoredLaneHoldsTokensThatNeverReachTheView(t *testing.T) {
	h := newHarness(t)
	view := h.start()

	stored, err := h.store.Get(context.Background(), h.source.ID, "main", 1)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ClaimToken == 0 || stored.AttemptToken == 0 || stored.RunID != 1 {
		t.Fatalf("stored lane lacks its capabilities: %+v", stored)
	}
	body, _ := json.Marshal(view)
	if strings.Contains(strings.ToLower(string(body)), "token") {
		t.Fatalf("view leaks a capability: %s", body)
	}
}

func TestReloadFindsTheOpenLaneAndStartIsIdempotent(t *testing.T) {
	h := newHarness(t)
	first := h.start()
	h.engine.reset()

	reloaded := NewService(h.svc.cfg)
	lanes, err := reloaded.List(context.Background(), h.source.ID)
	if err != nil || len(lanes) != 1 || !reflect.DeepEqual(lanes[0], first) {
		t.Fatalf("List = %+v, %v; want %+v", lanes, err, first)
	}
	again := h.start()
	if !reflect.DeepEqual(again, first) {
		t.Fatalf("second Start = %+v, want %+v", again, first)
	}
	if calls := h.engine.log(); len(calls) != 0 {
		t.Fatalf("a second Start called the engine: %v", calls)
	}
}

func TestListAnswersForTheLaneProjectToo(t *testing.T) {
	h := newHarness(t)
	view := h.start()
	lanes, err := h.svc.List(context.Background(), view.LaneProjectID)
	if err != nil || len(lanes) != 1 || lanes[0].ProjectID != h.source.ID {
		t.Fatalf("List(lane project) = %+v, %v; the Tasks panel follows the selected session into the lane", lanes, err)
	}
}

func TestRenewalRotatesTokensAndStoresThem(t *testing.T) {
	h := newHarness(t)
	h.start()
	before, _ := h.store.Get(context.Background(), h.source.ID, "main", 1)
	h.engine.reset()

	h.now = h.now.Add(h.svc.cfg.RenewEvery - time.Minute)
	h.svc.RenewDue(context.Background())
	if calls := h.engine.log(); len(calls) != 0 {
		t.Fatalf("renewed before it was due: %v", calls)
	}

	h.now = h.now.Add(2 * time.Minute)
	h.svc.RenewDue(context.Background())
	if calls := h.engine.log(); !reflect.DeepEqual(calls, []string{"run renew"}) {
		t.Fatalf("calls = %v, want one renewal", calls)
	}
	after, _ := h.store.Get(context.Background(), h.source.ID, "main", 1)
	if after.ClaimToken == before.ClaimToken || after.AttemptToken == before.AttemptToken {
		t.Fatalf("tokens were not rotated: before %+v after %+v", before, after)
	}
	if after.ClaimToken != h.engine.claimToken || after.AttemptToken != h.engine.attemptToken {
		t.Fatalf("stored tokens differ from the engine's: %+v", after)
	}
	if !after.RenewedAt.Equal(h.now) {
		t.Fatalf("renewed_at = %v, want %v", after.RenewedAt, h.now)
	}

	// The stored pair must be the live one: closing the lane uses it.
	if _, err := h.done(DoneInput{}); err != nil {
		t.Fatalf("Done after a renewal: %v", err)
	}
}

func TestRenewalFailureKeepsTheStoredTokens(t *testing.T) {
	h := newHarness(t)
	h.start()
	before, _ := h.store.Get(context.Background(), h.source.ID, "main", 1)
	h.engine.fail["run renew"] = errors.New("engine unreachable")

	h.now = h.now.Add(time.Hour)
	h.svc.RenewDue(context.Background())
	after, _ := h.store.Get(context.Background(), h.source.ID, "main", 1)
	if after.ClaimToken != before.ClaimToken || after.AttemptToken != before.AttemptToken || !after.RenewedAt.Equal(before.RenewedAt) {
		t.Fatalf("a failed renewal changed the lane: %+v", after)
	}
}

func TestDoneClosesInTheTaskWorkflowOrder(t *testing.T) {
	h := newHarness(t)
	view := h.start()
	h.engine.reset()

	result, err := h.done(DoneInput{})
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if !result.LaneClosed || result.Status != "done" || result.Branch != "tasks/main/1" {
		t.Fatalf("result = %+v", result)
	}
	if got, want := h.engine.log(), []string{"worktree remove", "show", "run finish"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("engine calls = %v, want %v", got, want)
	}
	if h.engine.status != "done" || h.engine.runStatus != "succeeded" {
		t.Fatalf("engine task %q run %q", h.engine.status, h.engine.runStatus)
	}
	if _, err := os.Stat(filepath.Join(h.repo, ".worktrees", "main-1")); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still exists: %v", err)
	}
	if result.BranchKept || exec.Command("git", "-C", h.repo, "rev-parse", "--verify", "-q", "refs/heads/tasks/main/1").Run() == nil {
		t.Fatalf("an empty lane branch should be deleted: %+v", result)
	}
	if _, err := h.projects.Get(context.Background(), view.LaneProjectID); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("lane project still registered: %v", err)
	}
	if _, err := h.store.Get(context.Background(), h.source.ID, "main", 1); !errors.Is(err, ErrNoLane) {
		t.Fatalf("lane row survived: %v", err)
	}

	h.engine.reset()
	h.now = h.now.Add(24 * time.Hour)
	h.svc.RenewDue(context.Background())
	if calls := h.engine.log(); len(calls) != 0 {
		t.Fatalf("a closed lane was renewed: %v", calls)
	}
}

func TestDoneRefusesUncommittedWork(t *testing.T) {
	h := newHarness(t)
	h.start()
	if err := os.WriteFile(filepath.Join(h.engine.worktree, "notes.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.engine.reset()

	_, err := h.done(DoneInput{})
	var risk *WorkAtRiskError
	if !errors.As(err, &risk) || risk.Dirty != 1 {
		t.Fatalf("err = %v, want one uncommitted file", err)
	}
	if calls := h.engine.log(); len(calls) != 0 {
		t.Fatalf("a refused close still called the engine: %v", calls)
	}
	if _, statErr := os.Stat(filepath.Join(h.engine.worktree, "notes.txt")); statErr != nil {
		t.Fatalf("the work was touched: %v", statErr)
	}
	if lanes, _ := h.svc.List(context.Background(), h.source.ID); len(lanes) != 1 || lanes[0].Phase != PhaseOpen {
		t.Fatalf("lane after a refusal = %+v", lanes)
	}
}

func TestDoneRefusesUnpushedCommitsUntilTheyAreOnARemote(t *testing.T) {
	h := newHarness(t)
	h.start()
	git(t, h.engine.worktree, "commit", "-q", "--allow-empty", "-m", "change")

	_, err := h.done(DoneInput{})
	var risk *WorkAtRiskError
	if !errors.As(err, &risk) || risk.Dirty != 0 || !strings.Contains(risk.Detail, "never pushed") {
		t.Fatalf("err = %v, want a never-pushed refusal", err)
	}

	remote := filepath.Join(t.TempDir(), "remote.git")
	git(t, filepath.Dir(remote), "init", "-q", "--bare", remote)
	git(t, h.engine.worktree, "remote", "add", "origin", remote)
	git(t, h.engine.worktree, "push", "-q", "-u", "origin", "tasks/main/1")
	git(t, h.engine.worktree, "commit", "-q", "--allow-empty", "-m", "another")
	if _, err := h.done(DoneInput{}); !errors.As(err, &risk) || !strings.Contains(risk.Detail, "not pushed") {
		t.Fatalf("err = %v, want an ahead-of-remote refusal", err)
	}

	git(t, h.engine.worktree, "push", "-q")
	result, err := h.done(DoneInput{})
	if err != nil || !result.LaneClosed || result.BranchKept {
		t.Fatalf("Done after pushing = %+v, %v; a branch whose remote has every commit is deleted", result, err)
	}
}

func TestDoneKeepsABranchGitCannotProveSafe(t *testing.T) {
	h := newHarness(t)
	h.start()
	git(t, h.engine.worktree, "commit", "-q", "--allow-empty", "-m", "change")
	remote := filepath.Join(t.TempDir(), "remote.git")
	git(t, filepath.Dir(remote), "init", "-q", "--bare", remote)
	git(t, h.engine.worktree, "remote", "add", "origin", remote)
	git(t, h.engine.worktree, "push", "-q", "-u", "origin", "tasks/main/1")
	git(t, h.engine.worktree, "push", "-q", "origin", "--delete", "tasks/main/1")
	git(t, h.engine.worktree, "fetch", "-q", "--prune", "origin")

	result, err := h.done(DoneInput{})
	if err != nil || !result.LaneClosed || !result.BranchKept {
		t.Fatalf("Done = %+v, %v; the branch holds a commit nothing else has, so it must stay", result, err)
	}
	if exec.Command("git", "-C", h.repo, "rev-parse", "--verify", "-q", "refs/heads/tasks/main/1").Run() != nil {
		t.Fatal("the branch was deleted")
	}
}

func TestDoneNeedsConsentToStopSessions(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.projects.live = []project.LiveSession{{ID: "s1", Label: "claude"}}
	h.engine.reset()

	_, err := h.done(DoneInput{})
	var live *SessionsLiveError
	if !errors.As(err, &live) || !reflect.DeepEqual(live.Labels, []string{"claude"}) {
		t.Fatalf("err = %v, want a live-sessions refusal", err)
	}
	if calls := h.engine.log(); len(calls) != 0 {
		t.Fatalf("a refused close called the engine: %v", calls)
	}
	h.engine.reset()
	if _, err := h.done(DoneInput{StopSessions: true}); err != nil {
		t.Fatalf("Done with consent: %v", err)
	}
	if got, want := h.engine.logAll(), []string{"hold sessions", "stop sessions", "worktree remove", "show", "run finish", "release sessions"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want sessions stopped before the worktree is removed: %v", got, want)
	}
}

func TestDoneNeverLeavesAgentsInADeletedCheckout(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.projects.live = []project.LiveSession{{ID: "s1", Label: "claude"}}
	h.engine.fail["run finish"] = errors.New("engine unreachable")
	h.engine.reset()
	if _, err := h.done(DoneInput{StopSessions: true}); err == nil {
		t.Fatal("expected the finish to fail")
	}
	if len(h.projects.live) != 0 {
		t.Fatal("sessions still run after the worktree was deleted")
	}
	if _, statErr := os.Stat(filepath.Join(h.repo, ".worktrees", "main-1")); !os.IsNotExist(statErr) {
		t.Fatalf("worktree should be gone: %v", statErr)
	}
	if calls := h.engine.log(); calls[0] != "stop sessions" {
		t.Fatalf("calls = %v; sessions must stop first", calls)
	}
}

// A session that was admitted while the others were stopping is still live
// when the close looks again, and the worktree stays.
func TestDoneRefusesASessionThatSlippedInDuringShutdown(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.projects.live = []project.LiveSession{{ID: "s1", Label: "claude"}}
	h.afterStop = func() { h.projects.live = []project.LiveSession{{ID: "s2", Label: "codex"}} }
	h.engine.reset()
	var live *SessionsLiveError
	if _, err := h.done(DoneInput{StopSessions: true}); !errors.As(err, &live) || !reflect.DeepEqual(live.Labels, []string{"codex"}) {
		t.Fatalf("err = %v, want the late session named", err)
	}
	if got, want := h.engine.logAll(), []string{"hold sessions", "stop sessions", "release sessions"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v; the worktree must not be removed and the hold must be lifted", got, want)
	}
}

func TestDoneLooksAgainAfterTheAgentsStop(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.projects.live = []project.LiveSession{{ID: "s1", Label: "claude"}}
	h.afterStop = func() {
		if err := os.WriteFile(filepath.Join(h.engine.worktree, "late.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.engine.reset()
	var risk *WorkAtRiskError
	if _, err := h.done(DoneInput{StopSessions: true}); !errors.As(err, &risk) || risk.Dirty != 1 {
		t.Fatalf("err = %v, want the file the agent wrote while stopping", err)
	}
	for _, call := range h.engine.log() {
		if call == "worktree remove" {
			t.Fatal("the worktree was removed over late work")
		}
	}
	if _, statErr := os.Stat(filepath.Join(h.engine.worktree, "late.txt")); statErr != nil {
		t.Fatal("the late work was lost")
	}
}

func TestRenewalWithALostResponseIsReplayedNotRepeated(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.engine.loseResponse["run renew"] = 1
	h.now = h.now.Add(time.Hour)
	h.svc.RenewDue(context.Background())
	stored, _ := h.store.Get(context.Background(), h.source.ID, "main", 1)
	if stored.PendingOp != "renew" || stored.PendingRequestID == "" || stored.ClaimToken == h.engine.claimToken {
		t.Fatalf("the renewal that may have committed must stay pending with its inputs: %+v", stored)
	}

	h.now = h.now.Add(time.Second)
	h.svc.RenewDue(context.Background())
	stored, _ = h.store.Get(context.Background(), h.source.ID, "main", 1)
	if stored.PendingOp != "" || stored.ClaimToken != h.engine.claimToken || stored.AttemptToken != h.engine.attemptToken {
		t.Fatalf("the replay must store the rotated pair: %+v engine %d/%d", stored, h.engine.claimToken, h.engine.attemptToken)
	}
	if _, err := h.done(DoneInput{}); err != nil {
		t.Fatalf("Done after a replayed renewal: %v", err)
	}
}

func TestDoneSettlesAnUnrecordedRenewalFirst(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.engine.loseResponse["run renew"] = 1
	h.now = h.now.Add(time.Hour)
	h.svc.RenewDue(context.Background())
	if result, err := h.done(DoneInput{}); err != nil || !result.LaneClosed {
		t.Fatalf("Done = %+v, %v; the stored tokens were dead until the renewal was replayed", result, err)
	}
}

func TestFinishWithALostResponseReplaysTheSameInputs(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.engine.loseResponse["run finish"] = 1
	if _, err := h.done(DoneInput{}); err == nil {
		t.Fatal("expected the lost response to fail the close")
	}
	stored, _ := h.store.Get(context.Background(), h.source.ID, "main", 1)
	if stored.PendingOp != "finish" || stored.PendingRevision == 0 || stored.Phase != PhaseRemoved {
		t.Fatalf("the finish inputs must be stored before dispatch: %+v", stored)
	}
	if h.engine.status != "done" {
		t.Fatalf("the stub should have committed the finish, status %q", h.engine.status)
	}
	result, err := h.done(DoneInput{})
	if err != nil || !result.LaneClosed {
		t.Fatalf("retry = %+v, %v; a re-read revision would be a replay conflict", result, err)
	}
}

func TestInProgressWriteResumesAfterTheClaimWasStored(t *testing.T) {
	h := newHarness(t)
	h.engine.fail["set-status"] = errors.New("boom")
	h.engine.fail["release"] = errors.New("engine unreachable")
	if _, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"}); err == nil {
		t.Fatal("expected a failure")
	}
	stored, _ := h.store.Get(context.Background(), h.source.ID, "main", 1)
	if stored.ClaimToken == 0 || stored.StatusDone || stored.PriorStatus != "pending" {
		t.Fatalf("the claim is stored and the status step is still owed: %+v", stored)
	}
	stored.Phase = PhaseClaimed
	if err := h.store.Save(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	delete(h.engine.fail, "set-status")
	delete(h.engine.fail, "release")
	view := h.start()
	if view.Phase != PhaseOpen || h.engine.status != "in-progress" {
		t.Fatalf("view %+v, task status %q; the resumed lane must have marked the task", view, h.engine.status)
	}
}

func TestRollbackRestoresTheStatusTheTaskHadBeforeTheClaim(t *testing.T) {
	h := newHarness(t)
	h.engine.status = "in-progress"
	h.engine.fail["run start"] = errEngineRefusal
	if _, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"}); err == nil {
		t.Fatal("expected a failure")
	}
	if h.engine.status != "in-progress" || h.engine.claimToken != 0 {
		t.Fatalf("status %q claim %d; a task that was already in progress stays so", h.engine.status, h.engine.claimToken)
	}
}

func TestSourceRemovalIsRefusedWhileALaneExists(t *testing.T) {
	h := newHarness(t)
	h.start()
	repo := h.projects.Repository
	err := repo.Remove(context.Background(), h.source.ID)
	if !errors.Is(err, project.ErrRemovalBlocked) || !strings.Contains(err.Error(), "open task lane") {
		t.Fatalf("err = %v", err)
	}
	if err := repo.RemovalBlocker(context.Background(), h.source.ID); !errors.Is(err, project.ErrRemovalBlocked) {
		t.Fatalf("pre-check = %v", err)
	}
	if _, err := h.done(DoneInput{}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Remove(context.Background(), h.source.ID); err != nil {
		t.Fatalf("after the lane closed: %v", err)
	}
}

// Removal and lane creation exclude each other: a source removed after the
// lane service looked it up gets no lane, and nothing is sent to the engine.
func TestLaneCreationRefusesASourceRemovedBetweenLookupAndInsert(t *testing.T) {
	h := newHarness(t)
	h.svc.cfg.Projects = &removingProjects{fakeProjects: h.projects, remove: func() {
		if err := h.projects.Remove(context.Background(), h.source.ID); err != nil {
			t.Fatal(err)
		}
	}}
	_, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"})
	if !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("err = %v, want the source reported gone", err)
	}
	if calls := h.engine.log(); len(calls) != 0 {
		t.Fatalf("engine calls = %v; nothing may be claimed for a removed source", calls)
	}
	if rows, _ := h.store.ListProject(context.Background(), h.source.ID); len(rows) != 0 {
		t.Fatalf("a lane row survived for a removed source: %+v", rows)
	}
}

type removingProjects struct {
	*fakeProjects
	remove func()
}

func (r *removingProjects) Get(ctx context.Context, id string) (*project.Project, error) {
	p, err := r.fakeProjects.Get(ctx, id)
	if err == nil && r.remove != nil {
		remove := r.remove
		r.remove = nil
		remove()
	}
	return p, err
}

// A startup command whose response is lost is sent again with the same
// request id and inputs, so what the engine committed is recorded.
func TestStartupCommandsWithALostResponseAreReplayed(t *testing.T) {
	for _, command := range []string{"claim", "run start", "worktree provision"} {
		t.Run(command, func(t *testing.T) {
			h := newHarness(t)
			h.engine.loseResponse[command] = 1
			view := h.start()
			if view.Phase != PhaseOpen {
				t.Fatalf("view = %+v", view)
			}
			count := 0
			for _, call := range h.engine.log() {
				if call == command {
					count++
				}
			}
			if count != 2 {
				t.Fatalf("%s was sent %d times, want the original and one replay", command, count)
			}
			if _, err := h.done(DoneInput{}); err != nil {
				t.Fatalf("the recorded receipt must close the lane: %v", err)
			}
		})
	}
}

// When even the replay fails, cleanup asks the engine again before it rolls
// back, and rolls back only what the answer shows was committed. A restarted
// service does the same from the stored row.
func TestCleanupAfterALostStartupResponseLearnsWhatCommitted(t *testing.T) {
	for _, command := range []string{"claim", "run start", "worktree provision"} {
		t.Run(command, func(t *testing.T) {
			h := newHarness(t)
			h.engine.loseResponse[command] = 2
			_, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"})
			var step *StepError
			if !errors.As(err, &step) {
				t.Fatalf("err = %v, want a named step", err)
			}
			if strings.Contains(step.Error(), "Cleaning up also failed") {
				t.Fatalf("cleanup should have learned the committed state: %v", err)
			}
			if h.engine.claimToken != 0 || h.engine.status != "pending" || h.engine.runStatus == "running" {
				t.Fatalf("the engine was not handed back: claim %d status %q run %q", h.engine.claimToken, h.engine.status, h.engine.runStatus)
			}
			if _, statErr := os.Stat(filepath.Join(h.repo, ".worktrees", "main-1")); !os.IsNotExist(statErr) {
				t.Fatalf("worktree left behind: %v", statErr)
			}
			if _, getErr := h.store.Get(context.Background(), h.source.ID, "main", 1); !errors.Is(getErr, ErrNoLane) {
				t.Fatalf("lane row left behind: %v", getErr)
			}
			h.engine.runStatus = ""
			if view := h.start(); view.Phase != PhaseOpen {
				t.Fatalf("retry = %+v", view)
			}
		})
	}
}

func TestARestartedServiceRecoversALostStartupCommand(t *testing.T) {
	for _, command := range []string{"claim", "run start", "worktree provision"} {
		t.Run(command, func(t *testing.T) {
			h := newHarness(t)
			// The command, its replay and the cleanup's replay all lose their answer.
			h.engine.loseResponse[command] = 3
			if _, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"}); err == nil {
				t.Fatal("expected a failure")
			}
			stored, err := h.store.Get(context.Background(), h.source.ID, "main", 1)
			if err != nil || stored.Phase != PhaseFailed {
				t.Fatalf("the lane must stay recorded: %+v, %v", stored, err)
			}
			restarted := NewService(h.svc.cfg)
			view, err := restarted.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"})
			if err != nil || view.Phase != PhaseOpen {
				t.Fatalf("restarted Start = %+v, %v", view, err)
			}
		})
	}
}

func TestDoneRetriesTheRunFinishAfterTheWorktreeIsGone(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.engine.fail["run finish"] = errors.New("engine unreachable")
	var step *StepError
	if _, err := h.done(DoneInput{}); !errors.As(err, &step) || step.Step != "finish the run" {
		t.Fatalf("err = %v, want the finish step", err)
	}
	stored, _ := h.store.Get(context.Background(), h.source.ID, "main", 1)
	if stored.Phase != PhaseRemoved {
		t.Fatalf("phase = %q, want removed", stored.Phase)
	}

	delete(h.engine.fail, "run finish")
	h.engine.reset()
	if result, err := h.done(DoneInput{}); err != nil || !result.LaneClosed {
		t.Fatalf("retry = %+v, %v", result, err)
	}
	if calls := h.engine.log(); !reflect.DeepEqual(calls, []string{"run finish"}) {
		t.Fatalf("retry called %v; the worktree must not be removed twice and the finish inputs are not re-read", calls)
	}
}

func TestDoneWithoutALaneWritesTheStatusAtTheSeenRevision(t *testing.T) {
	h := newHarness(t)
	seen := int64(1)
	result, err := h.done(DoneInput{Revision: &seen})
	if err != nil || result.LaneClosed || result.Status != "done" || h.engine.status != "done" {
		t.Fatalf("Done = %+v, %v; engine status %q", result, err, h.engine.status)
	}
	if got, want := h.engine.log(), []string{"show", "set-status"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("engine calls = %v, want %v", got, want)
	}
	if again, err := h.done(DoneInput{}); err != nil || again.Status != "done" {
		t.Fatalf("Done on a done task = %+v, %v", again, err)
	}
}

func TestDoneWithoutALaneRefusesAStaleRevision(t *testing.T) {
	h := newHarness(t)
	old := int64(0)
	if _, err := h.done(DoneInput{Revision: &old}); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
	if h.engine.status != "pending" {
		t.Fatalf("status = %q; a stale write must change nothing", h.engine.status)
	}
}

func TestDoneWithoutALaneSurfacesAClaimHeldByAnotherOwner(t *testing.T) {
	h := newHarness(t)
	h.engine.otherClaim = true
	var step *StepError
	if _, err := h.done(DoneInput{}); !errors.As(err, &step) || step.Step != "mark the task done" {
		t.Fatalf("err = %v, want a mark-done step error", err)
	}
	if h.engine.status != "pending" {
		t.Fatalf("status = %q", h.engine.status)
	}
}

func TestStartRefusesATaskAnotherOwnerClaimed(t *testing.T) {
	h := newHarness(t)
	h.engine.otherClaim = true
	_, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"})
	if !errors.Is(err, ErrClaimed) {
		t.Fatalf("err = %v, want ErrClaimed", err)
	}
	if _, err := h.store.Get(context.Background(), h.source.ID, "main", 1); !errors.Is(err, ErrNoLane) {
		t.Fatalf("a refused start left a lane row: %v", err)
	}
}

func TestStartRefusesADoneTask(t *testing.T) {
	h := newHarness(t)
	h.engine.status = "done"
	_, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"})
	if !errors.Is(err, ErrNotOpen) {
		t.Fatalf("err = %v, want ErrNotOpen", err)
	}
}

// A start that stops at any step names the step, hands the engine back the
// way it found it, and can be tried again.
func TestStartFailureAtEachStepIsNamedAndUndone(t *testing.T) {
	cases := []struct {
		name   string
		step   string
		break_ func(h *harness)
	}{
		{"read", "read the task", func(h *harness) { h.engine.fail["show"] = errors.New("boom") }},
		{"claim", "claim the task", func(h *harness) { h.engine.fail["claim"] = errEngineRefusal }},
		{"status", "mark the task in progress", func(h *harness) { h.engine.fail["set-status"] = errors.New("boom") }},
		{"run", "start the run", func(h *harness) { h.engine.fail["run start"] = errEngineRefusal }},
		{"worktree", "create the worktree", func(h *harness) { h.engine.fail["worktree provision"] = errEngineRefusal }},
		{"register", "register the worktree as a project", func(h *harness) { h.projects.registerErr = errors.New("boom") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.break_(h)
			_, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"})
			var step *StepError
			if !errors.As(err, &step) || step.Step != tc.step {
				t.Fatalf("err = %v, want step %q", err, tc.step)
			}
			if strings.Contains(step.Error(), "cleaning up") || strings.Contains(strings.ToLower(step.Error()), "cleanup") {
				t.Fatalf("cleanup should have succeeded: %v", err)
			}
			if h.engine.claimToken != 0 || h.engine.status != "pending" {
				t.Fatalf("claim %d status %q: the engine was not handed back", h.engine.claimToken, h.engine.status)
			}
			if h.engine.runStatus == "running" {
				t.Fatalf("the run is still running")
			}
			if _, statErr := os.Stat(filepath.Join(h.repo, ".worktrees", "main-1")); !os.IsNotExist(statErr) {
				t.Fatalf("worktree left behind: %v", statErr)
			}
			if _, getErr := h.store.Get(context.Background(), h.source.ID, "main", 1); !errors.Is(getErr, ErrNoLane) {
				t.Fatalf("lane row left behind: %v", getErr)
			}

			for k := range h.engine.fail {
				delete(h.engine.fail, k)
			}
			h.projects.registerErr = nil
			h.engine.runStatus = ""
			if view := h.start(); view.Phase != PhaseOpen {
				t.Fatalf("retry = %+v", view)
			}
		})
	}
}

func TestStartNeverRemovesAWorktreeThatHoldsWork(t *testing.T) {
	h := newHarness(t)
	h.projects.registerErr = errors.New("boom")
	h.engine.afterBind = func(path string) {
		if err := os.WriteFile(filepath.Join(path, "keep.txt"), []byte("work"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"})
	var step *StepError
	if !errors.As(err, &step) || step.Step != "register the worktree as a project" || !strings.Contains(step.Error(), "left in place") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(h.engine.worktree, "keep.txt")); statErr != nil {
		t.Fatalf("the work was deleted: %v", statErr)
	}
	stored, getErr := h.store.Get(context.Background(), h.source.ID, "main", 1)
	if getErr != nil || stored.Phase != PhaseFailed || stored.ClaimToken == 0 {
		t.Fatalf("the failed lane must stay recorded with its tokens: %+v, %v", stored, getErr)
	}
	if lanes, _ := h.svc.List(context.Background(), h.source.ID); len(lanes) != 1 || lanes[0].Phase != PhaseFailed {
		t.Fatalf("List = %+v", lanes)
	}
	h.now = h.now.Add(24 * time.Hour)
	h.engine.reset()
	h.svc.RenewDue(context.Background())
	if calls := h.engine.log(); len(calls) != 0 {
		t.Fatalf("a failed lane was renewed: %v", calls)
	}
	if _, doneErr := h.done(DoneInput{}); !errors.Is(doneErr, ErrNotReady) {
		t.Fatalf("Done on a failed lane = %v, want ErrNotReady", doneErr)
	}
}

func TestStartRetriesAnIncompleteCleanup(t *testing.T) {
	h := newHarness(t)
	h.engine.fail["worktree provision"] = errEngineRefusal
	h.engine.fail["run cancel"] = errors.New("engine unreachable")
	_, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"})
	var step *StepError
	if !errors.As(err, &step) || !strings.Contains(step.Error(), "Cleaning up also failed") || !strings.Contains(step.Error(), "hygiene") {
		t.Fatalf("err = %v, want a cleanup failure that points at noise tasks hygiene", err)
	}
	if h.engine.claimToken == 0 {
		t.Fatal("the claim must stay so noise tasks can see and recover it")
	}

	delete(h.engine.fail, "run cancel")
	delete(h.engine.fail, "worktree provision")
	if view := h.start(); view.Phase != PhaseOpen {
		t.Fatalf("retry = %+v", view)
	}
}

func TestStartResumesAfterARestartBetweenSteps(t *testing.T) {
	h := newHarness(t)
	h.engine.fail["worktree provision"] = errEngineRefusal
	h.engine.fail["run cancel"] = errors.New("engine unreachable")
	if _, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"}); err == nil {
		t.Fatal("expected a failure")
	}
	stored, _ := h.store.Get(context.Background(), h.source.ID, "main", 1)
	stored.Phase = PhaseRunning
	if err := h.store.Save(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	delete(h.engine.fail, "run cancel")
	delete(h.engine.fail, "worktree provision")
	h.engine.reset()

	view := h.start()
	if view.Phase != PhaseOpen {
		t.Fatalf("view = %+v", view)
	}
	if calls := h.engine.log(); !reflect.DeepEqual(calls, []string{"worktree provision"}) {
		t.Fatalf("resume called %v; claimed and started steps must not repeat", calls)
	}
}

func TestReviewNeedsADifferentAgent(t *testing.T) {
	h := newHarness(t)
	h.start()
	if _, err := h.svc.RequestReview(context.Background(), h.source.ID, "main", 1, "claude"); !errors.Is(err, ErrSameAgent) {
		t.Fatalf("err = %v, want ErrSameAgent", err)
	}
	view, err := h.svc.RequestReview(context.Background(), h.source.ID, "main", 1, "codex")
	if err != nil || !view.ReviewRequested || view.ReviewAgent != "codex" {
		t.Fatalf("view = %+v, %v", view, err)
	}
}

func TestReviewSeenWaitsForACommentAfterTheRequest(t *testing.T) {
	h := newHarness(t)
	h.start()
	state, err := h.svc.Review(context.Background(), h.source.ID, "main", 1)
	if err != nil || state.Requested || state.Seen {
		t.Fatalf("before a request: %+v, %v", state, err)
	}

	if _, err := h.svc.RequestReview(context.Background(), h.source.ID, "main", 1, "codex"); err != nil {
		t.Fatal(err)
	}
	before := h.now.Add(-time.Hour).Format(time.RFC3339)
	after := h.now.Add(time.Minute).Format(time.RFC3339)
	var args []string
	h.gh = func(a ...string) ([]byte, error) {
		args = a
		return []byte(`[{"number":7,"comments":[{"createdAt":"` + before + `"}],"reviews":[]}]`), nil
	}
	state, err = h.svc.Review(context.Background(), h.source.ID, "main", 1)
	if err != nil || !state.Requested || state.Seen || state.PullRequest != 7 {
		t.Fatalf("an older comment must not count: %+v, %v", state, err)
	}
	if !reflect.DeepEqual(args[:4], []string{"pr", "list", "--head", "tasks/main/1"}) {
		t.Fatalf("gh args = %v", args)
	}

	h.gh = func(...string) ([]byte, error) {
		return []byte(`[{"number":7,"comments":[],"reviews":[{"submittedAt":"` + after + `"}]}]`), nil
	}
	if state, err = h.svc.Review(context.Background(), h.source.ID, "main", 1); err != nil || !state.Seen {
		t.Fatalf("a review after the request must count: %+v, %v", state, err)
	}

	h.gh = nil
	if _, err := h.svc.Review(context.Background(), h.source.ID, "main", 1); err == nil {
		t.Fatal("an unreadable GitHub must be an error, not a silent no")
	}
}

func TestStartRequiresATagATaskAndAnAgent(t *testing.T) {
	h := newHarness(t)
	for _, in := range []StartInput{{TaskID: 1, Agent: "a"}, {Tag: "main", Agent: "a"}, {Tag: "main", TaskID: 1}} {
		if _, err := h.svc.Start(context.Background(), h.source.ID, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Start(%+v) = %v, want ErrInvalid", in, err)
		}
	}
}

// A release that committed but whose response was lost leaves a dead token in
// the row; cleanup asks the engine who holds the claim instead of repeating it.
func TestCleanupAfterALostReleaseResponseDoesNotRepeatTheDeadToken(t *testing.T) {
	h := newHarness(t)
	h.projects.registerErr = errors.New("boom")
	h.engine.loseResponse["release"] = 1
	_, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"})
	var step *StepError
	if !errors.As(err, &step) || !strings.Contains(step.Error(), "Cleaning up also failed") {
		t.Fatalf("err = %v, want the lost release to fail the cleanup", err)
	}
	if h.engine.claimToken != 0 {
		t.Fatal("the release should have committed")
	}
	stored, _ := h.store.Get(context.Background(), h.source.ID, "main", 1)
	if stored == nil || stored.ClaimToken == 0 {
		t.Fatalf("the row keeps the dead token until cleanup is retried: %+v", stored)
	}
	if err := h.projects.RemovalBlocker(context.Background(), h.source.ID); err == nil {
		t.Fatal("a failed row still blocks the source project's removal until it is cleaned up")
	}

	h.projects.registerErr = nil
	h.engine.runStatus = ""
	if view := h.start(); view.Phase != PhaseOpen {
		t.Fatalf("retry = %+v; the dead token must not wedge the lane", view)
	}
}

func TestARestartAfterReleaseButBeforeBookkeepingRecovers(t *testing.T) {
	h := newHarness(t)
	h.projects.registerErr = errors.New("boom")
	h.engine.fail["release"] = errors.New("engine unreachable")
	if _, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"}); err == nil {
		t.Fatal("expected a failure")
	}
	// The release went through after all; nothing recorded it.
	delete(h.engine.fail, "release")
	h.engine.claimToken = 0
	h.engine.runStatus = ""
	h.engine.status = "pending"
	h.projects.registerErr = nil

	view, err := NewService(h.svc.cfg).Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"})
	if err != nil || view.Phase != PhaseOpen {
		t.Fatalf("restarted Start = %+v, %v", view, err)
	}
}

func TestCleanupNeverReleasesAClaimAnotherOwnerHolds(t *testing.T) {
	h := newHarness(t)
	h.projects.registerErr = errors.New("boom")
	h.engine.fail["release"] = errors.New("engine unreachable")
	if _, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"}); err == nil {
		t.Fatal("expected a failure")
	}
	delete(h.engine.fail, "release")
	h.engine.claimToken = 0
	h.engine.otherClaim = true
	h.engine.reset()
	h.projects.registerErr = nil

	_, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"})
	if !errors.Is(err, ErrClaimed) {
		t.Fatalf("err = %v, want the other owner's claim respected", err)
	}
	for _, call := range h.engine.log() {
		if call == "release" {
			t.Fatalf("a claim held by someone else was released: %v", h.engine.log())
		}
	}
}
