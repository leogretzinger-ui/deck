package deploy

import (
	"os/exec"
	"testing"
)

func TestPrivateHookDiagnostics(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	cmd := exec.Command(python, "hook_diagnostics_test.py")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("private hook diagnostics tests: %v\n%s", err, output)
	}
}
