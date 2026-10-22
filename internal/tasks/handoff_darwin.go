//go:build darwin

package tasks

import "os"

func generationHandoffExecutable() (string, error) { return os.Executable() }

func handoffSocketDirectory(dir *os.File) string { return dir.Name() }
