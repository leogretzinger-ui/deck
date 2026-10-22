//go:build linux

package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func openProjectRoot(projectRoot string, afterAnchor func()) (*os.Root, error) {
	if !filepath.IsAbs(projectRoot) || filepath.Clean(projectRoot) != projectRoot {
		return nil, fmt.Errorf("%w: project root is not canonical", ErrOutsideProject)
	}

	// Acquire each component relative to its already-open parent. A component
	// replaced by a symlink is refused instead of redirecting the walk.
	flags := unix.O_PATH
	if projectRoot == "/" {
		flags = unix.O_RDONLY
	}
	fd, err := openDirectoryAtFlags(unix.AT_FDCWD, "/", flags)
	if err != nil {
		return nil, fmt.Errorf("open project root anchor: %w", err)
	}
	components := strings.Split(strings.TrimPrefix(projectRoot, "/"), "/")
	for i, component := range components {
		if component == "" {
			continue
		}
		flags = unix.O_PATH
		if i == len(components)-1 {
			flags = unix.O_RDONLY
		}
		next, openErr := openDirectoryAtFlags(fd, component, flags)
		closeErr := unix.Close(fd)
		if openErr != nil {
			return nil, fmt.Errorf("%w: project root changed during acquisition: %w", ErrOutsideProject, openErr)
		}
		if closeErr != nil {
			_ = unix.Close(next)
			return nil, fmt.Errorf("close project root component: %w", closeErr)
		}
		fd = next
	}

	anchor := os.NewFile(uintptr(fd), "project-root")
	if anchor == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open project root anchor: invalid file descriptor")
	}
	defer func() { _ = anchor.Close() }()
	anchorInfo, err := anchor.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat project root anchor: %w", err)
	}
	if afterAnchor != nil {
		afterAnchor()
	}

	// Bind through the acquired descriptor instead of reopening its pathname.
	root, err := os.OpenRoot("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		return nil, fmt.Errorf("bind project root anchor: %w", err)
	}
	probe, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("validate project root anchor: %w", err)
	}
	probeInfo, statErr := probe.Stat()
	closeErr := probe.Close()
	if statErr != nil {
		_ = root.Close()
		return nil, fmt.Errorf("validate project root anchor: %w", statErr)
	}
	if closeErr != nil {
		_ = root.Close()
		return nil, fmt.Errorf("close project root probe: %w", closeErr)
	}
	if !os.SameFile(anchorInfo, probeInfo) {
		_ = root.Close()
		return nil, errors.New("validate project root anchor: descriptor identity changed")
	}
	return root, nil
}

func renameNoReplace(pfd int, staged, leaf string) error {
	for {
		err := unix.Renameat2(pfd, staged, pfd, leaf, unix.RENAME_NOREPLACE)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}
