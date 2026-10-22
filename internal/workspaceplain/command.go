package workspaceplain

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type commandRunner interface {
	run(context.Context, string, []string, []string, bool) ([]byte, error)
}
type execRunner struct{}

func (execRunner) run(ctx context.Context, path string, args, env []string, capture bool) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.WaitDelay = time.Second
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, ErrCommand
	}
	defer func() { _ = null.Close() }()
	cmd.Stdout, cmd.Stderr = null, null
	var out boundedOutput
	if capture {
		cmd.Stdout = &out
	}
	err = cmd.Run()
	if out.overflow {
		return nil, ErrCommand
	}
	return out.Bytes(), err
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
	var e interface{ ExitCode() int }
	if errors.As(err, &e) {
		return e.ExitCode()
	}
	return -1
}

func (c *Controller) environment() []string {
	return []string{"PATH=" + filepath.Dir(c.cfg.PodmanPath) + ":/usr/local/bin:/usr/bin:/bin", "HOME=" + c.cfg.HomeDir, "XDG_RUNTIME_DIR=" + c.cfg.RuntimeDir, "LANG=C.UTF-8",
		"COMPOSE_PROJECT_NAME=" + c.cfg.ComposeProject, "NOISEDECK_PLAIN_STATE_ROOT=" + c.cfg.StateRoot, "NOISEDECK_PLAIN_INSTANCE=" + c.cfg.InstanceID,
		"NOISEDECK_PLAIN_CONTAINER=" + c.cfg.AppContainer, "NOISEDECK_PUBLIC_ORIGIN=" + c.cfg.PublicOrigin, "NOISEDECK_WORKSPACE_GATEWAY=true"}
}
func (c *Controller) command(ctx context.Context, path string, args []string, capture bool) ([]byte, error) {
	out, err := c.run.run(ctx, path, args, c.environment(), capture)
	if err != nil {
		return nil, ErrCommand
	}
	return out, nil
}
