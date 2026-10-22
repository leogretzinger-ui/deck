package tmux

import (
	"bytes"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tmuxAtLeast reports whether the tmux on PATH is at least major.minor.
// Synchronized updates are only expressible as DEC 2026 from 3.4; older tmux
// emits an iTerm2 DCS form that browsers ignore.
func tmuxAtLeast(t *testing.T, major, minor int) bool {
	t.Helper()
	out, err := exec.Command("tmux", "-V").Output()
	if err != nil {
		t.Skipf("tmux not runnable: %v", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		t.Skipf("unreadable tmux version %q", out)
	}
	digits := regexp.MustCompile(`^(\d+)\.(\d+)`).FindStringSubmatch(fields[1])
	if digits == nil {
		t.Skipf("unparseable tmux version %q", fields[1])
	}
	gotMajor, _ := strconv.Atoi(digits[1])
	gotMinor, _ := strconv.Atoi(digits[2])
	return gotMajor > major || (gotMajor == major && gotMinor >= minor)
}

// A pane redraw reaches the browser wrapped in synchronized updates.
//
// tmux batches one redraw between synchronized-update markers only for a
// client whose terminal claims the capability, and its defaults grant screen*
// nothing but `title`. Without the feature the browser renders every
// intermediate state of a provider's full-screen repaint, which is what a
// user sees as the whole screen redrawing.
func TestAttachedClientReceivesSynchronizedUpdates(t *testing.T) {
	if !tmuxAtLeast(t, 3, 4) {
		t.Skip("tmux predates DEC 2026 synchronized updates; it emits an iTerm2 DCS form instead")
	}
	c := newTestClient(t)

	mustCreate(t, c, spec("sync", "/bin/sh", "-c", "printf 'ready\\n'; sleep 300"))
	att, err := c.Attach("sync", 80, 24)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = att.Close() }()

	// Force a repaint the client must be sent: clearing and rewriting the
	// pane is exactly the shape an inline provider produces on resize.
	if err := att.Resize(100, 30); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	// Read in the background: Attach's pty read blocks, and the marker may
	// arrive in any chunk of the repaint.
	found := make(chan int, 1)
	go func() {
		var seen bytes.Buffer
		buf := make([]byte, 4096)
		for {
			n, readErr := att.Read(buf)
			if n > 0 {
				seen.Write(buf[:n])
				if bytes.Contains(seen.Bytes(), []byte("\x1b[?2026h")) {
					found <- seen.Len()
					return
				}
			}
			if readErr != nil {
				found <- -seen.Len()
				return
			}
		}
	}()

	select {
	case n := <-found:
		if n < 0 {
			t.Fatalf("the attachment ended after %d bytes with no synchronized-update "+
				"marker; a repaint is being sent unbatched", -n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no synchronized-update marker reached the client; a repaint is " +
			"being sent unbatched")
	}
}
