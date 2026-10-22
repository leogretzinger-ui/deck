//go:build !darwin

package tasks

import (
	"context"
	"errors"
	"os/exec"
)

func newTaskCommand(ctx context.Context, executable string, root *taskRoot, args ...string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = root.descriptorPath()
	return cmd, nil
}

func RunNativeTaskExec([]string) error {
	return errors.New("native Tasks execution is unsupported on this platform")
}
