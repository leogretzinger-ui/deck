package lane

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/noisefloor-oss/deck/internal/taskengine"
)

// realHarness points the lane service at the installed engine. It is skipped
// where the engine is not installed.
func realHarness(t *testing.T) (*harness, func(args ...string) string) {
	t.Helper()
	noise, err := exec.LookPath("noise")
	if err != nil {
		t.Skip("noise is not installed")
	}
	h := newHarness(t)
	env := func(family string) []string {
		return append(slices.DeleteFunc(os.Environ(), func(e string) bool { return strings.HasPrefix(e, "NOISE_PROJECT_DIR=") }), "NOISE_PROJECT_DIR="+family)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(noise, append([]string{"tasks"}, args...)...)
		cmd.Dir = h.repo
		cmd.Env = env(h.repo)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("noise tasks %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	h.svc.cfg.Engine = func(family, tag string) taskengine.Engine {
		return taskengine.Engine{
			Binary: noise, Prefix: []string{"tasks"}, Family: family, Tag: tag, Owner: Owner, ClaimTTL: LeaseTTL,
			Env: env(family),
		}
	}
	return h, run
}

// The pinned engine is the authority on what a claim, a run and a worktree
// accept, so one test drives the whole lane through it.
func TestLaneAgainstTheRealEngine(t *testing.T) {
	h, run := realHarness(t)
	run("init")
	run("add", "-title", "Real task", "-description", "d", "-details", "x", "-priority", "high", "-json")

	view := h.start()
	if view.Branch != "tasks/main/1" || view.Phase != PhaseOpen {
		t.Fatalf("view = %+v", view)
	}
	if _, err := os.Stat(filepath.Join(h.repo, ".worktrees", "main-1", ".git")); err != nil {
		t.Fatalf("the engine did not create the worktree: %v", err)
	}
	if shown := run("show", "-json", "-tag", "main", "1"); !strings.Contains(shown, `"in-progress"`) || !strings.Contains(shown, Owner) {
		t.Fatalf("task after start: %s", shown)
	}

	h.now = h.now.Add(h.svc.cfg.RenewEvery)
	h.svc.RenewDue(context.Background())
	stored, _ := h.store.Get(context.Background(), h.source.ID, "main", 1)
	if !stored.RenewedAt.Equal(h.now) || stored.ClaimToken <= 1 || stored.PendingOp != "" {
		t.Fatalf("the engine renewal was not recorded: %+v", stored)
	}

	result, err := h.done(DoneInput{})
	if err != nil || !result.LaneClosed {
		t.Fatalf("Done = %+v, %v", result, err)
	}
	shown := run("show", "-json", "-tag", "main", "1")
	if !strings.Contains(shown, `"status": "done"`) || strings.Contains(shown, `"claim"`) {
		t.Fatalf("task after close: %s", shown)
	}
	if _, err := os.Stat(filepath.Join(h.repo, ".worktrees", "main-1")); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
}

// With several tags, and none of them main, every command of the lane must
// name the tag: the engine's default would pick none or the wrong one.
func TestLaneInAFamilyWithSeveralTagsAndNoMain(t *testing.T) {
	h, run := realHarness(t)
	run("init", "-tag", "alpha")
	store, err := sql.Open("sqlite3", filepath.Join(h.repo, ".git", "noise-tasks", "tasks.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(`INSERT INTO tags (name, next_id, task_count, completed_count) VALUES ('beta', 1, 0, 0)`); err != nil {
		t.Fatalf("add a second tag: %v", err)
	}
	_ = store.Close()
	run("add", "-tag", "alpha", "-title", "First", "-description", "d", "-details", "x", "-priority", "high", "-json")
	run("add", "-tag", "beta", "-title", "Second", "-description", "d", "-details", "x", "-priority", "high", "-json")
	if listed := run("list", "-json", "-tag", "beta"); !strings.Contains(listed, "Second") {
		t.Fatalf("the second tag has no task: %s", listed)
	}

	view, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "beta", TaskID: 1, Agent: "claude"})
	if err != nil {
		t.Fatalf("Start on the second tag: %v", err)
	}
	if view.Branch != "tasks/beta/1" || view.Phase != PhaseOpen {
		t.Fatalf("view = %+v", view)
	}
	result, err := h.svc.Done(context.Background(), h.source.ID, DoneInput{Tag: "beta", TaskID: 1})
	if err != nil || !result.LaneClosed {
		t.Fatalf("Done = %+v, %v", result, err)
	}
	if shown := run("show", "-json", "-tag", "beta", "1"); !strings.Contains(shown, `"status": "done"`) {
		t.Fatalf("task after close: %s", shown)
	}
	if shown := run("show", "-json", "-tag", "alpha", "1"); strings.Contains(shown, `"status": "done"`) || strings.Contains(shown, `"claim"`) {
		t.Fatalf("the other tag's task was touched: %s", shown)
	}
}

// The same lost release against the installed engine: the release commits, its
// answer is dropped, and the next Start must not repeat it with the dead token.
func TestLostReleaseResponseAgainstTheRealEngine(t *testing.T) {
	h, run := realHarness(t)
	run("init")
	run("add", "-title", "Real task", "-description", "d", "-details", "x", "-priority", "high", "-json")
	real := h.svc.cfg.Engine
	dropped := false
	h.svc.cfg.Engine = func(family, tag string) taskengine.Engine {
		withEnv := real(family, tag)
		return taskengine.Engine{
			Family: family, Tag: tag, Owner: Owner, Prefix: []string{"tasks"},
			Run: func(ctx context.Context, args ...string) ([]byte, error) {
				cmd := exec.CommandContext(ctx, "noise", args...)
				cmd.Dir = family
				cmd.Env = withEnv.Env
				out, err := cmd.CombinedOutput()
				if err == nil && len(args) > 1 && args[1] == "release" && !dropped {
					dropped = true
					return nil, errors.New("response lost")
				}
				if err != nil {
					return out, err
				}
				return out, nil
			},
		}
	}
	h.projects.registerErr = errors.New("boom")
	if _, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"}); err == nil || !dropped {
		t.Fatalf("expected the cleanup to lose the release response (dropped %v): %v", dropped, err)
	}
	if shown := run("show", "-json", "-tag", "main", "1"); strings.Contains(shown, `"claim"`) {
		t.Fatalf("the release should have committed: %s", shown)
	}
	h.projects.registerErr = nil
	if view := h.start(); view.Phase != PhaseOpen {
		t.Fatalf("retry = %+v", view)
	}
}
