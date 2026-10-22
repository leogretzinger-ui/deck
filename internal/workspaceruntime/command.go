package workspaceruntime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
)

type commandRunner interface {
	run(context.Context, string, []string, []string, []byte, bool) ([]byte, error)
}

type execRunner struct{}

func (execRunner) run(ctx context.Context, path string, args, env []string, input []byte, capture bool) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(input)
	// Real file descriptors avoid pipe-copy goroutines waiting indefinitely
	// on a daemonized gocryptfs child. Its init output can include a master key.
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, ErrCommand
	}
	defer func() { _ = null.Close() }()
	cmd.Stdout, cmd.Stderr = null, null
	var output boundedOutput
	if capture {
		cmd.Stdout = &output
	}
	err = cmd.Run()
	if output.overflow {
		return nil, ErrCommand
	}
	return output.Bytes(), err
}

type boundedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64*1024 {
		b.overflow = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func exitCode(err error) int {
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}
