//go:build linux

package project

import (
	"fmt"
	"os"
	"strings"
)

func cloneHelperRunning(pid int) (bool, error) {
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !strings.Contains(string(status), "State:\tZ"), nil
}
