//go:build linux || darwin

package tasks

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func runGenerationHandoff(args []string) error {
	if len(args) != 2 && len(args) != 3 && len(args) != 5 {
		return errors.New("task generation handoff: invalid arguments")
	}
	engine, ackPath := args[0], args[1]
	numTasks := 0
	if len(args) >= 3 {
		count, err := strconv.Atoi(args[2])
		if err != nil || count < 0 || (count == 0 && len(args) == 3) || count > maxGenerateTasks {
			return errors.New("task generation handoff: invalid task count")
		}
		numTasks = count
	}
	env := os.Environ()
	if len(args) == 5 {
		selector := args[3]
		switch selector {
		case "", "claude-cli", "codex", "kimi", "omp":
		default:
			return errors.New("task generation handoff: invalid provider")
		}
		env = withoutEnvironment(env, "NOISE_TASKS_PROVIDER")
		if selector != "" {
			env = append(env, "NOISE_TASKS_PROVIDER="+selector)
		}
		// tmux can replace a pane's PATH. Bind the configured provider's
		// search path only when execing the generation engine.
		env = append(withoutEnvironment(env, "PATH"), "PATH="+args[4])
	}
	if !filepath.IsAbs(engine) || filepath.Clean(engine) != engine {
		return errors.New("task generation handoff: engine path is not canonical")
	}
	if !filepath.IsAbs(ackPath) || filepath.Clean(ackPath) != ackPath {
		return errors.New("task generation handoff: invalid acknowledgement path")
	}

	conn, err := net.DialTimeout("unix", ackPath, handoffTimeout)
	if err != nil {
		return fmt.Errorf("connect descriptor handoff: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(handoffTimeout)); err != nil {
		return err
	}
	root, prd, err := receiveHandoffDescriptors(conn.(*net.UnixConn))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	defer func() { _ = prd.Close() }()
	// The transfer socket may occupy the engine's fixed PRD descriptor.
	if err := conn.Close(); err != nil {
		return err
	}
	acknowledged := false
	defer func() {
		if !acknowledged {
			_ = signalHandoff(ackPath, false)
		}
	}()
	if info, err := root.Stat(); err != nil || !info.IsDir() {
		return errors.New("task generation handoff: project root is not a directory")
	}
	if err := unix.Fchdir(int(root.Fd())); err != nil {
		return fmt.Errorf("task generation handoff: bind working directory: %w", err)
	}

	if info, err := prd.Stat(); err != nil || !info.Mode().IsRegular() {
		return errors.New("task generation handoff: PRD is not a regular file")
	}

	if err := installDescriptor(prd, generationPRDFD); err != nil {
		return fmt.Errorf("task generation handoff: install PRD descriptor: %w", err)
	}
	defer func() { _ = unix.Close(generationPRDFD) }()

	if err := signalHandoff(ackPath, true); err != nil {
		return err
	}
	acknowledged = true
	argv := generationEngineArgv(engine, numTasks, runtime.GOOS)
	runtime.KeepAlive(prd)
	return unix.Exec(engine, argv, env)
}

// installDescriptor duplicates source above the fixed descriptor range
// before replacing the target. Otherwise an inherited descriptor layout
// could close the source before it is retained.
func installDescriptor(source *os.File, target int) error {
	retained, err := duplicateAtLeast(source, target+1)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(retained) }()
	if err := unix.Dup2(retained, target); err != nil {
		return err
	}
	return nil
}

func duplicateAtLeast(file *os.File, minimum int) (int, error) {
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, minimum)
	runtime.KeepAlive(file)
	return fd, err
}

func checkHandoffDirectory(dir *os.File) error {
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0o700 || !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("task generation handoff: socket directory must be owned by you with mode 0700")
	}
	current, err := os.Stat(dir.Name())
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) {
		return errors.New("task generation handoff: socket directory changed")
	}
	return nil
}

func sendHandoffDescriptors(conn *net.UnixConn, root, prd *os.File) error {
	rights := unix.UnixRights(int(root.Fd()), int(prd.Fd()))
	n, oobn, err := conn.WriteMsgUnix([]byte{1}, rights, nil)
	runtime.KeepAlive(root)
	runtime.KeepAlive(prd)
	if err != nil {
		return fmt.Errorf("send generation descriptors: %w", err)
	}
	if n != 1 || oobn != len(rights) {
		return errors.New("task generation handoff: incomplete descriptor transfer")
	}
	return nil
}

func receiveHandoffDescriptors(conn *net.UnixConn) (*os.File, *os.File, error) {
	status := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(2*4))
	n, oobn, flags, _, err := conn.ReadMsgUnix(status, oob)
	if err != nil {
		return nil, nil, fmt.Errorf("receive generation descriptors: %w", err)
	}
	messages, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return nil, nil, err
	}
	var fds []int
	keep := false
	defer func() {
		if !keep {
			for _, fd := range fds {
				_ = unix.Close(fd)
			}
		}
	}()
	for _, message := range messages {
		rights, err := unix.ParseUnixRights(&message)
		if err != nil {
			return nil, nil, err
		}
		for _, fd := range rights {
			unix.CloseOnExec(fd)
		}
		fds = append(fds, rights...)
	}
	if n != 1 || !bytes.Equal(status, []byte{1}) || flags&unix.MSG_CTRUNC != 0 || len(fds) != 2 {
		return nil, nil, errors.New("task generation handoff: expected root and PRD descriptors")
	}
	keep = true
	return os.NewFile(uintptr(fds[0]), "generation-root"), os.NewFile(uintptr(fds[1]), "generation-prd"), nil
}
