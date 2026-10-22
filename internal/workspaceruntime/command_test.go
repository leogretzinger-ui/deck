package workspaceruntime

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestExecRunnerDiscardsSecretOutput(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("shell unavailable")
	}
	const synthetic = "public-test-secret-not-a-credential"
	out, err := (execRunner{}).run(context.Background(), shell, []string{"-c", `IFS= read -r input; printf '%s' "$input"; printf '%s' "$input" >&2; exit 7`}, []string{"PATH=/usr/bin:/bin"}, []byte(synthetic+"\n"), false)
	if len(out) != 0 || exitCode(err) != 7 || strings.Contains(err.Error(), synthetic) {
		t.Fatalf("native output escaped discarded streams: bytes=%d err=%v", len(out), err)
	}
}

func TestExecRunnerDoesNotInheritAmbientEnvironment(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("shell unavailable")
	}
	t.Setenv("DECK_NATIVE_TEST_AMBIENT", "must-not-inherit")
	out, err := (execRunner{}).run(context.Background(), shell, []string{"-c", `printf '%s' "${DECK_NATIVE_TEST_AMBIENT-unset}"`}, []string{"PATH=/usr/bin:/bin"}, nil, true)
	if err != nil || string(out) != "unset" {
		t.Fatal("native command inherited controller environment")
	}
}

func TestCommandOutputIsBounded(t *testing.T) {
	var output boundedOutput
	for range 100 {
		output.Write([]byte(strings.Repeat("x", 1024)))
	}
	if !output.overflow || output.Len() > 64*1024 {
		t.Fatal("native observation output is not bounded")
	}
}
