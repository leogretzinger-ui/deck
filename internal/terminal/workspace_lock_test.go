package terminal

import (
	"context"
	"encoding/base64"
	"sync/atomic"
	"testing"

	"github.com/noisefloor-oss/deck/internal/tmux"
)

func TestWorkspaceLockRefusesAttachedTerminalInput(t *testing.T) {
	e := newEnv(t)
	c, _ := e.dial(t, "browser-a", false)
	defer func() { _ = c.Close() }()
	token := awaitAttached(t, c)
	var writes atomic.Int32
	e.proxy.SetWriteFaultForTest(func(_ *tmux.Attachment, _ []byte) (int, error) { writes.Add(1); return 0, nil })
	if err := e.proxy.gate.FenceAndDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	send(t, c, e.inputFrame(token, base64.StdEncoding.EncodeToString([]byte("echo should-not-run\n"))))
	awaitStatus(t, c, WriteFailed)
	awaitSocketClosed(t, c)
	if writes.Load() != 0 {
		t.Fatal("terminal writer crossed workspace fence")
	}
}

func TestWorkspaceLockRefusesNewTerminalAttachment(t *testing.T) {
	e := newEnv(t)
	var attaches atomic.Int32
	e.tmux.SetAttachObserverForTest(func(tmux.AttachObservation) { attaches.Add(1) })
	if err := e.proxy.gate.FenceAndDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, _ := e.dial(t, "browser-a", false)
	defer func() { _ = c.Close() }()
	frame := readFrame(t, c)
	if frame.Type != TypeError || frame.Code != CodeAttachFailed {
		t.Fatalf("post-fence attach response: %+v", frame)
	}
	awaitSocketClosed(t, c)
	if attaches.Load() != 0 {
		t.Fatal("post-fence terminal spawned a tmux client")
	}
}
