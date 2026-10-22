package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

const initializeTimeout = 30 * time.Second

// InitializeResult is the engine's report of the store it created and the
// tag it opened. Both are echoed back so the caller can show which store a
// project bound to: a worktree binds to its repository family's store, which
// is not under the project boundary.
type InitializeResult struct {
	Path        string `json:"path"`
	Tag         string `json:"tag"`
	AlreadyDone bool   `json:"already_initialized"`
}

type initializeExitError struct {
	err error
}

func (e *initializeExitError) Error() string {
	return "taskmaster: noise tasks init exited unsuccessfully"
}
func (e *initializeExitError) Unwrap() error { return e.err }

// initialize creates task state for a project that has none. A backlog is
// the only prerequisite for using tasks: a PRD is one way to fill a backlog,
// never a condition of having one, so this path takes no document and
// produces an empty board the user can add to directly.
//
// It is idempotent. An already-initialized family reports its existing store
// rather than failing, so a repeated click, a retried request, or two panes
// racing the same project all settle on the same answer.
func (h *Handler) initialize(parent context.Context, projectID string) (*InitializeResult, error) {
	p, err := h.projects.Get(parent, projectID)
	if err != nil {
		return nil, err
	}
	root, err := openEngineTaskRoot(parent, h.executable, p.CanonicalPath, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if !root.writable {
		return nil, ErrTaskStateReadOnly
	}
	if root.initialized {
		return &InitializeResult{Path: root.storePath, AlreadyDone: true}, nil
	}
	ctx, cancel := context.WithTimeout(parent, initializeTimeout)
	defer cancel()
	cmd, err := newTaskCommand(ctx, h.executable, root, "tasks", "init", "-json")
	if err != nil {
		return nil, err
	}
	cmd.Env = withoutEnvironment(os.Environ(), "NOISE_PROJECT_DIR")
	var stdout, stderr boundedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("taskmaster: noise tasks init canceled: %w", ctx.Err())
		}
		// The engine refuses to create a store that already exists, and the
		// check above cannot hold across the invocation: two first-time posts
		// both read an uninitialized family, one creates it, and the other
		// arrives to find it there. That loser asked for task state to exist
		// and it does, so it gets the same answer as any other repeat.
		//
		// The post-state decides this, never the exit status or the message:
		// a failure that did NOT leave a store is still a failure.
		if settled, serr := h.initializedStore(parent, p.CanonicalPath); serr == nil && settled != "" {
			return &InitializeResult{Path: settled, AlreadyDone: true}, nil
		}
		return nil, &initializeExitError{err: err}
	}
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("taskmaster: noise tasks init output exceeded limit")
	}
	var result InitializeResult
	dec := json.NewDecoder(bytes.NewReader(stdout.buf.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil || result.Path == "" {
		return nil, errors.New("taskmaster: noise tasks init returned invalid JSON")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("taskmaster: noise tasks init returned trailing data")
	}
	return &result, nil
}

// initializedStore reports the store path if the project's family now holds
// task state, and "" if it does not. It is a fresh resolution rather than a
// re-read of the root opened before the engine ran, because that root's view
// predates the invocation this is reconciling.
func (h *Handler) initializedStore(ctx context.Context, projectPath string) (string, error) {
	root, err := openEngineTaskRoot(ctx, h.executable, projectPath, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if !root.initialized {
		return "", nil
	}
	return root.storePath, nil
}
