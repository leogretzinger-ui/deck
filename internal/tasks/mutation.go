package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	addTaskTimeout      = 30 * time.Second
	addTaskMaxFieldSize = 64 << 10
)

var ErrInvalidTask = errors.New("taskmaster: invalid task")

type AddTaskInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Details     string `json:"details"`
	Priority    string `json:"priority"`
}

type AddTaskResult struct {
	ID string `json:"id"`
}

type addTaskExitError struct {
	err error
}

func (e *addTaskExitError) Error() string { return "taskmaster: noise tasks add exited unsuccessfully" }
func (e *addTaskExitError) Unwrap() error { return e.err }

func validateAddTask(input AddTaskInput) (AddTaskInput, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	input.Details = strings.TrimSpace(input.Details)
	input.Priority = strings.TrimSpace(input.Priority)
	if input.Title == "" {
		return AddTaskInput{}, fmt.Errorf("%w: title is required", ErrInvalidTask)
	}
	for name, value := range map[string]string{
		"title": input.Title, "description": input.Description, "details": input.Details,
	} {
		if len(value) > addTaskMaxFieldSize {
			return AddTaskInput{}, fmt.Errorf("%w: %s is too long", ErrInvalidTask, name)
		}
	}
	switch input.Priority {
	case "high", "medium", "low":
	default:
		return AddTaskInput{}, fmt.Errorf("%w: priority must be high, medium, or low", ErrInvalidTask)
	}
	return input, nil
}

func addTaskEngineArgv(input AddTaskInput) []string {
	return []string{
		"tasks", "add",
		"-title", input.Title,
		"-description", input.Description,
		"-details", input.Details,
		"-priority", input.Priority,
		"-json",
	}
}

// AddTask invokes the pinned Tasks engine against the repository-family
// store its schema-4 context selected. Values occupy argv entries
// only; there is no shell, PATH lookup, browser-owned write, or schema
// replica. The engine's working directory is the anchored project root, so
// store discovery binds to the registered project exactly as its context did.
func (h *Handler) addTask(parent context.Context, projectID string, raw AddTaskInput) (*AddTaskResult, error) {
	input, err := validateAddTask(raw)
	if err != nil {
		return nil, err
	}
	p, err := h.projects.Get(parent, projectID)
	if err != nil {
		return nil, err
	}
	root, err := openEngineTaskRoot(parent, h.executable, p.CanonicalPath, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if !root.initialized {
		return nil, ErrNotTaskmasterProject
	}
	if !root.writable {
		return nil, ErrTaskStateReadOnly
	}
	ctx, cancel := context.WithTimeout(parent, addTaskTimeout)
	defer cancel()
	cmd, err := newTaskCommand(ctx, h.executable, root, addTaskEngineArgv(input)...)
	if err != nil {
		return nil, err
	}
	cmd.Env = withoutEnvironment(os.Environ(), "NOISE_PROJECT_DIR")
	var stdout, stderr boundedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("taskmaster: noise tasks add canceled: %w", ctx.Err())
		}
		return nil, &addTaskExitError{err: err}
	}
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("taskmaster: noise tasks add output exceeded limit")
	}
	var result AddTaskResult
	dec := json.NewDecoder(bytes.NewReader(stdout.buf.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil || result.ID == "" {
		return nil, errors.New("taskmaster: noise tasks add returned invalid JSON")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("taskmaster: noise tasks add returned trailing data")
	}
	return &result, nil
}
