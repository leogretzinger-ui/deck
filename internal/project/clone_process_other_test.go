//go:build !linux && !darwin

package project

import "errors"

func cloneHelperRunning(int) (bool, error) {
	return false, errors.New("native process observation is unsupported on this platform")
}
