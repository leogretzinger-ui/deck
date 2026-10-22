package session

import (
	"strings"
	"testing"

	"github.com/noisefloor-oss/deck/internal/tmux"
)

// The persisted reason is the only evidence this refusal leaves, so each
// member names itself, and the case where no member disagrees — an identity
// never read completely — says that rather than claiming a replacement.
func TestSpawnOriginDivergenceNamesTheMember(t *testing.T) {
	live := func(mutate func(*tmux.PaneInfo)) *tmux.PaneInfo {
		info := &tmux.PaneInfo{
			PID: 4242, ProcessGroupID: 4242, StartTicks: 900,
			SpawnPID: 4242, SpawnProcessGroupID: 4242, SpawnStartTicks: 900,
		}
		mutate(info)
		return info
	}

	for _, tc := range []struct {
		name string
		info *tmux.PaneInfo
		want []string
	}{
		{
			name: "a moved pid",
			info: live(func(i *tmux.PaneInfo) { i.PID = 4343 }),
			want: []string{"pid 4343 != spawn pid 4242"},
		},
		{
			name: "a process group that never settled",
			info: live(func(i *tmux.PaneInfo) { i.ProcessGroupID = 7 }),
			want: []string{"pgid 7 != spawn pgid 4242"},
		},
		{
			name: "a start time from a recycled pid",
			info: live(func(i *tmux.PaneInfo) { i.StartTicks = 1200 }),
			want: []string{"start ticks 1200 != spawn start ticks 900"},
		},

		{
			name: "an identity that was never read completely",
			info: &tmux.PaneInfo{PID: 4242, SpawnPID: 4242},
			want: []string{"identity incomplete", "pid 4242", "pgid 0", "start ticks 0"},
		},
		{
			name: "a dead pane keeps only its leader pid",
			info: &tmux.PaneInfo{PID: 4343, SpawnPID: 4242, Dead: true},
			want: []string{"pid 4343 != spawn pid 4242"},
		},
		{
			name: "no observation at all",
			info: nil,
			want: []string{"no pane observation"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := spawnOriginDivergence(tc.info)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("reason %q does not name %q", got, want)
				}
			}
		})
	}

	// Divergent members are reported in MatchesSpawnOrigin's order: pid,
	// process group, then start ticks.
	all := live(func(i *tmux.PaneInfo) { i.PID, i.ProcessGroupID, i.StartTicks = 1, 2, 3 })
	if got, want := spawnOriginDivergence(all),
		"pid 1 != spawn pid 4242, pgid 2 != spawn pgid 4242, start ticks 3 != spawn start ticks 900"; got != want {
		t.Errorf("every member at once:\n got %q\nwant %q", got, want)
	}

	// A dead pane has no live members to compare, so reporting them would
	// invent a disagreement out of the zeros it legitimately carries.
	dead := &tmux.PaneInfo{PID: 4343, SpawnPID: 4242, SpawnProcessGroupID: 4242, SpawnStartTicks: 900, Dead: true}
	if got := spawnOriginDivergence(dead); strings.Contains(got, "pgid") || strings.Contains(got, "start ticks") {
		t.Errorf("dead pane reason %q reports live members", got)
	}
}
