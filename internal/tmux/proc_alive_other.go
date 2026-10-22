//go:build !linux && !darwin

package tmux

import "fmt"

// ProcAlive on unsupported hosts has no kernel-backed implementation: the
// honest answer is an error, never a fabricated dead or alive. Callers must
// refuse any destructive action that would depend on the answer.
func ProcAlive(pid int, startTicks int64) (bool, error) {
	return false, fmt.Errorf("proc liveness is unverifiable on %s", "this platform")
}

// ProcGroup on unsupported hosts fails the same way: no verified identity
// means no signal target may be returned.
func ProcGroup(pid int, startTicks int64) (int, error) {
	return 0, fmt.Errorf("proc group is unverifiable on %s", "this platform")
}
