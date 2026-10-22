package inputgate

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkspaceFenceDrainsAdmittedMutationAndStaysClosed(t *testing.T) {
	gate := New()
	release, err := gate.Admit()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := gate.FenceAndDrain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain before admitted writer returns: %v", err)
	}
	if _, err := gate.Admit(); !errors.Is(err, ErrWorkspaceLocking) {
		t.Fatalf("new writer crossed fence: %v", err)
	}
	release()
	release() // Cleanup paths may release once more; never underflow the drain.
	if err := gate.FenceAndDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Admit(); !errors.Is(err, ErrWorkspaceLocking) {
		t.Fatalf("drained fence reopened: %v", err)
	}
}
