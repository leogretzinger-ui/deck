package taskengine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureTaskUsesReplaySafeEnginePlan(t *testing.T) {
	var content string
	calls := 0
	e := Engine{Tag: "master", Run: func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		raw, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			t.Fatal(err)
		}
		if content != "" && string(raw) != content {
			t.Fatal("replay changed the plan")
		}
		content = string(raw)
		if strings.Join(args[:2], " ") == "plan validate" {
			return []byte(`{"planDigest":"sha256:fixture"}`), nil
		}
		if !strings.Contains(strings.Join(args, " "), "--request-id incident-1") {
			t.Fatal("missing replay key")
		}
		return []byte(`{"receipt":{"taskMappings":[{"key":"repair","id":"9"}]}}`), nil
	}}
	for n := 0; n < 2; n++ {
		if id, err := e.EnsureTask(context.Background(), "incident-1", "Repair", "Failure"); err != nil || id != 9 {
			t.Fatalf("id=%d err=%v", id, err)
		}
	}
	var plan map[string]any
	if err := json.Unmarshal([]byte(content), &plan); err != nil || plan["planId"] != "incident-1" {
		t.Fatal("invalid plan")
	}
	if calls != 4 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestEnsureTaskNativeEngineReplay(t *testing.T) {
	binary := os.Getenv("CIHEAL_TEST_ENGINE")
	if binary == "" {
		t.Skip("set CIHEAL_TEST_ENGINE to the pinned noise-tasks executable")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Fixture"}, {"config", "user.email", "fixture@example.invalid"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README"}, {"commit", "-qm", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(binary, "init", "--tag", "master", "--json")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	e := Engine{Binary: binary, Family: dir, Tag: "master"}
	first, err := e.EnsureTask(context.Background(), "incident-1", "Repair", "Failure")
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.EnsureTask(context.Background(), "incident-1", "Repair", "Failure")
	if err != nil || first != second {
		t.Fatalf("first=%d second=%d err=%v", first, second, err)
	}
	if _, err := e.EnsureTask(context.Background(), "incident-1", "Different", "Failure"); err == nil {
		t.Fatal("changed replay payload accepted")
	}
}
