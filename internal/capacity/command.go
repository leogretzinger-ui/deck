package capacity

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

const maxCommandOutput = 1 << 20

var errOutputLimit = errors.New("capacity: provider output exceeded limit")

type commandRunner func(context.Context, string, ...string) ([]byte, error)

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		return 0, errOutputLimit
	}
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		return remaining, errOutputLimit
	}
	return b.Buffer.Write(p)
}

func runCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	stdout := &limitedBuffer{limit: maxCommandOutput}
	stderr := &limitedBuffer{limit: maxCommandOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}
