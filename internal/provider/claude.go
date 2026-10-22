package provider

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// The launch, resume, and hook contracts below were measured by hand against
// Claude Code 2.1.241. See the package tests for what "measured" means here.

// ClaudeHookSettings is the exact settings document Deck provisions per launch
// and names with --settings. Its SessionStart hook invokes the provisioned
// capture receiver, which reads the endpoint and credential from the launched
// process's environment. Exported so tests and provisioning tooling compare
// the same bytes the launch writes.
//
//go:embed assets/claude-hook-settings.json
var ClaudeHookSettings []byte

// uuidPattern matches the canonical UUID form Claude requires for --session-id.
var uuidPattern = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ClaudeDescriptor implements Descriptor for the Claude Code CLI.
//
// Claude's identity is assigned-then-tracked. Deck assigns the session ID at
// launch with --session-id, so the birth identity is known before the process
// starts — and Claude Code can then MOVE the conversation to a new id
// (fork, /clear, compact-class entries), which its native SessionStart hook
// reports. The assigned id is the birth identity and the initial --resume
// currency; the hook is what keeps the recorded identity current afterwards.
// Verified against 2.1.241:
//
//	claude --session-id <uuid> ...      -> session created with that id
//	claude --resume <uuid> ...          -> resumes that exact conversation,
//	                                       KEEPING the same session id
//	claude --resume <unknown-uuid>      -> rc=1, "No conversation found with
//	                                       session ID: ..."
//	claude --session-id <used-uuid>     -> rc=1, "Session ID ... is already
//	                                       in use"
//
// The unknown-uuid line matters as much as the first two: resume fails loudly
// rather than silently starting a fresh conversation, so a resume that appears
// to succeed really did find the recorded session. The already-in-use line is
// its counterpart on the assign side: an assigned id is single-use the moment
// Claude writes its ledger file (<claude-home>/projects/<cwd-slug>/<uuid>.jsonl),
// even when the first launch died before the model replied — a relaunch must
// resume the recorded id or assign a fresh one, never re-assign the old one.
//
// Measured against 2.1.261, the ledger does not exist until the FIRST
// SUBMISSION: a launch that reaches the prompt and dies there — or dies in
// the workspace-trust, onboarding or login dialogs — leaves nothing, so
// `--resume <assigned-uuid>` answers "No conversation found" and exits 1,
// while `--session-id <assigned-uuid>` is accepted again. An assigned id is
// therefore a name Deck chose, not proof that a conversation exists; only
// the provider's answer to the resume says that. The dialogs also run BEFORE
// the id is looked up, so that answer can arrive minutes after launch, once
// the user has answered them. SessionStart does not fire for a refused
// resume; SessionEnd does, with reason "other".
//
// The SessionStart hook fires on EVERY entry with JSON on stdin:
//
//	{"session_id","transcript_path","cwd","hook_event_name":"SessionStart",
//	 "source"}  where source ∈ startup | resume | fork (docs also list
//	clear and compact). SessionEnd fires symmetrically with a reason.
//
// source startup/resume report the CURRENT id unchanged (confirmation);
// fork/clear/compact-class entries continue the context under a NEW id, and
// the hook reports that new id — which is exactly the drift the capture
// pipeline follows. transcript_path in the payload is the ledger file above.
//
// The hook is provisioned through `--settings <file>`: a hooks-only settings
// JSON named there COMPOSES with the user's own settings, so Deck injects
// its SessionStart capture hook per launch without ever writing the user's
// ~/.claude or the project's .claude/settings.json.
type ClaudeDescriptor struct {
	execPath string
}

// NewClaude builds a Claude descriptor. execPath must be absolute.
func NewClaude(execPath string) (*ClaudeDescriptor, error) {
	if !strings.HasPrefix(execPath, "/") {
		return nil, fmt.Errorf("provider: claude executable %q must be an absolute path", execPath)
	}
	return &ClaudeDescriptor{execPath: execPath}, nil
}

func (c *ClaudeDescriptor) Name() Name                         { return Claude }
func (c *ClaudeDescriptor) Executable() string                 { return c.execPath }
func (c *ClaudeDescriptor) IdentityContract() IdentityContract { return IdentityAssigned }

// IdentityCaptureMechanisms: assigned-at-launch remains first — the assigned
// uuid is the birth identity and the --resume currency — and the structured
// SessionStart hook is what keeps the recorded identity tracking the
// conversation across the provider's own id rolls.
func (c *ClaudeDescriptor) IdentityCaptureMechanisms() []IdentityCaptureMechanism {
	return []IdentityCaptureMechanism{
		IdentityCaptureAssignedAtLaunch,
		IdentityCaptureStructuredNativeHook,
	}
}

// IdentityHook reports the SessionStart callback. ConfigPath is empty because
// the registration is not a pre-installed file: Deck provisions the settings
// document (ClaudeHookSettings) per launch and names it with --settings.
// Timing is measured: SessionStart fires at process entry, before the first
// submission.
func (c *ClaudeDescriptor) IdentityHook() IdentityHook {
	return IdentityHook{
		Event:          "SessionStart",
		SessionIDField: "session_id",
		ReceiverPath:   HookReceiverPath,
		Timing:         IdentityHookAtProcessLaunch,
	}
}

// ExactResumeHookSource: measured against 2.1.241, `claude --resume <uuid>`
// fires SessionStart with source "resume" and the SAME session id, so a
// resumed generation can be attested against the exact recorded identity.
func (c *ClaudeDescriptor) ExactResumeHookSource() string { return "resume" }

// HookSettingsDocument returns the exact per-launch settings file content.
func (c *ClaudeDescriptor) HookSettingsDocument() []byte { return ClaudeHookSettings }

// HookSettingsFileName names the document inside the generation's Deck-owned
// directory.
func (c *ClaudeDescriptor) HookSettingsFileName() string { return "claude-hook-settings.json" }

// CurrentStoreRoot resolves the store the next launch would read, exactly as
// the pane would: launchArgv passes HOME and CLAUDE_CONFIG_DIR from the
// service environment (PermittedEnvironment), and Claude reads
// CLAUDE_CONFIG_DIR when set, else $HOME/.claude. The ledger measured against
// 2.1.241 lives at <store-root>/projects/<cwd-slug>/<session-uuid>.jsonl.
func (c *ClaudeDescriptor) CurrentStoreRoot() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); strings.TrimSpace(dir) != "" {
		return filepath.Clean(dir), nil
	}
	home := os.Getenv("HOME")
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("%w: claude resolves no store without HOME or CLAUDE_CONFIG_DIR",
			ErrStoreRootUnresolvable)
	}
	return filepath.Join(home, ".claude"), nil
}

// StorePinEnvironment pins a launch to the recorded store root. The caller
// injects this into the launch's extra environment, which launchArgv gives
// precedence over both the PermittedEnvironment-inherited user value and
// Environment() additions — the explicit pin must win, or a diverged
// CLAUDE_CONFIG_DIR would silently send the resume to the wrong store, which
// is the exact failure this exists to name.
func (c *ClaudeDescriptor) StorePinEnvironment(recordedRoot string) []string {
	return []string{"CLAUDE_CONFIG_DIR=" + recordedRoot}
}

// Availability reports nil: Deck assigns Claude's exact native session ID
// at launch.
func (c *ClaudeDescriptor) Availability() error { return nil }

// NewSessionArgs builds argv for a new session with an assigned identity.
//
// The working directory is NOT passed to Claude. tmux sets the pane's working
// directory, so passing it again would create two sources of truth for
// the same fact and let them disagree.
func (c *ClaudeDescriptor) NewSessionArgs(spec LaunchSpec) ([]string, error) {
	if err := validateSessionID(spec.SessionID); err != nil {
		return nil, err
	}
	argv := []string{c.execPath, "--session-id", spec.SessionID}
	settings, err := claudeSettingsArgs(spec.HookSettingsPath)
	if err != nil {
		return nil, err
	}
	argv = append(argv, settings...)
	if spec.SkipPermissions {
		// Verified against claude 2.1.241: --dangerously-skip-permissions
		// bypasses all permission checks. (--allow-dangerously-skip-permissions
		// merely ENABLES the former in environments that gate it; it is not
		// itself the bypass, so it is not what we pass.)
		argv = append(argv, "--dangerously-skip-permissions")
	}
	return argv, nil
}

// NewDelegatedSessionArgs uses Claude's documented interactive
// `claude [prompt]` form. The prompt is the fixed task-reference
// bootstrap; semantic task content remains in the canonical project files.
func (c *ClaudeDescriptor) NewDelegatedSessionArgs(spec LaunchSpec, delegated DelegatedLaunch) ([]string, error) {
	if err := validateDelegatedLaunch(delegated); err != nil {
		return nil, err
	}
	argv, err := c.NewSessionArgs(spec)
	if err != nil {
		return nil, err
	}
	return append(argv, "--model", delegated.Model, delegated.Prompt), nil
}

// ResumeSessionArgs builds argv to resume an exact recorded session.
func (c *ClaudeDescriptor) ResumeSessionArgs(spec ResumeSpec) ([]string, error) {
	if err := validateSessionID(spec.NativeSessionID); err != nil {
		return nil, err
	}
	// --resume takes the id as a value. It is never combined with --continue or
	// --fork-session: --continue resumes "the most recent conversation", which
	// is not exact identity, and --fork-session deliberately creates a NEW id,
	// which would silently orphan the session we recorded. (When the USER
	// forks inside the TUI, the SessionStart hook reports the new id and the
	// recorded identity follows it — Deck itself never asks for a fork.)
	argv := []string{c.execPath, "--resume", spec.NativeSessionID}
	settings, err := claudeSettingsArgs(spec.HookSettingsPath)
	if err != nil {
		return nil, err
	}
	argv = append(argv, settings...)
	if spec.SkipPermissions {
		argv = append(argv, "--dangerously-skip-permissions")
	}
	return argv, nil
}

// claudeSettingsArgs names the Deck-provisioned hook settings file. An empty
// path is a degraded launch, not an error: the assigned identity still works,
// only tracking across the provider's id rolls is lost, and the launch caller
// owns that decision. A relative path is refused — argv is executed without a
// shell from a working directory Deck does not control, so a relative settings
// path would silently register nothing.
func claudeSettingsArgs(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("%w: claude hook settings path %q must be absolute",
			ErrHookNotProvisioned, path)
	}
	return []string{"--settings", path}, nil
}

// Environment returns nothing. Claude reads its credentials and configuration
// from its own persisted home; Deck neither supplies nor
// relays them, so there is nothing to add and no secret to leak into argv or
// the process environment. The capture endpoint and credential are per-launch
// values the launch caller injects; the SessionStart hook command inherits
// them from the pane process.
func (c *ClaudeDescriptor) Environment() []string { return nil }

// PermittedEnvironment is an allowlist, not a denylist. The service process
// carries the user's shell environment, which routinely holds API keys for
// unrelated tools; inheriting it wholesale would hand a provider credentials it
// has no business seeing.
func (c *ClaudeDescriptor) PermittedEnvironment() []string {
	return []string{"HOME", "PATH", "TERM", "LANG", "LC_ALL", "CLAUDE_CONFIG_DIR"}
}

// RequiredConfigPaths names what a deployment must persist. Losing this does
// not merely log the user out — it destroys the native sessions recovery
// resumes.
func (c *ClaudeDescriptor) RequiredConfigPaths() []string {
	return []string{"~/.claude"}
}

// ExecutableIdentity: Claude is a native binary, so the kernel executable is
// the provider itself and matching it is sufficient.
//
// The path is version-stamped (~/.local/share/claude/versions/<version>) behind
// a stable symlink on PATH, so the expected value MOVES on every upgrade. It is
// derived from the launch path at launch rather than configured, or the first
// provider update would look like an identity mismatch on every session.
func (c *ClaudeDescriptor) ExecutableIdentity() ExecutableIdentity {
	return ExecutableIdentity{Interpreted: false}
}

// Injection reports how input reaches Claude's TUI.
//
// Verified is true: every field was measured against 2.1.220 in a real tmux
// pane during the Phase 0 spike, not declared. See
// docs/provider-contracts/claude.yaml for what each measurement was.
//
//   - bracketed paste + Enter delivers and submits;
//   - a body containing a newline arrives as two input lines and is NOT
//     submitted until Enter is sent separately;
//   - input submitted mid-turn is QUEUED by the CLI ("Press up to edit queued
//     messages") and processed exactly once after the turn finishes;
//   - input delivered while a modal prompt is open is silently DISCARDED.
func (c *ClaudeDescriptor) Injection() InjectionContract {
	return InjectionContract{
		UseBracketedPaste:        true,
		SubmitKey:                "Enter",
		SupportsMultiline:        true,
		AcceptsInputDuringTurn:   ActiveTurnQueued,
		DiscardsInputDuringModal: true,
		Verified:                 true,
	}
}

// GracefulStopSignal is SIGTERM; ForceStopSignal is SIGKILL.
//
// Measured: Claude HANDLES SIGTERM and exits 143 itself, so tmux records
// pane_dead_status=143 with an EMPTY pane_dead_signal — unlike a process that
// dies from the signal, which reports pane_dead_signal=15 and no status. A
// reconciler that expected a signal here would record no exit cause at all.
//
// No keyboard exit path was established: "/exit", Ctrl-D, and Ctrl-C twice all
// left the process running.
func (c *ClaudeDescriptor) GracefulStopSignal() syscall.Signal { return syscall.SIGTERM }
func (c *ClaudeDescriptor) ForceStopSignal() syscall.Signal    { return syscall.SIGKILL }

// validateSessionID enforces the UUID form Claude requires.
//
// Rejecting a malformed id here rather than letting the CLI reject it matters
// because the failure modes differ: we would otherwise launch a pane, have the
// provider exit immediately, and have to reconstruct why from an exit status.
func validateSessionID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("provider: session id is required")
	}
	if !uuidPattern.MatchString(id) {
		return fmt.Errorf(
			"provider: session id %q is not a canonical UUID; claude --session-id "+
				"requires one and would reject this at launch", id)
	}
	return nil
}
