//go:build !unix

package project

import (
	"errors"
	"os"
	"os/exec"
)

const leafOpenFlags = 0

func rootAtDescriptor(*os.File) (*os.Root, error) {
	return nil, errors.New("clone destinations are unsupported on this platform")
}

// killProcessGroup leaves the default cancel in place where process groups
// are not available: the command itself is killed.
func killProcessGroup(*exec.Cmd) {}

// workingDirectoryOf falls back to the path where a descriptor cannot name
// a directory; project file access is unsupported off Linux anyway.
func workingDirectoryOf(_ *os.File, path string) string { return path }
