package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"testing"
)

const claudeTestUUID = "1b501fb1-8f7c-4bb4-9422-0d4a1a68c9e7"

func mustClaudeHook(t *testing.T) *ClaudeDescriptor {
	t.Helper()
	d, err := NewClaude("/usr/local/bin/claude")
	if err != nil {
		t.Fatalf("NewClaude: %v", err)
	}
	return d
}

func TestClaudeArgvCarriesProvisionedHookSettings(t *testing.T) {
	d := mustClaudeHook(t)
	const settingsPath = "/tmp/noisedeck-sessions/x/g-a/claude-hook-settings.json"

	argv, err := d.NewSessionArgs(LaunchSpec{
		SessionID: claudeTestUUID, HookSettingsPath: settingsPath,
	})
	if err != nil {
		t.Fatalf("NewSessionArgs: %v", err)
	}
	if i := slices.Index(argv, "--settings"); i < 0 || i+1 >= len(argv) || argv[i+1] != settingsPath {
		t.Fatalf("launch argv = %v, want --settings %s", argv, settingsPath)
	}

	argv, err = d.ResumeSessionArgs(ResumeSpec{
		NativeSessionID: claudeTestUUID, HookSettingsPath: settingsPath,
	})
	if err != nil {
		t.Fatalf("ResumeSessionArgs: %v", err)
	}
	if i := slices.Index(argv, "--settings"); i < 0 || i+1 >= len(argv) || argv[i+1] != settingsPath {
		t.Fatalf("resume argv = %v, want --settings %s", argv, settingsPath)
	}
}

func TestClaudeArgvWithoutHookSettingsOmitsTheFlag(t *testing.T) {
	d := mustClaudeHook(t)
	argv, err := d.NewSessionArgs(LaunchSpec{SessionID: claudeTestUUID})
	if err != nil {
		t.Fatalf("NewSessionArgs: %v", err)
	}
	if slices.Contains(argv, "--settings") {
		t.Fatalf("launch argv = %v carries --settings with nothing provisioned", argv)
	}
	argv, err = d.ResumeSessionArgs(ResumeSpec{NativeSessionID: claudeTestUUID})
	if err != nil {
		t.Fatalf("ResumeSessionArgs: %v", err)
	}
	if slices.Contains(argv, "--settings") {
		t.Fatalf("resume argv = %v carries --settings with nothing provisioned", argv)
	}
}

func TestClaudeRefusesRelativeHookSettingsPath(t *testing.T) {
	d := mustClaudeHook(t)
	if _, err := d.NewSessionArgs(LaunchSpec{
		SessionID: claudeTestUUID, HookSettingsPath: "relative/settings.json",
	}); !errors.Is(err, ErrHookNotProvisioned) {
		t.Fatalf("launch err = %v, want ErrHookNotProvisioned", err)
	}
	if _, err := d.ResumeSessionArgs(ResumeSpec{
		NativeSessionID: claudeTestUUID, HookSettingsPath: "relative/settings.json",
	}); !errors.Is(err, ErrHookNotProvisioned) {
		t.Fatalf("resume err = %v, want ErrHookNotProvisioned", err)
	}
}

// claudeHookSettingsFile is Claude's documented settings shape for command
// hooks. Unrelated settings and hook events are intentionally ignored.
type claudeHookSettingsFile struct {
	Hooks map[string][]struct {
		Hooks []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// The provisioned settings document must be a single JSON value whose
// SessionStart hook invokes the provisioned receiver: a document that
// registers nothing would silently cost identity tracking on every launch.
func TestClaudeHookSettingsDocumentRegistersReceiver(t *testing.T) {
	var parsed claudeHookSettingsFile
	dec := json.NewDecoder(bytes.NewReader(ClaudeHookSettings))
	if err := dec.Decode(&parsed); err != nil {
		t.Fatalf("settings document is not valid JSON: %v", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		t.Fatal("settings document has content after the first JSON value")
	}
	d := mustClaudeHook(t)
	hook := d.IdentityHook()
	if hook.Event != "SessionStart" || hook.ReceiverPath != HookReceiverPath {
		t.Fatalf("identity hook = %+v", hook)
	}
	for _, group := range parsed.Hooks[hook.Event] {
		for _, entry := range group.Hooks {
			if entry.Type == "command" && entry.Command == hook.ReceiverPath {
				return
			}
		}
	}
	t.Fatalf("settings document has no %s hook invoking %s", hook.Event, hook.ReceiverPath)
}

func TestClaudeDeclaresExactResumeAttestation(t *testing.T) {
	var d Descriptor = mustClaudeHook(t)
	attestor, ok := d.(ResumeIdentityAttestationDescriptor)
	if !ok || attestor.ExactResumeHookSource() != "resume" {
		t.Fatalf("claude resume attestation = %v ok=%v", d, ok)
	}
	provisioned, ok := d.(ProvisionedHookSettingsDescriptor)
	if !ok || provisioned.HookSettingsFileName() != "claude-hook-settings.json" ||
		len(provisioned.HookSettingsDocument()) == 0 {
		t.Fatal("claude does not declare its provisioned hook settings")
	}
}
