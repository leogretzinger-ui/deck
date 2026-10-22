//go:build linux

package workspaceplain

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// This opt-in probe runs the real fixed wrapper and Podman on disposable data.
// It does not qualify Google, browser grants or real provider interruption.
func TestNativeHostPlainRuntime(t *testing.T) {
	if os.Getenv("NOISEDECK_PLAIN_HOST_TEST") != "1" {
		t.Skip("real rootless Podman host qualification not enabled")
	}
	path := os.Getenv("NOISEDECK_PLAIN_HOST_CONFIG")
	// #nosec G304 -- Explicit SRE-owned disposable installation configuration.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("disposable host configuration unavailable")
	}
	var cfg Config
	if json.Unmarshal(b, &cfg) != nil || !strings.HasPrefix(cfg.ComposeProject, "deck-plain-test-") || !strings.HasPrefix(cfg.AppContainer, "deck-plain-test-") {
		t.Fatal("requires dedicated deck-plain-test project and container")
	}
	for _, p := range []string{cfg.StateRoot, cfg.ControlDir} {
		entries, e := os.ReadDir(p)
		if e != nil || len(entries) != 0 {
			t.Fatal("host probe requires empty disposable state and control directories")
		}
	}
	c, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if c != nil {
			_ = c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	state, err := c.Observe(ctx)
	if err != nil || state.Configured || state.Runtime != "stopped" {
		t.Fatal("disposable project is not absent")
	}
	// Cleanup is limited to the exact previously absent test container. The
	// controller itself never has stop authority in unencrypted mode.
	env := c.environment()
	defer func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if _, e := (execRunner{}).run(cleanupCtx, cfg.PodmanPath, []string{"rm", "--force", cfg.AppContainer}, env, false); e != nil {
			t.Error("disposable container cleanup failed")
		}
	}()
	op, err := c.Start(ctx, "host_probe_start")
	if err != nil {
		t.Fatal(err)
	}
	for op.State.Runtime != "running" {
		select {
		case <-ctx.Done():
			t.Fatal("runtime readiness unconfirmed")
		case <-time.After(time.Second):
		}
		op, err = c.Operation(ctx, "host_probe_start")
		if err != nil {
			t.Fatal(err)
		}
	}
	if op.Status != "succeeded" {
		t.Fatal("observed running start did not complete")
	}
	epoch := op.State.Epoch
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	op, err = c.Start(ctx, "host_probe_start")
	if err != nil || op.State.Epoch != epoch || op.State.Runtime != "running" {
		t.Fatal("gateway restart replaced or lost the app process")
	}
}
