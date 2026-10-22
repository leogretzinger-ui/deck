//go:build linux

package workspaceruntime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/noisefloor-oss/deck/internal/sealedstate"
	"golang.org/x/sys/unix"
)

func (c *Controller) noAliases(mounts []sealedstate.Mount) error {
	for _, m := range mounts {
		if within(m.MountPoint, c.cfg.MountPoint) || (m.FSType == "fuse.gocryptfs" && m.Source == c.cfg.CipherDir) {
			return ErrUnsafeStorage
		}
	}
	return nil
}

// Check the dedicated owner's observable process namespaces as well as our
// own mount table. A detached bind mount must not become a successful lock
// merely because it disappeared from the controller's namespace. Inaccessible
// owner processes are uncertainty, never absence. This does not claim a proof
// against a malicious host moving processes or mount namespaces behind us.
func (c *Controller) observeClosedProcesses(ctx context.Context) error {
	return c.observeClosedProcessesAt(ctx, "/proc")
}

func (c *Controller) observeClosedProcessesAt(ctx context.Context, procRoot string) error {
	return c.observeClosedProcessesWith(ctx, procRoot, os.Readlink, systemUserManagerPID)
}

func (c *Controller) observeClosedProcessesWith(ctx context.Context, procRoot string, readlink func(string) (string, error), managerPID func(context.Context, int) (int, error)) error {
	gocryptfsPath, err := filepath.EvalSymlinks(c.cfg.GocryptfsPath)
	if err != nil {
		return ErrUnavailable
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return ErrUnavailable
	}
	var supervisor *protectedSupervisor
	var protected []processSnapshot
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return ErrUnavailable
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		root := filepath.Join(procRoot, entry.Name())
		var st unix.Stat_t
		if err := unix.Stat(root, &st); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return ErrUnavailable
		}
		if int(st.Uid) != c.cfg.OwnerUID {
			continue
		}
		// Snapshot identity before the mount observation so the before/after
		// pair below spans the mount read: an exec, reparent, or PID reuse
		// crossing that window is detected instead of being attested under a
		// stale identity. Required only when executable access needs
		// independent supervisor attestation.
		before, identityErr := readProcessSnapshot(procRoot, pid)
		// #nosec G304 -- root contains only a numeric PID from the fixed /proc directory.
		mounts, err := os.Open(filepath.Join(root, "mountinfo"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return ErrUnavailable
		}
		facts, parseErr := sealedstate.ParseMountInfo(mounts)
		_ = mounts.Close()
		if parseErr != nil {
			return ErrUnavailable
		}
		if err := c.noAliases(facts); err != nil {
			return err
		}
		executable, err := readlink(filepath.Join(root, "exe"))
		if err != nil {
			// Zombies have no executable and retain no address space or FUSE key.
			// ENOENT also covers a process that exited during this observation.
			if os.IsNotExist(err) {
				continue
			}
			if errors.Is(err, unix.EACCES) && identityErr == nil {
				if supervisor == nil {
					supervisor, err = attestSupervisor(ctx, procRoot, c.cfg.OwnerUID, managerPID)
					if err != nil {
						return ErrUnavailable
					}
				}
				after, err := readProcessSnapshot(procRoot, pid)
				if err != nil || after != before || !supervisor.contains(before) {
					return ErrUnavailable
				}
				protected = append(protected, before)
				continue
			}
			return ErrUnavailable
		}
		if strings.TrimSuffix(executable, " (deleted)") != gocryptfsPath {
			continue
		}
		// Only gocryptfs argv is read; provider arguments and environment are never
		// collected. The configured volume's daemon must have actually exited.
		// #nosec G304 -- root contains only a numeric PID from the fixed /proc directory.
		args, err := os.ReadFile(filepath.Join(root, "cmdline"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return ErrUnavailable
		}
		for _, arg := range bytes.Split(args, []byte{0}) {
			if string(arg) == c.cfg.CipherDir || string(arg) == c.cfg.MountPoint {
				return ErrUnsafeStorage
			}
		}
	}
	if supervisor != nil {
		if err := supervisor.revalidate(ctx, procRoot, managerPID); err != nil {
			return ErrUnavailable
		}
		for _, before := range protected {
			after, err := readProcessSnapshot(procRoot, before.pid)
			if err != nil || after != before || !supervisor.contains(after) {
				return ErrUnavailable
			}
		}
	}
	return nil
}
