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

const (
	taskEngineReadTimeout     = 15 * time.Second
	taskEngineListOutputLimit = 64 << 20
)

func runTaskEngine(parent context.Context, executable string, root *taskRoot, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, taskEngineReadTimeout)
	defer cancel()
	argv := append([]string{"tasks"}, args...)
	cmd, err := newTaskCommand(ctx, executable, root, argv...)
	if err != nil {
		return nil, err
	}
	cmd.Env = withoutEnvironment(os.Environ(), "NOISE_PROJECT_DIR")
	stdout := boundedBuffer{limit: taskEngineListOutputLimit}
	var stderr boundedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("taskmaster: Tasks command canceled: %w", ctx.Err())
		}
		return nil, errors.New("taskmaster: Tasks command exited unsuccessfully")
	}
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("taskmaster: Tasks command output exceeded limit")
	}
	return stdout.buf.Bytes(), nil
}

type liveListResult struct {
	Tag    string         `json:"tag"`
	Total  int            `json:"total"`
	Counts map[string]int `json:"counts"`
	Tasks  []Task         `json:"tasks"`
}

func readLiveTasks(ctx context.Context, executable string, root *taskRoot) (map[string]TagFile, error) {
	raw, err := runTaskEngine(ctx, executable, root, "list", "--json")
	if err != nil {
		return nil, err
	}
	var shape map[string]json.RawMessage
	if err := decodeOneJSON(raw, &shape); err != nil {
		return nil, errors.New("taskmaster: Tasks list returned invalid JSON")
	}
	for _, key := range []string{"tag", "total", "counts", "tasks"} {
		if shape[key] == nil {
			return nil, errors.New("taskmaster: Tasks list returned an unsupported shape")
		}
	}
	if len(shape) != 4 {
		return nil, errors.New("taskmaster: Tasks list returned an unsupported shape")
	}
	var result liveListResult
	if err := json.Unmarshal(raw, &result); err != nil || result.Tag == "" || result.Counts == nil || result.Tasks == nil || result.Total != len(result.Tasks) {
		return nil, errors.New("taskmaster: Tasks list returned an invalid result")
	}
	for i := range result.Tasks {
		if result.Tasks[i].Revision <= 0 {
			return nil, errors.New("taskmaster: Tasks list omitted a task revision")
		}
		if result.Tasks[i].Claim != nil && result.Tasks[i].Claim.Owner == "" {
			return nil, errors.New("taskmaster: Tasks list returned an invalid claim")
		}
	}
	return map[string]TagFile{result.Tag: {Tasks: result.Tasks}}, nil
}

func decodeOneJSON(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(out); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}
