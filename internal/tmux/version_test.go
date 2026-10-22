package tmux

import "testing"

func TestSupportedVersion(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   bool
	}{
		{"tmux 3.4", true}, {"tmux 3.6a\n", true}, {"tmux 4.0", true},
		{"tmux next-3.6", true}, {"tmux 3.5-rc", true}, {"tmux 3.5-rc2\n", true},
		{"tmux 3.3", false}, {"tmux 3.3a", false}, {"tmux 2.9", false}, {"tmux next-3.3", false}, {"tmux 3.5-rcx", false},
		{"tmux 999999999999999999999.4", false}, {"3.6a", false}, {"tmux 3.4broken", false},
	} {
		if got := SupportedVersion(tc.output); got != tc.want {
			t.Errorf("SupportedVersion(%q) = %v, want %v", tc.output, got, tc.want)
		}
	}
}
