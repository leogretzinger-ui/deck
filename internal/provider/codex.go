package provider

import (
	"fmt"
	"strings"
	"syscall"
)

// The contracts below were measured against Codex 0.145.0 and carried forward
// to the shipped 0.147.0; the conformance suite has not been rerun against it,
// so the values they declare are carried forward rather than verified.
// The no-daemon launch policy and deferred resume hook source are separately
// supported by the pinned 0.162.1 contract; other measurements remain historical.

// Codex hook trust is provider-owned state keyed by the exact hook file,
// event, and group/hook index. The current identity entries were accepted by
// Codex 0.162.1 on Linux arm64 (Docker on macOS) and Linux amd64 and match
// the identical entries in the owned memory composition. Memory hashes were
// measured by 0.147.0; 0.155.1 reproduced them and measured the a2a pair.
// Only exact owned hook compositions are eligible for provisioning these values.
const (
	CodexSessionStartTrustHash = "sha256:1c9347558100c3944c4811f2395489124b2ef14f8e7c752af83774ceb941f6e9"
	CodexStopTrustHash         = "sha256:73957a3fd7d0507af5c9168350e26925dea8384830c96217f2c045d87f548b63"

	CodexMempalaceSessionStartTrustHash = CodexSessionStartTrustHash
	CodexMempalaceStopTrustHash         = CodexStopTrustHash
	CodexMempalaceSessionMemoryHash     = "sha256:c168f94fc0b11d6a73551192239d9d0d3d351a8672a63f8c07ebd089f3ee8302"
	CodexMempalaceStopMemoryHash        = "sha256:44edd7944111d810d55e38c8a55bd4db3252165fa296e806c90cfbd370e36a5f"
	CodexMempalacePreCompactHash        = "sha256:dae151c7574dfb1ca7c569c92978700321cb6c85dcb9890cd99faca541076865"

	CodexMempalaceA2ASessionStartTrustHash = "sha256:e71e2b3e3145dbac81ac1340228af2406125b6f3b5d7e37a3f4cff057d60aef7"
	CodexMempalaceA2AStopTrustHash         = "sha256:339f4ef6cd2e2a03264ac7e47dbceb4783c385b5e64a0b1bf3c325d686208f8e"
)

// IdentityHook describes a provider callback that reports the native session
// ID of a session the provider just created.
//
// This is how an IdentityRetrieved provider establishes an exact native
// session ID without parsing anything the CLI printed. The distinction is not
// a technicality: scraping a startup banner
// couples us to presentation that can change for cosmetic reasons and gives no
// signal when it does, whereas a hook is a documented callback delivering a
// named field in structured JSON. If the field disappears, the hook stops
// producing it and the failure is immediate and legible.
type IdentityHook struct {
	// Event is the provider's hook event name.
	Event string
	// SessionIDField is the JSON key carrying the native session id.
	SessionIDField string
	// ReceiverPath is the provisioned capture receiver the hook command must
	// invoke exactly. It is deployment configuration (the image layout owns
	// it): the gate refuses a config whose SessionStart hook points anywhere
	// else, because such a hook can never deliver an identity here.
	ReceiverPath string
	// ConfigPath is the provider configuration file that registers hooks.
	// Installing the hook there is provisioning's job; Deck never writes
	// the user's provider configuration. It must verify the hook before every
	// launch or resume
	// and refuse with a remediation error naming this file when the hook is
	// missing or malformed, so a reset config surfaces at launch time rather
	// than silently stopping identity capture.
	ConfigPath string
	// Timing records when the structured callback becomes available. A caller
	// cannot safely treat an after-first-submission id as launch-time state.
	Timing IdentityHookTiming
	// RequiresNativeTrust means provisioning must establish the provider's own
	// hook-trust decision. Deck is forbidden to answer that modal.
	RequiresNativeTrust bool
}

// IdentityHookTiming is measured provider lifecycle behavior, not an inferred
// busy/idle state.
type IdentityHookTiming int

const (
	IdentityHookTimingUnknown IdentityHookTiming = iota
	IdentityHookAtProcessLaunch
	IdentityHookAfterFirstSubmission
)

// CodexDescriptor implements Descriptor for the Codex CLI.
//
// Codex cannot be told what session id to use, but it reports the id of the
// session it created through SessionStart and Stop hooks. Stop re-attests the
// current ID after native /resume changes the thread in place. Verified against 0.147.0 by
// installing a test-owned capture hook that retained only session_id, cwd,
// and source. It established that session_id is a canonical UUID, cwd is the
// test-owned project, startup reports source=startup, and exact resume reports
// source=resume with the same id. No native id or prompt content is retained in
// this durable contract evidence.
//
// Timing is part of the measured contract: no identity event was observed during
// a ten-second idle window after a fully provisioned launch became ready. The
// first observed startup event arrived after the first user submission and
// before that turn's reply; Stop followed the completed turn. Callers must represent that pending-id window
// honestly; they may not inject a synthetic prompt to force the hook.
//
//	codex resume $CAPTURED_ID             -> resumed the exact conversation
//	codex resume $UNKNOWN_CANONICAL_UUID  -> status 1 and no hook
//
// The unknown-id failure is what makes the contract usable: a resume that
// appears to succeed really did find the recorded session.
//
// The cost is that identity capture depends on a hook in the user's Codex
// configuration — provider state Deck does not own and never writes.
// Provisioning installs and natively trusts it; Deck must not answer the
// trust modal. That is why
// RequiredConfigPaths names the file: losing it does not just log the
// user out, it breaks identity capture for every new session, so Agent
// Console must verify the hook before every launch and refuse when it is
// gone.
type CodexDescriptor struct {
	execPath     string
	hookPath     string
	receiverPath string
}

// NewCodex builds a Codex descriptor. hookConfigPath is the provider hook
// configuration whose SessionStart hook Deck must verify before
// launch; provisioning installs it.
func NewCodex(execPath, hookConfigPath, receiverPath string) (*CodexDescriptor, error) {
	if !strings.HasPrefix(execPath, "/") {
		return nil, fmt.Errorf("provider: codex executable %q must be an absolute path", execPath)
	}
	if !strings.HasPrefix(hookConfigPath, "/") {
		return nil, fmt.Errorf("provider: codex hook config %q must be an absolute path", hookConfigPath)
	}
	if !strings.HasPrefix(receiverPath, "/") {
		return nil, fmt.Errorf("provider: codex hook receiver %q must be an absolute path", receiverPath)
	}
	return &CodexDescriptor{execPath: execPath, hookPath: hookConfigPath, receiverPath: receiverPath}, nil
}

func (c *CodexDescriptor) Name() Name                         { return Codex }
func (c *CodexDescriptor) Executable() string                 { return c.execPath }
func (c *CodexDescriptor) IdentityContract() IdentityContract { return IdentityRetrieved }
func (c *CodexDescriptor) IdentityCaptureMechanisms() []IdentityCaptureMechanism {
	return []IdentityCaptureMechanism{IdentityCaptureStructuredNativeHook}
}

// ExactResumeHookSource names the SessionStart source emitted for resumed
// history by Codex 0.162.1. Like startup, this hook is deferred until
// the first submission; recovery must preserve its grant after process commit.
func (c *CodexDescriptor) ExactResumeHookSource() string {
	return "resume"
}

// IdentityHook reports the callback that yields the native session id.
func (c *CodexDescriptor) IdentityHook() IdentityHook {
	return IdentityHook{
		Event:               "SessionStart",
		SessionIDField:      "session_id",
		ConfigPath:          c.hookPath,
		ReceiverPath:        c.receiverPath,
		Timing:              IdentityHookAfterFirstSubmission,
		RequiresNativeTrust: true,
	}
}

// Availability reports nil: Codex reports its exact native session ID through
// the SessionStart hook.
func (c *CodexDescriptor) Availability() error { return nil }

// NewSessionArgs launches a new interactive session.
//
// Unlike Claude, no id is supplied — Codex allocates it and reports it through
// the hook. LaunchSpec.SessionID is therefore ignored, and passing one is an
// error rather than being silently dropped: a caller that believes it assigned
// an id would record the wrong identity and be unable to resume.
func (c *CodexDescriptor) NewSessionArgs(spec LaunchSpec) ([]string, error) {
	if spec.SessionID != "" {
		return nil, fmt.Errorf(
			"provider: codex allocates its own session id and cannot be assigned "+
				"%q; capture the id from the %s hook instead",
			spec.SessionID, c.IdentityHook().Event)
	}
	return c.sessionArgs(spec.SkipPermissions), nil
}

// sessionArgs keeps each pane's hook environment in its own native server.
// Reusing Codex's daemon retains the first pane's callback token in later tabs.
//
// --no-alt-screen runs the TUI inline: transcript lines land in the normal
// screen buffer, so they scroll into tmux pane history, where Deck's
// scrollback and copy live. In the alternate screen nothing codex prints
// ever reaches the history, so output that scrolled off the visible screen
// cannot be reached or copied from the browser at all. The cost is inline
// rendering: full-screen redraws scroll through history as painted frames
// instead of staying on one clean screen. Verified against 0.162.1.
func (c *CodexDescriptor) sessionArgs(skipPermissions bool) []string {
	argv := []string{c.execPath, "--no-daemon", "--no-alt-screen"}
	if skipPermissions {
		// Verified against codex-cli 0.145.0. This flag drops BOTH the
		// approval prompts and codex's own sandbox — the two are one flag
		// there, unlike Claude's. Nothing weaker matches what the user
		// asked for: -a/--ask-for-approval only moves the prompt threshold.
		argv = append(argv, "--dangerously-bypass-approvals-and-sandbox")
	}
	return argv
}

// NewDelegatedSessionArgs uses Codex's documented interactive
// `codex --model MODEL [PROMPT]` form for the fixed task-reference bootstrap.
func (c *CodexDescriptor) NewDelegatedSessionArgs(spec LaunchSpec, delegated DelegatedLaunch) ([]string, error) {
	if err := validateDelegatedLaunch(delegated); err != nil {
		return nil, err
	}
	argv, err := c.NewSessionArgs(spec)
	if err != nil {
		return nil, err
	}
	return append(argv, "--model", delegated.Model, delegated.Prompt), nil
}

// ResumeSessionArgs resumes an exact recorded session.
//
// The recorded id is replayed verbatim: the CLI owns its own id format, and
// a format change upstream must never strand a recorded session. Only an
// empty id is refused — an empty recording is a bookkeeping bug on our
// side, not a provider format change.
func (c *CodexDescriptor) ResumeSessionArgs(spec ResumeSpec) ([]string, error) {
	id := spec.NativeSessionID
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("provider: codex resume requires a native session id")
	}
	// Positional id only. --last resumes "the most recent recorded session",
	// which is not exact identity, so it must never appear here.
	return append(c.sessionArgs(spec.SkipPermissions), "resume", id), nil
}

func (c *CodexDescriptor) Environment() []string { return nil }

func (c *CodexDescriptor) PermittedEnvironment() []string {
	return []string{"HOME", "PATH", "TERM", "LANG", "LC_ALL", "CODEX_HOME"}
}

func (c *CodexDescriptor) RequiredConfigPaths() []string {
	return []string{"~/.codex", c.hookPath}
}

// ExecutableIdentity: Codex is a Node script, so nothing in /proc identifies
// it. Measured: /proc/exe is the node interpreter, and /proc/cmdline shows the
// SYMLINK path rather than the resolved script.
func (c *CodexDescriptor) ExecutableIdentity() ExecutableIdentity {
	return ExecutableIdentity{
		Interpreted:     true,
		ExpectedProcExe: "/usr/local/bin/node",
		LaunchTarget:    "/usr/local/lib/node_modules/@openai/codex/bin/codex.js",
		ObservedCmdline: "node /usr/local/bin/codex",
	}
}

// Injection returns the contract measured against the real 0.145.0 TUI in a
// private tmux namespace. The conformance harness may observe terminal output
// to establish this version-pinned test evidence; Deck runtime may
// not parse provider presentation to infer mode or delivery.
func (c *CodexDescriptor) Injection() InjectionContract {
	return InjectionContract{
		UseBracketedPaste:        true,
		SubmitKey:                "Enter",
		SupportsMultiline:        true,
		AcceptsInputDuringTurn:   ActiveTurnQueued,
		DiscardsInputDuringModal: true,
		Verified:                 true,
	}
}

func (c *CodexDescriptor) GracefulStopSignal() syscall.Signal { return syscall.SIGTERM }
func (c *CodexDescriptor) ForceStopSignal() syscall.Signal    { return syscall.SIGKILL }
