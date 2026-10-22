package tasks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/noisefloor-oss/deck/internal/provider"
)

type unsupportedGenerationHarness struct {
	name string
}

func (e *unsupportedGenerationHarness) Error() string {
	return fmt.Sprintf("%s can't generate tasks yet. Add the first task by hand.", e.name)
}

type generationHarness struct {
	selector string
	path     string
}

func (r *Runner) generationHarness(root *taskRoot, harness string) (*generationHarness, error) {
	if harness == "" {
		return nil, nil
	}
	if _, err := root.stat(root.preferencesPath); err == nil {
		return nil, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	selection := &generationHarness{path: os.Getenv("PATH")}
	switch harness {
	case "claude":
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			selection.selector = "claude-cli"
		}
	case "codex", "kimi", "omp":
		selection.selector = harness
	default:
		name := harness
		if name == "shell" || name == "terminal" {
			name = "Terminal"
		}
		return nil, &unsupportedGenerationHarness{name: name}
	}
	if p := r.providers[provider.Name(harness)]; p != nil {
		selection.path = filepath.Dir(p.Executable()) + string(os.PathListSeparator) + selection.path
	}
	return selection, nil
}
