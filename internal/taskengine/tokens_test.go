package taskengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fixtureClaimToken = "314159265358979"
const fixtureAttemptToken = "271828182845904"

func TestEngineTokenPreparationFailureRemovesPartialFiles(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	_, cleanup, err := engineTokenFiles([]string{"-claim-token", fixtureClaimToken, "-attempt-token"})
	if err == nil || cleanup != nil {
		t.Fatal("missing token value was accepted")
	}
	entries, err := os.ReadDir(temp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed preparation retained token files: %v", err)
	}
	t.Setenv("TMPDIR", filepath.Join(temp, "absent"))
	if _, _, err := engineTokenFiles([]string{"-claim-token", fixtureClaimToken}); err == nil {
		t.Fatal("missing temporary parent was accepted")
	}
}

func TestEngineTokenDiagnosticsRedactBeforeTruncating(t *testing.T) {
	args := []string{"run", "renew", "-claim-token", fixtureClaimToken}
	err := (Engine{}).wrap(args, errors.New("claim is not live"), []byte(strings.Repeat("x", 1997)+fixtureClaimToken))
	if strings.Contains(err.Error(), fixtureClaimToken[:3]) {
		t.Fatal("truncation exposed a partial capability")
	}
	redact := engineTokenRedactor([]string{"-claim-token", "12", "-attempt-token", "123"})
	if got := redact("claim 12 attempt 123"); got != "claim [redacted] attempt [redacted]" {
		t.Fatalf("overlapping capability values were partially exposed: %s", got)
	}
}

func TestEngineTokenChild(t *testing.T) {
	if os.Getenv("CIHEAL_TOKEN_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	var paths []string
	for i, arg := range os.Args {
		if strings.Contains(arg, fixtureClaimToken) || strings.Contains(arg, fixtureAttemptToken) {
			t.Fatal("capability appeared in child argv")
		}
		want := ""
		switch arg {
		case "-claim-token-file":
			want = fixtureClaimToken
		case "-attempt-token-file":
			want = fixtureAttemptToken
		}
		if want == "" {
			continue
		}
		path := os.Args[i+1]
		body, err := os.ReadFile(path)
		if err != nil || string(body) != want {
			t.Fatal("child could not read its token file")
		}
		for name, mode := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
			info, err := os.Stat(name)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatal("capability file or parent permissions are not private")
			}
		}
		paths = append(paths, path)
	}
	if len(paths) != 2 {
		t.Fatal("child did not receive both token files")
	}
	raw, err := json.Marshal(paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("CIHEAL_TOKEN_CAPTURE"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CIHEAL_TOKEN_BLOCK") == "1" {
		if err := os.WriteFile(os.Getenv("CIHEAL_TOKEN_CAPTURE")+".pid", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Hour)
	}
	switch os.Getenv("CIHEAL_TOKEN_STDERR_BOUNDARY") {
	case "prefix":
		fmt.Fprint(os.Stderr, strings.Repeat(" ", 32754)+fixtureClaimToken+strings.Repeat("x", 70000))
		os.Exit(7)
	case "suffix":
		fmt.Fprint(os.Stderr, strings.Repeat(" ", 70000)+fixtureClaimToken+strings.Repeat(" ", 32754))
		os.Exit(7)
	}
	if os.Getenv("CIHEAL_TOKEN_FAIL") == "1" {
		fmt.Fprintln(os.Stderr, "claim is not live: "+fixtureClaimToken+" "+fixtureAttemptToken)
		fmt.Fprintln(os.Stdout, fixtureClaimToken)
		os.Exit(7)
	}
	fmt.Print(string(raw))
	os.Exit(0)
}

func TestEngineCancellationRemovesTokenFiles(t *testing.T) {
	t.Setenv("CIHEAL_TOKEN_CHILD", "1")
	t.Setenv("CIHEAL_TOKEN_BLOCK", "1")
	capture := filepath.Join(t.TempDir(), "paths.json")
	t.Setenv("CIHEAL_TOKEN_CAPTURE", capture)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := Engine{Binary: os.Args[0], Family: t.TempDir()}
	done := make(chan error, 1)
	go func() {
		_, err := e.run(ctx, "-test.run=^TestEngineTokenChild$", "--",
			"run", "renew", "-claim-token", fixtureClaimToken, "-attempt-token", fixtureAttemptToken)
		done <- err
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(capture + ".pid"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("engine child did not reach its blocking operation")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled engine reported success")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("cancelled engine did not exit promptly")
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	if err := json.Unmarshal(raw, &paths); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Fatal("cancelled invocation retained token files")
		}
	}
	pidText, err := os.ReadFile(capture + ".pid")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidText))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("cancelled engine child still exists: %v", err)
	}
}

func TestEngineCapabilitiesStayOutOfArgvAndErrors(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%v", fail), func(t *testing.T) {
			t.Setenv("CIHEAL_TOKEN_CHILD", "1")
			capture := filepath.Join(t.TempDir(), "paths.json")
			t.Setenv("CIHEAL_TOKEN_CAPTURE", capture)
			if fail {
				t.Setenv("CIHEAL_TOKEN_FAIL", "1")
			}
			e := Engine{Binary: os.Args[0], Family: t.TempDir()}
			out, err := e.run(context.Background(), "-test.run=^TestEngineTokenChild$", "--",
				"run", "renew", "-claim-token", fixtureClaimToken, "-attempt-token", fixtureAttemptToken)
			if fail {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 || !strings.Contains(err.Error(), "claim is not live") {
					t.Fatalf("engine failure lost its cause: %v", err)
				}
				if len(out) != 0 || strings.Contains(err.Error(), fixtureClaimToken) || strings.Contains(err.Error(), fixtureAttemptToken) {
					t.Fatal("engine failure exposed a capability")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			var paths []string
			if err := json.Unmarshal(raw, &paths); err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
					t.Fatal("token directory survived the engine invocation")
				}
			}
		})
	}
}

func TestEngineLargeStderrRedactsBeforeExecTruncation(t *testing.T) {
	for _, boundary := range []string{"prefix", "suffix"} {
		t.Run(boundary, func(t *testing.T) {
			t.Setenv("CIHEAL_TOKEN_CHILD", "1")
			t.Setenv("CIHEAL_TOKEN_STDERR_BOUNDARY", boundary)
			t.Setenv("CIHEAL_TOKEN_CAPTURE", filepath.Join(t.TempDir(), "paths.json"))
			e := Engine{Binary: os.Args[0], Family: t.TempDir()}
			out, err := e.run(context.Background(), "-test.run=^TestEngineTokenChild$", "--",
				"run", "renew", "-claim-token", fixtureClaimToken, "-attempt-token", fixtureAttemptToken)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 7 || len(out) != 0 {
				t.Fatal("large-stderr failure lost its cause or exposed raw output")
			}
			for _, partial := range []string{fixtureClaimToken[:14], fixtureClaimToken[1:]} {
				if strings.Contains(err.Error(), partial) {
					t.Fatal("os/exec truncation exposed a partial capability")
				}
			}
			if !strings.Contains(err.Error(), "[redacted]") || len(err.Error()) > 2200 {
				t.Fatal("large-stderr failure lost redaction or diagnostic bound")
			}
		})
	}
}
