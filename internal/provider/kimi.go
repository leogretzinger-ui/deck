package provider

import (
	"fmt"
	"strings"
	"syscall"
)

// The identity and resume contracts below were measured against Kimi 0.39.0.
// The injection contract has never been measured, and the conformance suite
// has not been rerun against this version.

const kimiSessionStartConfigProbe = `[[hooks]]
event = "SessionStart"
command = "/usr/local/bin/noisedeck-provider-hook"
`

// KimiDescriptor implements Descriptor for the Kimi Code CLI.
//
// Like Codex, Kimi allocates its own session id and reports it through a
// SessionStart hook. Re-verified against 0.39.0 by installing a capture hook
// and reading its payload:
//
//	{"hook_event_name":"SessionStart",
//	 "session_id":"session_6e37a411-0c3c-4d69-9710-eca6af72e925",
//	 "cwd":"/…","client_type":"kimi_code_cli","source":"startup",
//	 "model":"kimi-code/k3-256k","profile":"agent"}
//
//	kimi -S session_6e37a411-…   -> resumed, context restored
//	kimi -S session_00000000-…   -> `Session "…" not found.`
//
// The event carries source:"startup", but it does NOT arrive at process
// launch: 0.39.0 holds it until the user's first submission, measured
// across a full launch that reached the prompt and sat silent. The timing
// this descriptor declares is therefore after-first-submission - a kimi
// pane waiting at its prompt has no id to report yet, and that is an empty
// lazy session, not a broken identity path.
//
// Note the id format differs from Codex's: Kimi prefixes a "session_" string
// before the UUID, so it is not parseable as a bare UUID and must be stored and
// replayed verbatim.
//
// Kimi's hook configuration is TOML with a FLAT shape — `[[hooks]]` entries
// carrying event/command directly. It is not the nested arrays-of-hooks
// structure Codex uses, and an earlier attempt to configure it by analogy with
// Codex was rejected by `kimi doctor`. Only event, matcher, command, and
// timeout are permitted keys; extra keys make the whole config fail to load,
// which means a careless write here breaks every Kimi session, not just ours.
type KimiDescriptor struct {
	execPath     string
	hookPath     string
	receiverPath string
}

// NewKimi builds a Kimi descriptor. hookConfigPath is the TOML configuration
// whose SessionStart hook Deck must verify before launch;
// provisioning installs it.
func NewKimi(execPath, hookConfigPath, receiverPath string) (*KimiDescriptor, error) {
	if !strings.HasPrefix(execPath, "/") {
		return nil, fmt.Errorf("provider: kimi executable %q must be an absolute path", execPath)
	}
	if !strings.HasPrefix(hookConfigPath, "/") {
		return nil, fmt.Errorf("provider: kimi hook config %q must be an absolute path", hookConfigPath)
	}
	if !strings.HasPrefix(receiverPath, "/") {
		return nil, fmt.Errorf("provider: kimi hook receiver %q must be an absolute path", receiverPath)
	}
	return &KimiDescriptor{execPath: execPath, hookPath: hookConfigPath, receiverPath: receiverPath}, nil
}

func (k *KimiDescriptor) Name() Name                         { return Kimi }
func (k *KimiDescriptor) Executable() string                 { return k.execPath }
func (k *KimiDescriptor) IdentityContract() IdentityContract { return IdentityRetrieved }
func (k *KimiDescriptor) IdentityCaptureMechanisms() []IdentityCaptureMechanism {
	return []IdentityCaptureMechanism{IdentityCaptureStructuredNativeHook}
}

// IdentityHook reports the callback that yields the native session id. The
// event arrives at the first submission, so a pane that has reported no id
// has simply not been typed into yet.
func (k *KimiDescriptor) IdentityHook() IdentityHook {
	return IdentityHook{
		Event:          "SessionStart",
		SessionIDField: "session_id",
		ConfigPath:     k.hookPath,
		ReceiverPath:   k.receiverPath,
		Timing:         IdentityHookAfterFirstSubmission,
	}
}

// Availability reports nil: Kimi reports its exact native session ID through
// the SessionStart hook.
func (k *KimiDescriptor) Availability() error { return nil }

// NewSessionArgs launches a new interactive session.
//
// Kimi allocates its own id, so supplying one is an error rather than being
// silently dropped: a caller that believed it assigned an id would record the
// wrong identity and never be able to resume.
func (k *KimiDescriptor) NewSessionArgs(spec LaunchSpec) ([]string, error) {
	if spec.SessionID != "" {
		return nil, fmt.Errorf(
			"provider: kimi allocates its own session id and cannot be assigned "+
				"%q; capture the id from the %s hook instead",
			spec.SessionID, k.IdentityHook().Event)
	}
	argv := []string{k.execPath}
	if spec.SkipPermissions {
		// Verified against kimi 0.29.2 and carried forward: --auto is
		// "fully autonomous, the
		// agent will not ask". -y/--yolo is the WEAKER one (auto-approves
		// regular tool calls but still asks questions), so --auto is the
		// honest match for a setting that says permissions are skipped.
		argv = append(argv, "--auto")
	}
	return argv, nil
}

// ResumeSessionArgs resumes an exact recorded session.
//
// The recorded id is replayed verbatim: the CLI owns its own id format, and
// a format change upstream must never strand a recorded session. Only an
// empty id is refused — an empty recording is a bookkeeping bug on our
// side, not a provider format change.
func (k *KimiDescriptor) ResumeSessionArgs(spec ResumeSpec) ([]string, error) {
	id := strings.TrimSpace(spec.NativeSessionID)
	if id == "" {
		return nil, fmt.Errorf("provider: kimi resume requires a native session id")
	}
	// -S takes the id as a value. --continue is never used: it resumes "the
	// previous session for the working directory", which is ambiguous the
	// moment a project has more than one session, and is not exact identity.
	argv := []string{k.execPath, "-S", id}
	if spec.SkipPermissions {
		argv = append(argv, "--auto")
	}
	return argv, nil
}

func (k *KimiDescriptor) Environment() []string { return nil }

func (k *KimiDescriptor) PermittedEnvironment() []string {
	// KIMI_CODE_HOME relocates Kimi's config/credentials home (verified:
	// `kimi doctor` resolves config.toml beneath it), letting tests and
	// deployments target an explicit home the way CODEX_HOME does for Codex.
	return []string{"HOME", "PATH", "TERM", "LANG", "LC_ALL", "KIMI_CODE_HOME"}
}

func (k *KimiDescriptor) RequiredConfigPaths() []string {
	return []string{"~/.kimi-code", k.hookPath}
}

// ExecutableIdentity: Kimi is the strongest case for why executable evidence
// cannot be trusted here. /proc/exe is the node interpreter — the same value
// Codex reports — and Kimi has rewritten argv[0], so /proc/cmdline is just
// "kimi-code" with no path in it at all.
func (k *KimiDescriptor) ExecutableIdentity() ExecutableIdentity {
	return ExecutableIdentity{
		Interpreted:     true,
		ExpectedProcExe: "/usr/local/bin/node",
		LaunchTarget:    "/usr/local/lib/node_modules/@moonshot-ai/kimi-code/dist/main.mjs",
		ObservedCmdline: "kimi-code",
	}
}

func (k *KimiDescriptor) Injection() InjectionContract {
	return InjectionContract{
		UseBracketedPaste:      true,
		SubmitKey:              "Enter",
		SupportsMultiline:      true,
		AcceptsInputDuringTurn: ActiveTurnUnknown,
		Verified:               false,
	}
}

func (k *KimiDescriptor) GracefulStopSignal() syscall.Signal { return syscall.SIGTERM }
func (k *KimiDescriptor) ForceStopSignal() syscall.Signal    { return syscall.SIGKILL }
