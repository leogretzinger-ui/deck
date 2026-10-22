package hookcapture

import "testing"

const storeTestSession = "8b2f7c1d-9a4e-4c3b-8d21-6f5e4a3b2c1d"

func TestClaudeStoreRootDerivesTheProjectsAncestor(t *testing.T) {
	root, err := ClaudeStoreRoot(
		"/home/op/.claude/projects/-workspace-app/"+storeTestSession+".jsonl",
		storeTestSession)
	if err != nil || root != "/home/op/.claude" {
		t.Fatalf("root = %q err=%v, want /home/op/.claude", root, err)
	}
}

func TestClaudeStoreRootRefusesNonLedgerShapes(t *testing.T) {
	cases := map[string]string{
		"relative":            "projects/-slug/" + storeTestSession + ".jsonl",
		"foreign session":     "/home/op/.claude/projects/-slug/other.jsonl",
		"no projects dir":     "/home/op/.claude/ledgers/-slug/" + storeTestSession + ".jsonl",
		"missing slug level":  "/home/op/.claude/projects/" + storeTestSession + ".jsonl",
		"rootless store":      "/projects/-slug/" + storeTestSession + ".jsonl",
		"bare projects chain": "/" + storeTestSession + ".jsonl",
	}
	for name, path := range cases {
		if root, err := ClaudeStoreRoot(path, storeTestSession); err == nil {
			t.Errorf("%s: derived %q from %q, want refusal", name, root, path)
		}
	}
}

// A claude event carrying a malformed transcript conflicts before acceptance:
// a store record that cannot be derived must never be half-recorded.
func TestValidateEventRefusesMalformedClaudeTranscript(t *testing.T) {
	workDir := t.TempDir()
	err := ValidateEvent("claude", workDir, Event{
		HookEventName: "SessionStart", SessionID: storeTestSession,
		CWD: workDir, Source: ExpectedSourceStartup,
		TranscriptPath: "not/a/ledger.jsonl",
	})
	if err == nil || ConflictReason(err) != "transcript_path" {
		t.Fatalf("malformed transcript = %v (%s), want transcript_path conflict",
			err, ConflictReason(err))
	}
	if err := ValidateEvent("claude", workDir, Event{
		HookEventName: "SessionStart", SessionID: storeTestSession,
		CWD: workDir, Source: ExpectedSourceStartup,
	}); err != nil {
		t.Fatalf("transcript-free event refused: %v", err)
	}
}
