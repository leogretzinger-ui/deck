//go:build unix

package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/apppreview"
	"github.com/noisefloor-oss/deck/internal/db"
)

func TestPreviewHelperProcess(t *testing.T) {
	if os.Getenv("DECK_TEST_PREVIEW_HELPER") != "1" {
		return
	}
	os.Exit(run([]string{"preview", "run"}, os.Stdout, os.Stderr))
}

func previewCommandFixture(t *testing.T, command []string) (*exec.Cmd, *apppreview.Gateway, int) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(context.Background(), db.Config{Path: filepath.Join(dir, "control.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects (id,name,canonical_path,created_at,last_used_at) VALUES ('preview-test','preview-test','/synthetic','2026-09-21','2026-09-21')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	targets := []apppreview.Target{{ProjectID: "preview-test", Origin: "https://app.example.net", Upstream: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}}
	gateway, err := apppreview.New(apppreview.Config{DeckOrigins: []string{"https://deck.example.com"}, Targets: targets})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })
	registry, err := apppreview.NewRegistry(d, gateway, apppreview.RegistryConfig{StaticTargets: targets})
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "metadata", "preview.sock")
	metadata, err := apppreview.StartMetadata(socket, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	if err := os.Mkdir(filepath.Join(dir, ".deck"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(apppreview.LaunchConfig{Version: 1, Command: command})
	if err := os.WriteFile(filepath.Join(dir, ".deck", "preview.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPreviewHelperProcess$")
	cmd.Dir = dir
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "NOISEDECK_PROJECT_ID" && key != "NOISEDECK_PROJECT_PREVIEWS_METADATA_SOCKET" && key != "DECK_TEST_PREVIEW_HELPER" && !strings.HasPrefix(key, "DECK_PREVIEW_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "DECK_TEST_PREVIEW_HELPER=1", "NOISEDECK_PROJECT_ID=preview-test", "NOISEDECK_PROJECT_PREVIEWS_METADATA_SOCKET="+socket, "DECK_PREVIEW_PORT=1")
	return cmd, gateway, port
}

func TestPreviewExecExit(t *testing.T) {
	cmd, _, port := previewCommandFixture(t, []string{"/bin/sh", "-c", `printf '%s\n' "$DECK_PREVIEW_HOST|$DECK_PREVIEW_PORT|$DECK_PREVIEW_ORIGIN|$DECK_PREVIEW_HOSTNAME|$DECK_PREVIEW_HTTPS_PORT"; exit 37`})
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 37 {
		t.Fatalf("dev command exit: %v, %s", err, output)
	}
	want := fmt.Sprintf("127.0.0.1|%d|https://app.example.net|app.example.net|443\n", port)
	if string(output) != want {
		t.Fatalf("assignment env: %q, want %q", output, want)
	}
}

func TestPreviewExecSignal(t *testing.T) {
	cmd, _, _ := previewCommandFixture(t, []string{"/bin/sh", "-c", `printf '%s\n' "$$"; exec sleep 60`})
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("command did not start: %s", stderr.String())
	}
	if scanner.Text() != strconv.Itoa(cmd.Process.Pid) {
		t.Fatalf("launcher inserted a process supervisor: child=%s helper=%d", scanner.Text(), cmd.Process.Pid)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("signal exit: %v", err)
	}
	status := exit.Sys().(syscall.WaitStatus)
	if !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("signal was not preserved: %v", status)
	}
}

func TestPreviewRefusesLaunch(t *testing.T) {
	for _, kind := range []string{"disabled", "collision", "offline"} {
		t.Run(kind, func(t *testing.T) {
			cmd, gateway, port := previewCommandFixture(t, []string{"/bin/sh", "-c", "echo must-not-execute"})
			switch kind {
			case "disabled":
				gateway.Unregister("preview-test")
			case "collision":
				l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
			case "offline":
				for i, e := range cmd.Env {
					if strings.HasPrefix(e, "NOISEDECK_PROJECT_PREVIEWS_METADATA_SOCKET=") {
						cmd.Env[i] = "NOISEDECK_PROJECT_PREVIEWS_METADATA_SOCKET=" + filepath.Join(cmd.Dir, "absent.sock")
					}
				}
			}
			out, err := cmd.CombinedOutput()
			if err == nil || strings.Contains(string(out), "must-not-execute") {
				t.Fatalf("refused state executed command: %v, %s", err, out)
			}
		})
	}
}
