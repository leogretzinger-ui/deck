//go:build !linux && !darwin

package tmux

import "fmt"

// BootID on unsupported hosts returns an error, never a fabricated epoch.
// Callers refuse to launch rather than record an identity the platform cannot
// verify.
func BootID() (string, error) {
	return "", fmt.Errorf("boot id is unverifiable on %s", "this platform")
}
