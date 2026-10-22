package server

import (
	"testing"

	"github.com/noisefloor-oss/deck/internal/session"
)

func TestMapObservedKeepsTmuxServerDownUnknown(t *testing.T) {
	got := mapObserved(session.RuntimeObservation{
		Pane: session.PaneServerDown, Process: session.ProcessAlive,
	})
	if got.PaneState != session.PaneServerDown || got.ProcessState != session.ProcessAlive {
		t.Fatalf("states = pane %q process %q", got.PaneState, got.ProcessState)
	}
	if got.PaneExists != nil || got.PaneAlive != nil {
		t.Fatalf("server-down booleans = exists %v alive %v, want unknown",
			got.PaneExists, got.PaneAlive)
	}
}

func TestMapObservedReportsReachablePaneAbsence(t *testing.T) {
	got := mapObserved(session.RuntimeObservation{Pane: session.PaneAbsent})
	if got.PaneExists == nil || *got.PaneExists || got.PaneAlive == nil || *got.PaneAlive {
		t.Fatalf("absent booleans = exists %v alive %v, want false/false",
			got.PaneExists, got.PaneAlive)
	}
}
