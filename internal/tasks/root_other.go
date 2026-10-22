//go:build !linux && !darwin

package tasks

import (
	"errors"
	"os"
)

var errRootedTaskAccessUnsupported = errors.New("rooted task access is unsupported on this platform")

func openTaskRoot(string, func()) (*taskRoot, error) {
	return nil, errors.Join(ErrUnsafeTree, errRootedTaskAccessUnsupported)
}

func (r *taskRoot) Close() error           { return nil }
func (r *taskRoot) descriptorPath() string { return "" }
func descriptorPath(*os.File) string       { return "" }
func openTaskRootDirectory(*os.Root, string) (*os.File, error) {
	return nil, errRootedTaskAccessUnsupported
}
