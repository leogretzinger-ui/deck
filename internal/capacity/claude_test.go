package capacity

import (
	"encoding/json"
	"testing"
	"time"
)

func claudeJSON(t *testing.T, result string) []byte {
	t.Helper()
	raw, err := json.Marshal(claudeCommandResult{Result: result})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseClaudeModelsUsesProviderReportedAliases(t *testing.T) {
	raw := claudeJSON(t, "Current model: Fable 5\nUsage: /model <name>. Available: sonnet, opus, haiku, fable, best, sonnet[1m], opus[1m], fable[1m], opusplan, default, or a full model ID.")
	models := parseClaudeModels(raw)
	if len(models) != 10 {
		t.Fatalf("models = %#v", models)
	}
	if models[3].ID != "fable" || models[9].ID != "default" {
		t.Fatalf("models = %#v", models)
	}
}

func TestParseClaudeUsagePreservesModelScopedWindow(t *testing.T) {
	observed := time.Date(2026, 8, 14, 19, 0, 0, 0, time.UTC)
	raw := claudeJSON(t, "Current session: 0% used · resets Aug 14, 11:50pm (UTC)\nCurrent week (all models): 24% used · resets Aug 18, 6am (UTC)\nCurrent week (Fable): 27% used · resets Aug 18, 6am (UTC)")
	windows := parseClaudeUsage(raw, observed)
	if len(windows) != 3 {
		t.Fatalf("windows = %#v", windows)
	}
	if windows[2].Name != "week" || windows[2].Scope != "model:fable" || windows[2].UsedPercent != 27 {
		t.Fatalf("Fable window = %#v", windows[2])
	}
	wantReset := time.Date(2026, 8, 18, 6, 0, 0, 0, time.UTC)
	if windows[2].ResetsAt == nil || !windows[2].ResetsAt.Equal(wantReset) {
		t.Fatalf("reset = %v, want %v", windows[2].ResetsAt, wantReset)
	}
}

func TestParseClaudeUsageRejectsInvalidPercent(t *testing.T) {
	raw := claudeJSON(t, "Current week (Fable): 101% used · resets Aug 18, 6am (UTC)")
	if windows := parseClaudeUsage(raw, time.Now()); len(windows) != 0 {
		t.Fatalf("windows = %#v", windows)
	}
}
