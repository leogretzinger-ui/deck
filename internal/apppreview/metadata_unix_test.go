//go:build unix

package apppreview

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func metadataFixture(t *testing.T) (*Registry, string, *MetadataServer) {
	t.Helper()
	r, d, _ := registryFixture(t, 3)
	registryProject(t, d, "a")
	registryProject(t, d, "b")
	socket := filepath.Join(t.TempDir(), "metadata", "preview.sock")
	server, err := StartMetadata(socket, r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return r, socket, server
}

func TestMetadataLifecycle(t *testing.T) {
	r, socket, server := metadataFixture(t)
	ctx := context.Background()
	if _, err := ReadAssignment(ctx, socket, "a"); err == nil || !strings.Contains(err.Error(), "enable") {
		t.Fatalf("unregistered: %v", err)
	}
	a, err := r.Enable(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Enable(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	for _, reg := range []*Registration{a, b} {
		got, err := ReadAssignment(ctx, socket, reg.ProjectID)
		if err != nil || got.ProjectID != reg.ProjectID || got.Port != reg.Port || got.Origin != reg.Origin || got.Host != "127.0.0.1" {
			t.Fatalf("assignment = %+v, %v", got, err)
		}
	}
	if err := r.Disable(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAssignment(ctx, socket, "a"); err == nil || !strings.Contains(err.Error(), "enable") {
		t.Fatalf("disabled: %v", err)
	}
	if _, err := r.Enable(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadAssignment(ctx, socket, "a"); err != nil || got.Port != a.Port {
		t.Fatalf("reenable = %+v, %v", got, err)
	}
	registrySQL(t, r.db, `UPDATE projects SET removed_at = '2026-09-21' WHERE id = 'a'`)
	if _, err := ReadAssignment(ctx, socket, "a"); err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("removed: %v", err)
	}
	if _, err := ReadAssignment(ctx, socket, "unknown"); err == nil {
		t.Fatal("unknown project accepted")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAssignment(ctx, socket, "b"); err == nil {
		t.Fatal("stopped metadata returned cached assignment")
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("shutdown left socket: %v", err)
	}
	restarted, err := StartMetadata(socket, r)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if got, err := ReadAssignment(ctx, socket, "b"); err != nil || got.Port != b.Port {
		t.Fatalf("metadata restart = %+v, %v", got, err)
	}
}

func TestMetadataFailedDisable(t *testing.T) {
	r, socket, _ := metadataFixture(t)
	ctx := context.Background()
	if _, err := r.Enable(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	registrySQL(t, r.db, `CREATE TRIGGER reject_preview_disable BEFORE UPDATE ON project_preview_registrations BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END`)
	if err := r.Disable(ctx, "a"); err == nil {
		t.Fatal("fixture did not reject disable persistence")
	}
	if _, err := ReadAssignment(ctx, socket, "a"); err == nil {
		t.Fatal("failed durable disable exposed revoked live assignment")
	}
}

func rawMetadata(t *testing.T, socket, raw string) metadataResponse {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = io.WriteString(conn, raw)
	_ = conn.(*net.UnixConn).CloseWrite()
	var response metadataResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestMetadataProtocol(t *testing.T) {
	r, socket, _ := metadataFixture(t)
	if _, err := r.Enable(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"version":1,"project_id":"a","origin":"https://internal"}`,
		`{"version":1,"project_id":"a","action":"enable"}`,
		`{"version":1,"project_id":"a"} {}`,
		`{"version":2,"project_id":"a"}`,
		`{"version":1,"project_id":""}`,
		`{"version":1,"project_id":"` + strings.Repeat("a", 257) + `"}`,
		`{"version":1}`, `null`, `[]`, "GET /api/projects HTTP/1.1\r\n\r\n",
		strings.Repeat(" ", 1025),
	} {
		got := rawMetadata(t, socket, raw)
		if got.Error != "bad_request" || got.Assignment != nil {
			t.Fatalf("invalid request accepted: %+v", got)
		}
	}
	got := rawMetadata(t, socket, `{"version":1,"project_id":"a"}`)
	if got.Error != "" || got.Assignment == nil {
		t.Fatalf("valid request: %+v", got)
	}
	info, err := os.Stat(socket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket mode: %v %v", info, err)
	}
}

func TestMetadataObservedConflicts(t *testing.T) {
	r, socket, original := metadataFixture(t)
	if _, err := StartMetadata(socket, r); err == nil {
		t.Fatal("replaced live socket")
	}
	if _, err := r.Enable(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAssignment(context.Background(), socket, "a"); err != nil {
		t.Fatalf("original stopped: %v", err)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := StartMetadata(socket, r); err == nil {
		t.Fatal("replaced ordinary file")
	}
	data, _ := os.ReadFile(socket)
	if string(data) != "keep" {
		t.Fatal("ordinary file changed")
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, socket); err != nil {
		t.Fatal(err)
	}
	if _, err := StartMetadata(socket, r); err == nil {
		t.Fatal("accepted an observed symlink at the socket path")
	}
	if got, err := os.Readlink(socket); err != nil || got != target {
		t.Fatalf("symlink changed: %q, %v", got, err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: socket})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()
	restarted, err := StartMetadata(socket, r)
	if err != nil {
		t.Fatalf("owned stale socket: %v", err)
	}
	defer restarted.Close()
	if err := os.Rename(socket, socket+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(socket); string(data) != "replacement" {
		t.Fatal("shutdown removed a replacement present before cleanup began")
	}
}

func TestMetadataPrivateDirectory(t *testing.T) {
	r, _, _ := metadataFixture(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := StartMetadata(filepath.Join(dir, "preview.sock"), r); err == nil {
		t.Fatal("public metadata directory accepted")
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Dir(dir), link); err != nil {
		t.Fatal(err)
	}
	if _, err := StartMetadata(filepath.Join(link, "preview.sock"), r); err == nil {
		t.Fatal("symlink metadata directory accepted")
	}
}

func TestMetadataClientValidation(t *testing.T) {
	for _, reply := range []string{
		`{"version":1,"assignment":{"project_id":"sibling","host":"127.0.0.1","port":15000,"origin":"https://app.example.net"}}`,
		`{"version":1,"assignment":{"project_id":"a","host":"0.0.0.0","port":15000,"origin":"https://app.example.net"}}`,
		`{"version":1,"assignment":{"project_id":"a","host":"127.0.0.1","port":0,"origin":"https://app.example.net"}}`,
		`{"version":1,"assignment":{"project_id":"a","host":"127.0.0.1","port":15000,"origin":"http://app.example.net"}}`,
		`{"version":1,"assignment":{"project_id":"a","host":"127.0.0.1","port":15000,"origin":"https://localhost"}}`,
		`{"version":1,"error":"internal","assignment":{"project_id":"a","host":"127.0.0.1","port":15000,"origin":"https://app.example.net"}}`,
		`{"version":2,"error":"preview_disabled"}`, `{"version":1}`, `null`,
		`{"version":1,"error":"preview_disabled","ticket":"unexpected"}`,
		`{"version":1,"error":"preview_disabled"} {}`,
		strings.Repeat(" ", metadataLimit+1),
	} {
		socket := filepath.Join(t.TempDir(), "reply.sock")
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: socket})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			_, _ = io.ReadAll(io.LimitReader(conn, 1025))
			_, _ = io.WriteString(conn, reply)
		}()
		_, err = ReadAssignment(context.Background(), socket, "a")
		_ = listener.Close()
		<-done
		if err == nil {
			t.Fatalf("invalid metadata response accepted: %.100s", reply)
		}
	}
}
