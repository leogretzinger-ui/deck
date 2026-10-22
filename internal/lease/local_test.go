package lease

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type observedContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

func (c *observedContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}

func TestLocalCancellationWhileWaitingForLock(t *testing.T) {
	for _, operation := range []string{"acquire", "heartbeat", "validate", "get"} {
		t.Run(operation, func(t *testing.T) {
			s := NewLocal()
			before, err := s.Acquire(context.Background(), "target", "holder", false)
			if err != nil {
				t.Fatal(err)
			}
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &observedContext{Context: base, checked: make(chan struct{})}
			result := make(chan error, 1)
			s.mu.Lock()
			go func() {
				var err error
				switch operation {
				case "acquire":
					_, err = s.Acquire(ctx, "target", "new-holder", true)
				case "heartbeat":
					_, err = s.Heartbeat(ctx, "target", "holder", before.FencingToken)
				case "validate":
					err = s.Validate(ctx, "target", "holder", before.FencingToken)
				case "get":
					_, err = s.Get(ctx, "target")
				}
				result <- err
			}()
			select {
			case <-ctx.checked:
			case <-time.After(time.Second):
				s.mu.Unlock()
				t.Fatal("operation did not check cancellation before waiting")
			}
			cancel()
			s.mu.Unlock()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("operation returned %v, want cancellation", err)
				}
			case <-time.After(time.Second):
				t.Fatal("operation did not return after unlocking")
			}
			after, err := s.Get(context.Background(), "target")
			if err != nil || *after != *before {
				t.Fatalf("cancelled operation changed lease: before=%+v after=%+v error=%v", before, after, err)
			}
		})
	}
}

func TestManagerRejectsEmptyIDsBeforeDatabaseAccess(t *testing.T) {
	m := NewManager(nil)
	for _, ids := range [][2]string{{"", "holder"}, {"target", ""}} {
		if _, err := m.Acquire(context.Background(), ids[0], ids[1], false); err == nil || err.Error() != "lease: session and controller ids are required" {
			t.Fatalf("empty IDs returned %v", err)
		}
	}
}
