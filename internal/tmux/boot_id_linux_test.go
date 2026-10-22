//go:build linux

package tmux

import (
	"os"
	"strings"
	"testing"
)

func TestBootIDReadsTheKernelEpoch(t *testing.T) {
	id, err := BootID()
	if err != nil {
		t.Fatalf("BootID: %v", err)
	}
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatalf("read boot_id directly: %v", err)
	}
	if id != strings.TrimSpace(string(raw)) {
		t.Fatalf("BootID = %q, /proc says %q", id, strings.TrimSpace(string(raw)))
	}
	// The epoch is stable within a boot: two reads agree.
	again, err := BootID()
	if err != nil || again != id {
		t.Fatalf("second read = %q, %v; want %q", again, err, id)
	}
}
