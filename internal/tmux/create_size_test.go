package tmux

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A session is created detached, and tmux's default sizes it 80x24 — a
// width no user is viewing, which lays out every headless line for
// nobody. A spec size must reach the window at creation. Two further facts,
// measured on tmux 3.6a
// with the default window-size=latest and recorded here because they shape
// the client model: an attach resizes the window to the attaching client,
// and input from ANY still-attached client resizes it back — the controller
// acquisition path must therefore close a displaced attachment promptly.
func TestCreateSessionTakesTheCreatingClientSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(Config{SocketPath: dir + "/t.sock"})
	if err != nil {
		t.Fatal(err)
	}
	// exit-empty off keeps this server alive past its sessions; without the
	// kill it outlives the test run.
	t.Cleanup(func() { _ = c.KillServer(context.Background()) })
	if _, err := c.CreateSession(context.Background(), SessionSpec{
		Name: "sized", WorkDir: dir, Argv: []string{"/bin/sh", "-c", "sleep 60"},
		Generation: 1, Marker: "sized-marker", Cols: 45, Rows: 90,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("tmux", "-S", dir+"/t.sock", "list-windows", "-a",
		"-F", "#{window_width}x#{window_height}").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "45x90" {
		t.Fatalf("created window is %s, want 45x90", got)
	}
	if _, err := c.CreateSession(context.Background(), SessionSpec{
		Name: "unsized", WorkDir: dir, Argv: []string{"/bin/sh", "-c", "sleep 60"},
		Generation: 1, Marker: "unsized-marker",
	}); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command("tmux", "-S", dir+"/t.sock", "list-windows", "-a",
		"-F", "#{session_name} #{window_width}x#{window_height}").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "unsized 80x24") {
		t.Fatalf("zero-size spec changed the tmux default: %q", strings.TrimSpace(string(out)))
	}
}
