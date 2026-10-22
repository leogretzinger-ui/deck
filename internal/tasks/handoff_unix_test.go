//go:build linux || darwin

package tasks

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func testHandoffListener(t *testing.T) *handoffListener {
	t.Helper()
	dir, err := os.MkdirTemp("", "nd-handoff-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	h, err := newHandoffListener(dir, "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h
}

func TestHandoffRefusesUnsafeSocketDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if h, err := newHandoffListener(dir, "0123456789abcdef"); err == nil {
		h.Close()
		t.Fatal("accepted a shared socket directory")
	}
}

func TestHandoffRefusesReplacedSocketDirectory(t *testing.T) {
	h := testHandoffListener(t)
	dir := h.dir.Name()
	saved := dir + ".anchored"
	if err := os.Rename(dir, saved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(saved) })
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, filepath.Base(h.path))
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.await(context.Background(), nil, nil); err == nil {
		t.Fatal("accepted a replaced socket directory")
	}
	h.Close()
	assertFileBody(t, replacement, "replacement")
}

func TestHandoffAcknowledgementDeadline(t *testing.T) {
	for _, connectedAck := range []bool{false, true} {
		h := testHandoffListener(t)
		conn, err := net.Dial("unix", h.path)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if connectedAck {
			ack, err := net.Dial("unix", h.path)
			if err != nil {
				t.Fatal(err)
			}
			defer ack.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		started := time.Now()
		if ok, err := h.await(ctx, h.dir, h.dir); err == nil || ok {
			t.Fatalf("accepted a missing acknowledgement: %v, %v", ok, err)
		}
		if time.Since(started) > time.Second {
			t.Fatal("acknowledgement exceeded context deadline")
		}
	}
}

func TestHandoffRefusesInvalidDescriptorTransfer(t *testing.T) {
	for _, count := range []int{0, 1, 3, 8} {
		h := testHandoffListener(t)
		client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: h.path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		server, err := h.listener.AcceptUnix()
		if err != nil {
			t.Fatal(err)
		}
		fds := make([]int, count)
		for i := range fds {
			fds[i] = int(h.dir.Fd())
		}
		if _, _, err := client.WriteMsgUnix([]byte{1}, unix.UnixRights(fds...), nil); err != nil {
			t.Fatal(err)
		}
		root, prd, err := receiveHandoffDescriptors(server)
		if root != nil {
			root.Close()
		}
		if prd != nil {
			prd.Close()
		}
		client.Close()
		server.Close()
		if err == nil {
			t.Fatalf("accepted %d descriptors", count)
		}
	}
}

func TestHandoffRefusesInvalidDescriptorTypes(t *testing.T) {
	for _, badRoot := range []bool{false, true} {
		h := testHandoffListener(t)
		regular, err := os.CreateTemp(t.TempDir(), "prd")
		if err != nil {
			t.Fatal(err)
		}
		defer regular.Close()
		root, prd := h.dir, h.dir
		want := "PRD is not a regular file"
		if badRoot {
			root, prd = regular, regular
			want = "project root is not a directory"
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGenerationHandoffProcess$", "--", "/bin/true", h.path)
		var output strings.Builder
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if ok, err := h.await(ctx, root, prd); err != nil || ok {
			t.Fatalf("accepted invalid descriptors: %v, %v", ok, err)
		}
		if err := cmd.Wait(); err == nil || !strings.Contains(output.String(), want) {
			t.Fatalf("handoff exit: %v, output: %s", err, output.String())
		}
	}
}
