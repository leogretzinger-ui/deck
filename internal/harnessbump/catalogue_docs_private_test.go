package harnessbump

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogueMatchesContractReadme(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	pins := ReadPins(dockerfile)
	readme, err := os.ReadFile(filepath.Join("..", "..", "docs", "provider-contracts", "README.md"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("internal documents are not part of a source export")
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range Catalogue {
		for _, r := range h.Repeats {
			if r.Path != "docs/provider-contracts/README.md" {
				continue
			}
			if !strings.Contains(string(readme), fmt.Sprintf(r.Format, pins[h.VersionArg])) {
				t.Errorf("%s: %s does not spell the shipped version as %q", h.Name, r.Path, fmt.Sprintf(r.Format, pins[h.VersionArg]))
			}
		}
	}
}
