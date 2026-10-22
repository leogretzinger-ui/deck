package deploy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestProviderHookUsesPrivateSocketWithoutTCPFallback(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	dir, err := os.MkdirTemp("/tmp", "deck-hook-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "backend.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	const payload = `{"hook_event_name":"SessionStart","session_id":"test","cwd":"/project","source":"startup"}`
	var privateCalls, tcpCalls atomic.Int32
	private := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		privateCalls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != payload || r.Header.Get("X-Noisedeck-Hook-Token") != "test-token" {
			t.Error("private transport changed the hook payload or token")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	_ = private.Listener.Close()
	private.Listener = listener
	private.Start()
	defer private.Close()
	tcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tcpCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer tcp.Close()
	run := func() {
		t.Helper()
		cmd := exec.Command("/bin/sh", filepath.Join("provider-hook", "noisedeck-provider-hook"))
		cmd.Env = append(os.Environ(), "NOISEDECK_SESSION_HOOK_URL="+tcp.URL,
			"NOISEDECK_SESSION_HOOK_TOKEN=test-token", "NOISEDECK_SESSION_HOOK_SOCKET="+path)
		cmd.Stdin = strings.NewReader(payload)
		if output, err := cmd.CombinedOutput(); err != nil || len(output) != 0 {
			t.Fatalf("hook delivery must fail silently: error=%v output bytes=%d", err, len(output))
		}
	}
	run()
	if privateCalls.Load() != 1 || tcpCalls.Load() != 0 {
		t.Fatalf("hook transport counts: private=%d TCP=%d", privateCalls.Load(), tcpCalls.Load())
	}
	private.Close()
	run()
	if privateCalls.Load() != 1 || tcpCalls.Load() != 0 {
		t.Fatal("an unavailable private socket fell back to TCP")
	}
}

func TestProviderHookRetriesOnlyTransientDeliveryFailures(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}

	tests := []struct {
		name         string
		statuses     []int
		dropFirst    int
		wantAttempts int
	}{
		{name: "server error then success", statuses: []int{http.StatusServiceUnavailable, http.StatusNoContent}, wantAttempts: 2},
		{name: "client error stops", statuses: []int{http.StatusBadRequest, http.StatusNoContent}, wantAttempts: 1},
		{name: "network errors retry", statuses: []int{http.StatusNoContent}, dropFirst: 2, wantAttempts: 3},
		{name: "server errors exhaust bound", statuses: []int{http.StatusServiceUnavailable}, wantAttempts: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const payload = `{"hook_event_name":"SessionStart","session_id":"test","cwd":"/project","source":"startup"}`
			var mu sync.Mutex
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				attempts++
				attempt := attempts
				mu.Unlock()

				if r.Header.Get("X-Noisedeck-Hook-Token") != "test-token" {
					t.Error("capture token header missing")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
				} else if string(body) != payload {
					t.Error("delivery changed the serialized capture body")
				}
				if attempt <= tc.dropFirst {
					hijacker, ok := w.(http.Hijacker)
					if !ok {
						t.Error("test server cannot drop connection")
						return
					}
					conn, _, err := hijacker.Hijack()
					if err != nil {
						t.Errorf("drop connection: %v", err)
						return
					}
					_ = conn.Close()
					return
				}
				statusIndex := attempt - tc.dropFirst - 1
				if statusIndex >= len(tc.statuses) {
					statusIndex = len(tc.statuses) - 1
				}
				w.WriteHeader(tc.statuses[statusIndex])
			}))
			defer server.Close()

			cmd := exec.Command("/bin/sh", filepath.Join("provider-hook", "noisedeck-provider-hook"))
			cmd.Env = append(os.Environ(),
				"NOISEDECK_SESSION_HOOK_URL="+server.URL,
				"NOISEDECK_SESSION_HOOK_TOKEN=test-token",
			)
			cmd.Stdin = strings.NewReader(payload)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("hook receiver exited non-zero: %v", err)
			}
			if len(output) != 0 {
				t.Fatalf("hook receiver emitted %d bytes; delivery must be silent", len(output))
			}
			mu.Lock()
			gotAttempts := attempts
			mu.Unlock()
			if gotAttempts != tc.wantAttempts {
				t.Fatalf("delivery attempts = %d, want %d", gotAttempts, tc.wantAttempts)
			}
		})
	}
}

func TestProviderHookKeepsPayloadInMemory(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("provider-hook", "noisedeck-provider-hook"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, want := range []string{
		"payload=$(cat",
		`printf '%s' "$payload" |`,
		"deadline=$((started_at + 8))",
		`while [ "$attempt" -le 3 ]`,
		`sleep 1`,
		"2??) diagnostic receiver_accepted; exit 0",
		"4??) diagnostic receiver_client_refused; exit 0",
		"5??) ;;",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("hook receiver is missing bounded in-memory delivery mechanism %q", want)
		}
	}
	for _, forbidden := range []string{"mktemp", "set -x", "--location", `--data-binary "$payload"`} {
		if strings.Contains(script, forbidden) {
			t.Errorf("hook receiver contains forbidden delivery mechanism %q", forbidden)
		}
	}
}
