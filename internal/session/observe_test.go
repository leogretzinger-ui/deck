package session

import "testing"

func TestRuntimeDiscrepancyKeepsUnknownDistinct(t *testing.T) {
	verified, mismatched := true, false
	tests := []struct {
		name     string
		status   Status
		observed RuntimeObservation
		want     *bool
	}{
		{"running and verified alive", StatusRunning,
			RuntimeObservation{Pane: PaneAlive, Process: ProcessAlive, IdentityVerified: &verified}, boolPtr(false)},
		{"running pane absent", StatusRunning,
			RuntimeObservation{Pane: PaneAbsent, Process: ProcessDead}, boolPtr(true)},
		{"failed pane observation with live process is unknown", StatusRunning,
			RuntimeObservation{Pane: PaneObservationFailed, Process: ProcessAlive}, nil},
		{"terminal row with verified live pane", StatusExited,
			RuntimeObservation{Pane: PaneAlive, Process: ProcessAlive, IdentityVerified: &verified}, boolPtr(true)},
		{"terminal row with orphan process", StatusInterrupted,
			RuntimeObservation{Pane: PaneServerDown, Process: ProcessAlive}, boolPtr(true)},
		{"terminal row after process death", StatusExited,
			RuntimeObservation{Pane: PaneServerDown, Process: ProcessDead}, boolPtr(false)},
		{"identity mismatch", StatusRunning,
			RuntimeObservation{Pane: PaneAlive, Process: ProcessAlive, IdentityVerified: &mismatched}, boolPtr(true)},
		{"converged legacy row without recorded pane", StatusInterrupted,
			RuntimeObservation{Pane: PaneNotRecorded, Process: ProcessNotRecorded}, nil},
		{"starting without a pane", StatusStarting,
			RuntimeObservation{Pane: PaneNotRecorded, Process: ProcessNotRecorded}, boolPtr(false)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := RuntimeDiscrepancy(&Session{Status: test.status}, test.observed)
			if test.want == nil {
				if got != nil {
					t.Fatalf("discrepancy = %v, want unknown", *got)
				}
				return
			}
			if got == nil || *got != *test.want {
				t.Fatalf("discrepancy = %v, want %v", got, *test.want)
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }
