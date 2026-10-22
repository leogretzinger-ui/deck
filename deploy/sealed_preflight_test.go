package deploy

import (
	"os/exec"
	"testing"
)

// The host-side preflight is Python and runs where the Deck host has Python
// and nothing else; its fixture tests run under the same interpreter here so
// a change to the classification rules fails this package.
func TestSealedPreflightFixtures(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python, "-B", "-m", "unittest", "-v", "test_sealed_preflight.py")
	command.Dir = "."
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("sealed-preflight fixture tests failed: %v\n%s", err, out)
	}
}
