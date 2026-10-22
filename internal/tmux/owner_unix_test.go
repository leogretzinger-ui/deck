//go:build linux || darwin

package tmux

import (
	"os"
	"syscall"
	"testing"
)

type ownershipInfo struct {
	os.FileInfo
	metadata any
}

func (i ownershipInfo) Sys() any { return i.metadata }

func TestOwnershipRequiresRuntimeUID(t *testing.T) {
	dir := t.TempDir()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyOwnedByRuntimeUser(dir, info); err != nil {
		t.Fatalf("current owner refused: %v", err)
	}
	foreign := *info.Sys().(*syscall.Stat_t)
	foreign.Uid = uint32(os.Getuid()) + 1
	if err := verifyOwnedByRuntimeUser(dir, ownershipInfo{info, &foreign}); err == nil {
		t.Fatal("foreign owner accepted")
	}
	if err := verifyOwnedByRuntimeUser(dir, ownershipInfo{info, nil}); err == nil {
		t.Fatal("missing ownership metadata accepted")
	}
}
