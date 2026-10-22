//go:build !darwin

package tmux

import "strconv"

func parsePaneSignal(value string) (int, error) {
	return strconv.Atoi(value)
}
