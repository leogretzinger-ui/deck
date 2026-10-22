package tmux

import (
	"context"
	"testing"
)

// A respawned pane must answer identity probes with the NEW generation's
// launch marker. The marker is read back through #{E:...}, which resolves in
// the SESSION environment and therefore survives the respawn, so respawn-pane's
// own -e cannot republish it — that flag reaches only the spawned process.
// Without the explicit set-environment a respawned pane answers with its
// predecessor's marker, and an in-place recovery would verify against the
// identity of the process it just replaced.
func TestRespawnRepublishesLaunchMarker(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	before := mustCreate(t, c, spec("markresp", "/bin/false"))
	if before.LaunchMarker == "" {
		t.Fatal("create did not stamp a launch marker")
	}

	next := spec("markresp", "/bin/sleep", "300")
	next.Generation = 2
	next.Marker = "marker-gen2"
	after, err := c.RespawnPane(ctx, before.PaneID, next)
	if err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}
	if after.LaunchMarker != "marker-gen2" {
		t.Fatalf("launch marker = %q, want marker-gen2 (stale: %q)",
			after.LaunchMarker, before.LaunchMarker)
	}
}
