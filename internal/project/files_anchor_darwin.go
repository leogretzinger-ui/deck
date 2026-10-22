//go:build darwin

package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// O_SEARCH is O_EXEC | O_DIRECTORY in Darwin's fcntl.h; x/sys omits O_EXEC.
const directorySearchFlag = 0x40000000

func openProjectRoot(projectRoot string, afterAnchor func()) (*os.Root, error) {
	if !filepath.IsAbs(projectRoot) || filepath.Clean(projectRoot) != projectRoot {
		return nil, fmt.Errorf("%w: project root is not canonical", ErrOutsideProject)
	}
	flags := directorySearchFlag
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
		flags = directorySearchFlag
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

	// Darwin cannot bind through /dev/fd with O_DIRECTORY. Reopen the path
	// and verify it still names the acquired directory before using it.
	root, err := os.OpenRoot(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("bind project root anchor: %w", err)
	}
	rootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(anchorInfo, rootInfo) {
		_ = root.Close()
		return nil, fmt.Errorf("%w: project root descriptor identity changed", ErrOutsideProject)
	}
	if afterAnchor != nil {
		afterAnchor()
	}
	return root, nil
}

func renameNoReplace(pfd int, staged, leaf string) error {
	for {
		err := unix.RenameatxNp(pfd, staged, pfd, leaf, unix.RENAME_EXCL)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}
