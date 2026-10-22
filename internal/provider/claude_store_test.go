package provider

import (
	"errors"
	"testing"
)

func TestClaudeCurrentStoreRootResolution(t *testing.T) {
	d := mustClaudeHook(t)
	t.Setenv("HOME", "/home/op")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	root, err := d.CurrentStoreRoot()
	if err != nil || root != "/home/op/.claude" {
		t.Fatalf("HOME resolution = %q err=%v, want /home/op/.claude", root, err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/srv/claude-store/")
	root, err = d.CurrentStoreRoot()
	if err != nil || root != "/srv/claude-store" {
		t.Fatalf("CLAUDE_CONFIG_DIR resolution = %q err=%v, want /srv/claude-store", root, err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("HOME", "")
	if _, err := d.CurrentStoreRoot(); !errors.Is(err, ErrStoreRootUnresolvable) {
		t.Fatalf("storeless environment = %v, want ErrStoreRootUnresolvable", err)
	}
}

func TestClaudeStorePinEnvironment(t *testing.T) {
	d := mustClaudeHook(t)
	got := d.StorePinEnvironment("/srv/old-store")
	if len(got) != 1 || got[0] != "CLAUDE_CONFIG_DIR=/srv/old-store" {
		t.Fatalf("pin environment = %v", got)
	}
}
