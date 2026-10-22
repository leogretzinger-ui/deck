package deploy

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMemPalaceHookForwarder(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python, "-B", "-m", "unittest", "-v", "test_hook_forwarder.py")
	command.Dir = filepath.Join("mempalace")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("MemPalace hook forwarder tests failed: %v\n%s", err, output)
	}
}

func TestMemPalacePiHook(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	command := exec.Command(node, "--test", "test_pi_hook.mjs")
	command.Dir = filepath.Join("mempalace")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("MemPalace Pi hook tests failed: %v\n%s", err, output)
	}
}
