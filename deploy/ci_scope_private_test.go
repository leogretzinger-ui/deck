package deploy

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestProjectMergePreflight(t *testing.T) {
	cmd := exec.Command("python3", "-B", "verify-merge-gate-test.py")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("project merge preflight: %v %s", err, out)
	}
}

func TestPolicyRoutesToProjectMergePreflight(t *testing.T) {
	policy, err := os.ReadFile("../SDLC.md")
	if os.IsNotExist(err) {
		t.Skip("SDLC.md is absent from the source export")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(policy), "python3 deploy/verify-merge-gate.py --reviewer") {
		t.Fatal("SDLC must route to the versioned project preflight")
	}
}
