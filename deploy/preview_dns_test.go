package deploy

import (
	"os/exec"
	"testing"
)

func TestPreviewDNSReconciler(t *testing.T) {
	if out, err := exec.Command("python3", "preview_dns_test.py").CombinedOutput(); err != nil {
		t.Fatalf("preview DNS tests: %v\n%s", err, out)
	}
}
