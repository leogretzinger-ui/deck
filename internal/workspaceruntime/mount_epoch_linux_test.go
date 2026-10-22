//go:build linux

package workspaceruntime

import (
	"context"
	"errors"
	"testing"
)

func TestMountEpochRejectsRecycledMountIdentity(t *testing.T) {
	before := epoch("boot", 123, 1000, "0:40", "absent")
	if before == epoch("boot", 123, 1001, "0:40", "absent") {
		t.Fatal("recycled mountinfo ID and device reused authorization epoch")
	}
	if before != epoch("boot", 123, 1000, "0:40", "absent") {
		t.Fatal("unchanged kernel mount identity changed across observations")
	}
}

func TestMissingUniqueMountIdentityRefusesObservation(t *testing.T) {
	f := newFixture(t)
	f.facts.MountInstance = 0
	s, err := f.c.Observe(context.Background())
	if !errors.Is(err, ErrUnavailable) || s.Epoch != "" {
		t.Fatalf("missing identity produced usable observation: %+v %v", s, err)
	}
}

func TestNativeMountIdentityIsStable(t *testing.T) {
	path := t.TempDir()
	a, err := readKernelFor(path)
	if err != nil {
		t.Skip("host does not expose supported unique mount identity")
	}
	b, err := readKernelFor(path)
	if err != nil || a.MountInstance == 0 || a.MountInstance != b.MountInstance {
		t.Fatalf("same live mount identity changed: %d %d %v", a.MountInstance, b.MountInstance, err)
	}

}
