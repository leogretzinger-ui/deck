//go:build darwin

package tmux

import (
	"strconv"
	"syscall"
	"testing"
)

func TestDarwinPaneSignalNames(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  syscall.Signal
	}{
		{"int", syscall.SIGINT},
		{"term", syscall.SIGTERM},
		{"SIGKILL", syscall.SIGKILL},
		{"hup", syscall.SIGHUP},
		{strconv.Itoa(int(syscall.SIGTERM)), syscall.SIGTERM},
	} {
		got, err := parsePaneSignal(tc.value)
		if err != nil || got != int(tc.want) {
			t.Errorf("parsePaneSignal(%q) = %d, %v; want %d", tc.value, got, err, tc.want)
		}
	}
	for _, value := range []string{"", "unknown", "term extra", "2extra"} {
		if _, err := parsePaneSignal(value); err == nil {
			t.Errorf("invalid signal %q accepted", value)
		}
	}
}
