package lane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/project"
	"github.com/noisefloor-oss/deck/internal/taskengine"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// stubEngine stands in for the Tasks engine. It keeps the same state the
// engine does for one task, enforces the fencing tokens, rotates them on
// renewal, and creates a real git worktree on provision.
type stubEngine struct {
	t    *testing.T
	repo string

	mu      sync.Mutex
	calls   []string
	fail    map[string]error
	counter int64

	status       string
	revision     int64
	claimOwner   string
	claimToken   int64
	runID        int64
	runStatus    string
	attemptToken int64
	worktree     string
	otherClaim   bool
	afterBind    func(path string)

	// loseResponse makes a command commit and then report failure, once.
	loseResponse map[string]int
	// replays remembers each request id with the inputs it first carried and
	// the receipt it returned, as the engine does.
	replays map[string]replay
}

type replay struct {
	inputs  string
	receipt []byte
}

func newStubEngine(t *testing.T, repo string) *stubEngine {
	return &stubEngine{t: t, repo: repo, status: "pending", revision: 1, fail: map[string]error{},
		loseResponse: map[string]int{}, replays: map[string]replay{}}
}

func (e *stubEngine) factory(family, tag string) taskengine.Engine {
	return taskengine.Engine{Family: family, Tag: tag, Owner: Owner, Run: e.run}
}

// log is the engine's own calls; logAll adds the session holds around them.
func (e *stubEngine) log() []string {
	var out []string
	for _, call := range e.logAll() {
		if call != "hold sessions" && call != "release sessions" {
			out = append(out, call)
		}
	}
	return out
}

func (e *stubEngine) logAll() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func (e *stubEngine) reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = nil
}

func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func (e *stubEngine) token() int64 { e.counter++; return e.counter }

func refusal(kind string, extra string) ([]byte, error) {
	body := fmt.Sprintf("{\n  \"kind\": %q%s\n}", kind, extra)
	return []byte(body), errors.New("exit status 3")
}

func (e *stubEngine) note(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, event)
}

func (e *stubEngine) run(ctx context.Context, args ...string) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out, err := e.exec(ctx, args...)
	command := args[0]
	if command == "run" || command == "worktree" {
		command += " " + args[1]
	}
	if err == nil && e.loseResponse[command] > 0 {
		e.loseResponse[command]--
		return nil, errors.New("response lost")
	}
	return out, err
}

// replayed answers a repeated request id from the first receipt, and refuses
// it when the inputs differ.
func (e *stubEngine) replayed(args []string, inputs string) ([]byte, error, bool) {
	prior, ok := e.replays[flagValue(args, "-request-id")]
	if !ok {
		return nil, nil, false
	}
	if prior.inputs != inputs {
		out, err := refusal("replay-conflict", "")
		return out, err, true
	}
	return prior.receipt, nil, true
}

func (e *stubEngine) remember(args []string, inputs string, receipt []byte) {
	e.replays[flagValue(args, "-request-id")] = replay{inputs: inputs, receipt: receipt}
}

func (e *stubEngine) exec(_ context.Context, args ...string) ([]byte, error) {
	command := args[0]
	if command == "run" || command == "worktree" {
		command += " " + args[1]
	}
	e.calls = append(e.calls, command)
	if err := e.fail[command]; err != nil {
		return nil, err
	}
	intFlag := func(name string) int64 {
		v, _ := strconv.ParseInt(flagValue(args, name), 10, 64)
		return v
	}
	claimOK := func() bool { return e.claimToken != 0 && intFlag("-claim-token") == e.claimToken }
	runOK := func() bool {
		return claimOK() && intFlag("-attempt-token") == e.attemptToken && e.runStatus == "running"
	}
	reply := func(v any) ([]byte, error) { out, _ := json.Marshal(v); return out, nil }
	switch command {
	case "show":
		task := map[string]any{"id": "1", "status": e.status, "revision": e.revision}
		if e.claimToken != 0 || e.otherClaim {
			owner := e.claimOwner
			if e.otherClaim {
				owner = "someone-else"
			}
			task["claim"] = map[string]string{"owner": owner, "acquiredAt": "2026-10-15T00:00:00Z", "expiresAt": "2099-01-01T00:00:00Z"}
		}
		return reply(task)
	case "claim":
		inputs := fmt.Sprint(intFlag("-revision"))
		if out, err, ok := e.replayed(args, inputs); ok {
			return out, err
		}
		if e.otherClaim || e.claimToken != 0 {
			return refusal("claim", "")
		}
		if intFlag("-revision") != e.revision {
			return refusal("stale-write", "")
		}
		e.claimToken, e.claimOwner = e.token(), flagValue(args, "-owner")
		out, _ := reply(map[string]any{"token": e.claimToken, "expiresAt": "2099-01-01T00:00:00Z", "revision": e.revision})
		e.remember(args, inputs, out)
		return out, nil
	case "set-status":
		if intFlag("-revision") != e.revision {
			return refusal("stale-write", "")
		}
		if token := intFlag("-token"); token != 0 && token != e.claimToken {
			return refusal("claim", "")
		}
		if e.otherClaim {
			return refusal("claim", "")
		}
		e.status = args[len(args)-1]
		e.revision++
		return reply(map[string]any{"status": e.status})
	case "run start":
		if flagValue(args, "-tag") == "" {
			return nil, errors.New("run start needs the lane's tag")
		}
		inputs := fmt.Sprint(intFlag("-claim-token"))
		if out, err, ok := e.replayed(args, inputs); ok {
			return out, err
		}
		if !claimOK() {
			return refusal("claim", "")
		}
		e.runID, e.runStatus, e.attemptToken = 1, "running", e.token()
		out, _ := reply(map[string]any{"runId": e.runID, "attempt": 1, "attemptToken": e.attemptToken, "expiresAt": "2099-01-01T00:00:00Z"})
		e.remember(args, inputs, out)
		return out, nil
	case "run renew":
		inputs := fmt.Sprint(intFlag("-claim-token"), "/", intFlag("-attempt-token"))
		if out, err, ok := e.replayed(args, inputs); ok {
			return out, err
		}
		if !runOK() {
			return refusal("run-conflict", "")
		}
		e.claimToken, e.attemptToken = e.token(), e.token()
		out, _ := reply(map[string]any{"claimToken": e.claimToken, "attemptToken": e.attemptToken,
			"claimExpiresAt": "2099-01-01T00:00:00Z", "attemptExpiresAt": "2099-01-01T00:00:00Z"})
		e.remember(args, inputs, out)
		return out, nil
	case "worktree provision":
		inputs := fmt.Sprint(intFlag("-claim-token"), "/", intFlag("-attempt-token"))
		if out, err, ok := e.replayed(args, inputs); ok {
			return out, err
		}
		if !runOK() {
			return refusal("run-conflict", "")
		}
		branch := "tasks/main/1"
		e.worktree = filepath.Join(e.repo, ".worktrees", "main-1")
		if exec.Command("git", "-C", e.repo, "rev-parse", "--verify", "-q", "refs/heads/"+branch).Run() == nil {
			return refusal("worktree-conflict", ", \"reason\": \"branch-exists\"")
		}
		git(e.t, e.repo, "worktree", "add", "-q", "-b", branch, e.worktree)
		if e.afterBind != nil {
			e.afterBind(e.worktree)
		}
		out, _ := reply(map[string]any{"status": "bound", "runId": e.runID, "path": e.worktree, "branch": branch,
			"baseCommit": git(e.t, e.repo, "rev-parse", "HEAD")})
		e.remember(args, inputs, out)
		return out, nil
	case "worktree remove":
		if !runOK() {
			return refusal("run-conflict", "")
		}
		if out, err := exec.Command("git", "-C", e.repo, "worktree", "remove", e.worktree).CombinedOutput(); err != nil {
			return refusal("worktree-conflict", fmt.Sprintf(", \"reason\": \"dirty\", \"detail\": %q", out))
		}
		e.worktree = ""
		return reply(map[string]any{"status": "removed", "runId": e.runID})
	case "run finish":
		inputs := fmt.Sprint(intFlag("-revision"), "/", flagValue(args, "-task-status"))
		if out, err, ok := e.replayed(args, inputs); ok {
			return out, err
		}
		if !runOK() {
			return refusal("run-conflict", "")
		}
		if e.worktree != "" {
			return refusal("worktree-conflict", "")
		}
		if flagValue(args, "-task-status") != "" {
			if intFlag("-revision") != e.revision {
				return refusal("stale-write", "")
			}
			e.status = flagValue(args, "-task-status")
			e.revision++
		}
		e.runStatus, e.claimToken = "succeeded", 0
		out, _ := reply(map[string]any{"runStatus": "succeeded"})
		e.remember(args, inputs, out)
		return out, nil
	case "run cancel":
		if !runOK() {
			return refusal("run-conflict", "")
		}
		e.runStatus = "cancelled"
		return reply(map[string]any{"runStatus": "cancelled"})
	case "release":
		if e.runStatus == "running" || intFlag("-token") != e.claimToken {
			return refusal("release", "")
		}
		e.claimToken = 0
		return reply(map[string]any{"id": "1"})
	}
	return nil, fmt.Errorf("stub engine: unexpected command %q", command)
}

// fakeProjects wraps the real registry so a test can control the live
// sessions and break registration.
type fakeProjects struct {
	*project.Repository
	live        []project.LiveSession
	registerErr error
}

func (f *fakeProjects) LiveSessions(context.Context, string) ([]project.LiveSession, error) {
	return f.live, nil
}

func (f *fakeProjects) Register(ctx context.Context, name, path string, roots []string, id project.GitIdentity) (*project.Project, *project.ReplacedGitIdentity, error) {
	if f.registerErr != nil {
		return nil, nil, f.registerErr
	}
	return f.Repository.Register(ctx, name, path, roots, id)
}

type harness struct {
	t         *testing.T
	svc       *Service
	store     *Store
	engine    *stubEngine
	projects  *fakeProjects
	source    *project.Project
	repo      string
	now       time.Time
	gh        func(args ...string) ([]byte, error)
	stopErr   error
	afterStop func()
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "src")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	d, err := db.Open(context.Background(), db.Config{Path: filepath.Join(root, "control.db")})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	repository := project.NewRepository(d)
	source, _, err := repository.Register(context.Background(), "src", repo, []string{root},
		project.GitIdentity{Name: "Test", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register source: %v", err)
	}
	h := &harness{
		t: t, store: NewStore(d), engine: newStubEngine(t, repo),
		projects: &fakeProjects{Repository: repository}, source: source, repo: repo,
		now: time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC),
	}
	h.svc = NewService(Config{
		Store: h.store, Projects: h.projects, Engine: h.engine.factory,
		RemoveProject: func(ctx context.Context, id string) error { return repository.Remove(ctx, id) },
		HoldSessions: func(string) func() {
			h.engine.note("hold sessions")
			return func() { h.engine.note("release sessions") }
		},
		StopSessions: func(context.Context, string) error {
			h.engine.note("stop sessions")
			h.projects.live = nil
			if h.afterStop != nil {
				h.afterStop()
			}
			return h.stopErr
		},
		AllowedRoots: []string{root}, Log: zerolog.Nop(),
		Now: func() time.Time { return h.now },
		GH: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if h.gh == nil {
				return nil, errors.New("gh unavailable")
			}
			return h.gh(args...)
		},
	})
	return h
}

func (h *harness) start() *View {
	h.t.Helper()
	view, err := h.svc.Start(context.Background(), h.source.ID, StartInput{Tag: "main", TaskID: 1, Agent: "claude"})
	if err != nil {
		h.t.Fatalf("Start: %v", err)
	}
	return view
}

func (h *harness) done(in DoneInput) (*DoneResult, error) {
	in.Tag, in.TaskID = "main", 1
	return h.svc.Done(context.Background(), h.source.ID, in)
}

// errEngineRefusal is an answer from the engine that the command did not commit.
var errEngineRefusal = errors.New("noise-tasks: {\n  \"kind\": \"boom\"\n}")
