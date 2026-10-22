//go:build !darwin

package project

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

func newCloneCommand(ctx context.Context, git, url, destination string, dir *os.File) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, git, "clone", "--progress", "--", url, ".")
	cmd.Dir = workingDirectoryOf(dir, destination)
	return cmd, nil
}

func RunNativeCloneExec([]string) error {
	return errors.New("native clone helper is unavailable on this platform")
}
