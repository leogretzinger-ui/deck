//go:build darwin

package tmux

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// macOS tmux reports short signal names such as "int" and "term".
func parsePaneSignal(value string) (int, error) {
	if n, err := strconv.Atoi(value); err == nil {
		return n, nil
	}
	name := strings.TrimPrefix(strings.ToUpper(value), "SIG")
	signal := unix.SignalNum("SIG" + name)
	if signal == 0 {
		return 0, fmt.Errorf("unknown signal name %q", value)
	}
	return int(signal), nil
}
