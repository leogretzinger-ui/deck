package supervisor

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Destination changes require a matching confinement profile and plan schema.
func TestConfinedLinuxDocRatifiesSandboxPlanDestinations(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "security", "confined-linux-v1.md"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("internal documents are not part of a source export")
	}
	if err != nil {
		t.Fatalf("read confined-linux-v1 profile doc: %v", err)
	}
	lines := make([]string, 0, len(sandboxPlanRoleOrder))
	for _, role := range sandboxPlanRoleOrder {
		lines = append(lines, string(role)+" "+sandboxPlanDestinations[role])
	}
	vector := strings.Join(lines, "\n") + "\n"
	if !strings.Contains(string(doc), vector) {
		t.Errorf("profile doc does not ratify the exact ordered destination vector:\n%s", vector)
	}
	const revisionRule = "both a new confinement profile revision and a new plan schema"
	if !strings.Contains(string(doc), revisionRule) {
		t.Errorf("profile doc does not bind destination changes to %q", revisionRule)
	}
}
