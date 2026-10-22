package deploy

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapacityExperimentDocRequiresEffectiveState(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "docs", "provider-contracts", "capacity-routing-experiment.md"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("internal documents are not part of a source export")
	}
	if err != nil {
		t.Fatalf("read capacity-routing experiment doc: %v", err)
	}
	// Enable and rollback must verify the running container's effective value.
	const effectiveStateCmd = "docker exec noisefloor-deck printenv NOISEDECK_EXPERIMENTS_UNCONFINED_CAPACITY_ROUTING"
	if got := strings.Count(string(doc), effectiveStateCmd); got != 2 {
		t.Errorf("experiment doc carries %d effective-state commands, want exactly 2 (enable and rollback): %q", got, effectiveStateCmd)
	}
	for _, want := range []string{"must print exactly `true`", "must print exactly `false`"} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("experiment doc does not require the exact effective value (%s)", want)
		}
	}
}

func TestModelOfferDesignContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "docs", "adr", "0015-reviewer-models-move-by-a-daily-offer.md"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("internal documents are not part of a source export")
	}
	if err != nil {
		t.Fatal(err)
	}
	adr := string(raw)
	// The ADR wraps prose; compare on collapsed whitespace.
	joined := strings.Join(strings.Fields(adr), " ")
	for _, required := range []string{
		"# ADR-0015: reviewer model pins move by a daily offer",
		"like-for-like",
		"Ambiguous and offer nothing",
		".noise/reviewer-models.json",
		"no model call is ever spent",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("ADR-0015 is missing %q", required)
		}
	}
}
