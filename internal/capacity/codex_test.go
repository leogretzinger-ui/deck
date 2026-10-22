package capacity

import "testing"

func TestCodexWindowsKeepNamedBucketsSeparate(t *testing.T) {
	primaryDuration := int64(300)
	weeklyDuration := int64(10080)
	reset := int64(1787196625)
	response := codexUsageResponse{
		RateLimits: codexSnapshot{Secondary: &codexWindow{UsedPercent: 54, WindowDurationMin: &weeklyDuration, ResetsAt: &reset}},
		RateLimitsByLimitID: map[string]codexSnapshot{
			"codex": {Primary: &codexWindow{UsedPercent: 12, WindowDurationMin: &primaryDuration}},
			"fable": {Primary: &codexWindow{UsedPercent: 27, WindowDurationMin: &weeklyDuration}},
		},
	}
	windows := codexWindows(response)
	if len(windows) != 3 {
		t.Fatalf("windows = %#v", windows)
	}
	if windows[0].Scope != "account" || windows[1].Scope != "limit:codex" || windows[2].Scope != "limit:fable" {
		t.Fatalf("windows = %#v", windows)
	}
}
