package tasks

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	generationHandoffCommand = "__task-generation-handoff"
	generationPRDFD          = 10
	handoffTimeout           = 10 * time.Second
)

// maxGenerateTasks bounds the optional task count to the engine's own range.
const maxGenerateTasks = 50

// generationEngineArgv is the engine command line. numTasks 0 leaves the
// engine's default count in force.
func generationEngineArgv(engine string, numTasks int, goos string) []string {
	argv := []string{engine, "tasks", "generate", "--yes"}
	if numTasks > 0 {
		argv = append(argv, "-num-tasks", strconv.Itoa(numTasks))
	}
	prd := "/proc/self/fd/10"
	if goos == "darwin" {
		prd = "/dev/fd/10"
	}
	return append(argv, prd)
}

// IsGenerationHandoffCommand keeps the private command spelling owned by
// this package rather than duplicated in the service entry point.
func IsGenerationHandoffCommand(arg string) bool {
	return arg == generationHandoffCommand
}

// RunGenerationHandoff is the private process mode used inside the managed
// tmux pane. It is exported only so cmd/noisedeck can enter the mode before
// normal configuration loading.
func RunGenerationHandoff(args []string) error {
	return runGenerationHandoff(args)
}

type handoffListener struct {
	listener *net.UnixListener
	path     string
	dir      *os.File
}

// handoffSocketName must add no more bytes to the directory path than tmux's
// own "/noisedeck.sock": on macOS the directory is spelled out, so wherever the
// tmux socket path fits in sun_path the handoff socket must fit too.
func handoffSocketName(runID string) string {
	return ".th-" + runID[:10]
}

func newHandoffListener(socketDir, runID string) (*handoffListener, error) {
	dir, err := os.Open(socketDir)
	if err != nil {
		return nil, fmt.Errorf("task generation handoff: open handoff directory: %w", err)
	}
	if err := checkHandoffDirectory(dir); err != nil {
		_ = dir.Close()
		return nil, err
	}
	path := filepath.Join(handoffSocketDirectory(dir), handoffSocketName(runID))
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		_ = dir.Close()
		return nil, fmt.Errorf("task generation handoff: listen for descriptor handoff: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	if err := checkHandoffDirectory(dir); err != nil {
		_ = listener.Close()
		_ = dir.Close()
		return nil, err
	}
	return &handoffListener{listener: listener, path: path, dir: dir}, nil
}

func (h *handoffListener) Close() {
	_ = h.listener.Close()
	if checkHandoffDirectory(h.dir) == nil {
		_ = os.Remove(h.path)
	}
	_ = h.dir.Close()
}

func (h *handoffListener) await(ctx context.Context, root, prd *os.File) (bool, error) {
	if err := checkHandoffDirectory(h.dir); err != nil {
		return false, err
	}
	deadline := time.Now().Add(handoffTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := h.listener.SetDeadline(deadline); err != nil {
		return false, err
	}
	conn, err := h.listener.AcceptUnix()
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, fmt.Errorf("task generation handoff: descriptor connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(deadline); err != nil {
		return false, err
	}
	if err := checkHandoffDirectory(h.dir); err != nil {
		return false, err
	}
	if err := sendHandoffDescriptors(conn, root, prd); err != nil {
		return false, err
	}
	if err := conn.Close(); err != nil {
		return false, err
	}
	ack, err := h.listener.AcceptUnix()
	if err != nil {
		return false, fmt.Errorf("task generation handoff: acknowledgement: %w", err)
	}
	defer func() { _ = ack.Close() }()
	if err := ack.SetDeadline(deadline); err != nil {
		return false, err
	}
	var status [1]byte
	if _, err := io.ReadFull(ack, status[:]); err != nil {
		return false, fmt.Errorf("task generation handoff: read acknowledgement: %w", err)
	}
	return status[0] == 1, nil
}

func signalHandoff(path string, success bool) error {
	conn, err := net.DialTimeout("unix", path, handoffTimeout)
	if err != nil {
		return fmt.Errorf("connect descriptor handoff acknowledgement: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(handoffTimeout)); err != nil {
		return err
	}
	status := byte(0)
	if success {
		status = 1
	}
	if _, err := conn.Write([]byte{status}); err != nil {
		return fmt.Errorf("signal descriptor handoff: %w", err)
	}
	return nil
}
