package apppreview

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLaunchContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preview.json")
	for _, raw := range []string{`{"version":1,"command":["node","a b.mjs","$(not-a-shell)"]}`, `{"version":1,"command":["./dev"]}`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		config, err := ReadLaunchConfig(path)
		if err != nil || len(config.Command) == 0 {
			t.Fatalf("valid command: %+v, %v", config, err)
		}
	}
	for _, raw := range []string{`{"version":1,"command":[]}`, `{"version":1,"command":[""]}`, `{"version":2,"command":["dev"]}`, `{"version":1,"command":["dev"],"port":3333}`, `{"version":1,"command":"dev"}`, `{"version":1,"command":["dev"]} {}`, `{"version":1,"command":["dev\u0000"]}`, "null", strings.Repeat(" ", 16385)} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadLaunchConfig(path); err == nil {
			t.Fatalf("invalid command accepted: %.100s", raw)
		}
	}
	if _, err := ReadLaunchConfig(filepath.Dir(path)); err == nil {
		t.Fatal("directory configuration accepted")
	}
}

func TestLaunchEnvironmentAssignment(t *testing.T) {
	a := Assignment{ProjectID: "a", Host: "127.0.0.1", Port: 15001, Origin: "https://app.example.net:8443"}
	env := LaunchEnvironment([]string{"PATH=/usr/bin", "DECK_PREVIEW_PORT=9", "DECK_PREVIEW_HOST=0.0.0.0", "DECK_PREVIEW_OBSOLETE=value", "ORDINARY=value"}, a)
	joined := strings.Join(env, "\n")
	for _, want := range []string{"PATH=/usr/bin", "ORDINARY=value", "DECK_PREVIEW_HOST=127.0.0.1", "DECK_PREVIEW_PORT=15001", "DECK_PREVIEW_ORIGIN=https://app.example.net:8443", "DECK_PREVIEW_HOSTNAME=app.example.net", "DECK_PREVIEW_HTTPS_PORT=8443"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, bad := range []string{"PORT=9", "HOST=0.0.0.0", "OBSOLETE"} {
		if strings.Contains(joined, bad) {
			t.Errorf("retained stale %s", bad)
		}
	}
	a.Origin = "https://app.example.net"
	if !strings.Contains(strings.Join(LaunchEnvironment(nil, a), "\n"), "DECK_PREVIEW_HTTPS_PORT=443") {
		t.Fatal("default HTTPS port missing")
	}
}

func TestLaunchPortCollision(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := Assignment{ProjectID: "a", Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, Origin: "https://app.example.net"}
	if err := CheckLaunchPort(a); err == nil {
		t.Fatal("occupied port accepted")
	}
	_ = l.Close()
	if err := CheckLaunchPort(a); err != nil {
		t.Fatalf("released port: %v", err)
	}
	l, err = net.Listen("tcp", net.JoinHostPort(a.Host, strconv.Itoa(a.Port)))
	if err != nil {
		t.Fatalf("preflight held port: %v", err)
	}
	_ = l.Close()
	// The assignment validation also applies to responses from an unrelated
	// socket accidentally selected by a user.
	a.Host = "0.0.0.0"
	if err := CheckLaunchPort(a); err == nil {
		t.Fatal("public bind accepted")
	}
}

func TestLaunchOriginEnvironment(t *testing.T) {
	for _, tc := range []struct{ origin, scheme, port string }{
		{"https://app.example.net", "https", "443"},
		{"https://app.example.net:8443", "https", "8443"},
		{"http://127.0.0.2:8840", "http", "8840"},
		{"http://127.0.0.2", "http", "80"},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			values := map[string]string{}
			for _, entry := range LaunchEnvironment([]string{"DECK_PREVIEW_SCHEME=stale", "DECK_PREVIEW_ORIGIN_PORT=9"}, Assignment{Origin: tc.origin}) {
				key, value, _ := strings.Cut(entry, "=")
				if _, exists := values[key]; exists {
					t.Fatalf("duplicate %s", key)
				}
				values[key] = value
			}
			if values["DECK_PREVIEW_SCHEME"] != tc.scheme || values["DECK_PREVIEW_ORIGIN_PORT"] != tc.port || values["DECK_PREVIEW_HTTPS_PORT"] != tc.port {
				t.Fatalf("environment: %+v", values)
			}
		})
	}
}

func TestLoopbackAssignmentValidation(t *testing.T) {
	a := Assignment{ProjectID: "a", Host: "127.0.0.1", Port: 15000, Origin: "http://127.0.0.2:8840"}
	for _, origin := range []string{"http://127.0.0.2:8840", "http://127.0.0.2:80"} {
		a.Origin = origin
		if err := a.validate("a"); err != nil {
			t.Fatal(err)
		}
	}
	for _, origin := range []string{"http://127.0.0.1:8840", "http://localhost:8840", "http://[::1]:8840", "http://app.example.net:8840", "https://127.0.0.2:8840"} {
		a.Origin = origin
		if err := a.validate("a"); err == nil {
			t.Fatalf("unsafe assignment accepted: %s", origin)
		}
	}
}
