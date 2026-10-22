package project

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// openAllowedPath keeps the allowlist boundary live through the filesystem
// operation, even if a checked directory is replaced by a symlink.
func openAllowedPath(path string, roots []string) (*os.Root, string, error) {
	base, rel, err := allowedPathRoot(path, roots)
	if err != nil {
		return nil, "", err
	}
	root, err := openProjectRoot(base, nil)
	if err != nil {
		return nil, "", err
	}
	return root, rel, nil
}

func allowedPathRoot(path string, roots []string) (string, string, error) {
	if !filepath.IsAbs(path) {
		return "", "", fmt.Errorf("%w: %s", ErrOutsideRoots, path)
	}
	for _, base := range roots {
		rel, err := filepath.Rel(base, path)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		return base, rel, nil
	}
	return "", "", fmt.Errorf("%w: %s", ErrOutsideRoots, path)
}

func allowedStat(path string, roots []string) (os.FileInfo, error) {
	root, rel, err := openAllowedPath(path, roots)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.Stat(rel)
}

func allowedReadDir(path string, roots []string) ([]os.DirEntry, error) {
	root, rel, err := openAllowedPath(path, roots)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return readDirInRoot(root, rel)
}

func readDirInRoot(root *os.Root, rel string) ([]os.DirEntry, error) {
	dir, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(-1)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, err
}

func allowedReadFile(path string, roots []string) ([]byte, error) {
	root, rel, err := openAllowedPath(path, roots)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(rel)
}

func allowedDirectory(path string, roots []string) (*os.Root, error) {
	root, rel, err := openAllowedPath(path, roots)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.OpenRoot(rel)
}
