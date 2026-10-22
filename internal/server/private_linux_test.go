//go:build linux

package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func privateSocketPath(t *testing.T) string {
	t.Helper()
	// The kernel's Unix address limit also applies when the harness has a
	// deeply nested TMPDIR. Keep this disposable listener's path bounded.
	dir, err := os.MkdirTemp("/tmp", "deck-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "backend.sock")
}

func socketClient(path string) *http.Client {
	return &http.Client{Timeout: time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
}

func TestPrivateServerServesOnlyItsUnixListener(t *testing.T) {
	path := privateSocketPath(t)
	s := New(Config{UnixSocket: path, GatewayUID: os.Geteuid()}, zerolog.Nop(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for s.BoundAddr() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.BoundAddr() != path {
		t.Fatalf("server did not bind its private socket: %q", s.BoundAddr())
	}
	client := socketClient(path)
	defer client.CloseIdleConnections()
	response, err := client.Get("http://localhost/livez")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("livez returned %d", response.StatusCode)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v, %v", info, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed server left its socket behind: %v", err)
	}
}

func TestPrivateListenerRejectsKernelPeerDespiteForgedHeaders(t *testing.T) {
	path := privateSocketPath(t)
	raw, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	// The real client has this process's UID. A different configured UID
	// must reject it before HTTP can interpret any caller-supplied identity.
	listener := &privateListener{UnixListener: raw, uid: os.Geteuid() + 1}
	var reached atomic.Bool
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusNoContent)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	client := socketClient(path)
	defer client.CloseIdleConnections()
	request, err := http.NewRequest(http.MethodGet, "http://localhost/api/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Forwarded-User", "owner@example.test")
	request.Header.Set("X-Deck-Gateway-UID", "1")
	request.Header.Set("Cookie", "deck_session=forged")
	response, err := client.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil || reached.Load() {
		t.Fatal("a rejected kernel peer reached HTTP")
	}
}

func TestPrivateListenerPreservesExistingLiveSocket(t *testing.T) {
	path := privateSocketPath(t)
	first, err := listenPrivate(path, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := listenPrivate(path, os.Geteuid())
	if second != nil {
		_ = second.Close()
	}
	if err == nil {
		t.Fatal("a live socket was replaced")
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("live socket changed: %v", err)
	}
}

func TestPrivateListenerRecoversOnlyAStaleSocket(t *testing.T) {
	path := privateSocketPath(t)
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := listenPrivate(path, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	connection, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
}

func TestPrivateListenerRefusesUnsafePaths(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "directory", "public parent", "symlink parent", "missing parent", "relative", "unclean", "long", "wrong UID"} {
		t.Run(kind, func(t *testing.T) {
			path := privateSocketPath(t)
			uid := os.Geteuid()
			var prepareErr error
			switch kind {
			case "file":
				prepareErr = os.WriteFile(path, []byte("keep this file"), 0600)
			case "symlink":
				prepareErr = os.Symlink("missing-target", path)
			case "directory":
				prepareErr = os.Mkdir(path, 0700)
			case "public parent":
				prepareErr = os.Chmod(filepath.Dir(path), 0755)
			case "symlink parent":
				alias := path + "-alias"
				prepareErr = os.Symlink(filepath.Dir(path), alias)
				path = filepath.Join(alias, "backend.sock")
			case "missing parent":
				path = filepath.Join(filepath.Dir(path), "missing", "backend.sock")
			case "relative":
				path = "backend.sock"
			case "unclean":
				path = filepath.Dir(path) + "/./backend.sock"
			case "long":
				path = filepath.Join(filepath.Dir(path), strings.Repeat("s", 104))
			case "wrong UID":
				uid++
			}
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			before, beforeErr := os.Lstat(path)
			listener, err := listenPrivate(path, uid)
			if listener != nil {
				_ = listener.Close()
			}
			if err == nil {
				t.Fatal("unsafe listener path accepted")
			}
			if beforeErr == nil {
				after, err := os.Lstat(path)
				if err != nil || !os.SameFile(before, after) {
					t.Fatalf("existing path was changed: %v", err)
				}
			}
			if kind == "file" {
				file, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(file)
				_ = file.Close()
				if err != nil || string(body) != "keep this file" {
					t.Fatal("existing file content was changed")
				}
			}
		})
	}
}

func TestPrivateServerRefusesTCPAlongsideSocket(t *testing.T) {
	s := New(Config{Bind: "127.0.0.1:0", UnixSocket: privateSocketPath(t), GatewayUID: os.Geteuid()}, zerolog.Nop(), nil)
	if err := s.Run(context.Background()); err == nil || s.BoundAddr() != "" {
		t.Fatalf("ambiguous listener opened: %v", err)
	}
}
