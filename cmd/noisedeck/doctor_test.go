package main

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noisefloor-oss/deck/internal/doctor"
	"github.com/noisefloor-oss/deck/internal/taskengine"
)

func TestDoctorCommandUsesServeConfigAndExitStatus(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("NOISEDECK_PROJECT_PREVIEWS_METADATA_SOCKET", "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bind := ln.Addr().String()
	ln.Close()
	t.Setenv("NOISEDECK_SERVER_BIND", bind)
	for name, body := range map[string]string{
		"tmux":  "[ \"$*\" = '-V' ] || exit 2; printf 'tmux 3.6a\\n'",
		"git":   "exit 2",
		"gh":    "exit 2",
		"a2a":   "exit 2",
		"noise": "[ \"$*\" = 'tasks version' ] || exit 2; printf 'noise-tasks " + taskengine.Version + "\\n'",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(dir, "deck.yaml")
	body := "projects:\n  allowed_roots: [" + dir + "]\ndatabase:\n  path: " + filepath.Join(dir, "data", "deck.db") + "\ntaskmaster:\n  executable: " + filepath.Join(dir, "noise") + "\nchats:\n  executable: " + filepath.Join(dir, "a2a") + "\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []bool{false, true} {
		if missing {
			if err := os.Remove(filepath.Join(dir, "gh")); err != nil {
				t.Fatal(err)
			}
		}
		for _, asJSON := range []bool{false, true} {
			args := []string{"doctor", "--config", cfg}
			if asJSON {
				args = append(args, "--json")
			}
			var stdout, stderr bytes.Buffer
			code := run(args, &stdout, &stderr)
			want := 0
			if missing {
				want = 1
			}
			if code != want || stderr.Len() != 0 {
				t.Fatalf("doctor = %d, want %d, stderr=%s stdout=%s", code, want, &stderr, &stdout)
			}
			if asJSON {
				var report doctor.Report
				if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || report.OK == missing || len(report.Checks) == 0 {
					t.Fatalf("JSON=%s, err=%v", &stdout, err)
				}
			} else if !strings.Contains(stdout.String(), "gh:") {
				t.Fatalf("text report=%s", &stdout)
			}
		}
	}
}

func TestDoctorConfigErrorsAreJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"doctor", "--json", "--config", filepath.Join(t.TempDir(), "missing")}, &stdout, &stderr)
	var report doctor.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || code != 1 || report.OK || len(report.Checks) != 1 || report.Checks[0].Name != "config" {
		t.Fatalf("doctor=%d, JSON=%s, err=%v", code, &stdout, err)
	}
}

func TestDoctorRejectsUnexpectedArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"doctor", "extra"}, &stdout, &stderr); code != 2 {
		t.Fatalf("doctor=%d, stderr=%s", code, &stderr)
	}
}
