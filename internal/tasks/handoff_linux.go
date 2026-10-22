//go:build linux

package tasks

import (
	"fmt"
	"os"
)

func generationHandoffExecutable() (string, error) {
	return fmt.Sprintf("/proc/%d/exe", os.Getpid()), nil
}

func handoffSocketDirectory(dir *os.File) string { return descriptorPath(dir) }
