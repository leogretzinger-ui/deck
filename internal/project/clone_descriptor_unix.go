//go:build unix && !darwin

package project

import (
	"fmt"
	"os"
)

func rootAtDescriptor(dir *os.File) (*os.Root, error) {
	return os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", dir.Fd()))
}

func workingDirectoryOf(dir *os.File, _ string) string {
	return fmt.Sprintf("/proc/self/fd/%d", dir.Fd())
}
