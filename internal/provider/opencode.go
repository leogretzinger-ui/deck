package provider

import (
	"fmt"
	"strings"
	"syscall"
)

// The contracts below were measured against opencode 1.18.4 and carried
// forward to the shipped 1.18.15; the conformance suite has not been rerun
// against it.

// OpencodeDescriptor implements Descriptor for the opencode CLI.
//
// Like Codex and Kimi, opencode allocates its own session id and reports it
// through a hook — here, a provisioning-installed plugin that subscribes to
// the plugin event bus and POSTs the payload to Deck's capture endpoint.
// Verified against 1.18.4 by installing a JS plugin at the provisioning path
// and reading the delivered payload:
//
//	{"hook_event_name":"SessionStart",
//	 "session_id":"ses_032d1af39ffejke3TwnEL00DxZ",
//	 "cwd":"<test wd>","source":"startup"}
//
// The native id lives at properties.info.id inside the plugin event payload;
// the plugin must extract it and forward it under the session_id field the
// capture endpoint expects. Format is "ses_" + 26 base62 chars — store verbatim,
// do not parse.
//
// Timing is part of the measured contract: opencode creates sessions LAZILY —
// no session id exists at process launch at all. Zero session.created events
// during a ten-second idle window after the TUI became ready; the first event
// arrived 0.4-0.6s after the first user submission and before that turn's
// reply. Resume launches emit NO session.created event. Callers must represent
// that pending-id window honestly; they may not inject a synthetic prompt.
//
// Delivery is AT-LEAST-ONCE: opencode reloads plugins in place (~hourly bursts
// observed), so session.created duplicates are normal and a receiver must
// dedupe on session_id.
//
//	opencode -s ses_032d1af39ffejke3TwnEL00DxZ  -> resumed, history intact
//	opencode -s ses_000…                        -> exit 1, "Session not found"
//
// The unknown-id failure makes the contract usable: a resume that appears to
// succeed really did find the recorded session.
//
// The hook mechanism is a plugin file, not a config entry: any .js/.ts file in
// ~/.config/opencode/plugins/ is loaded at startup. Provisioning installs the
// plugin; Deck never writes the user's plugin directory and must
// verify the plugin before every launch/resume, refusing with a remediation
// error when it is missing or malformed. opencode has no native trust modal
// for plugins — they load silently — so RequiresNativeTrust is false, but a
// wiped plugins dir breaks capture just as completely.
//
// `--pure` runs opencode WITHOUT external plugins, silently disabling identity
// capture — launch/resume argv must never contain it.
type OpencodeDescriptor struct {
	execPath string
	hookPath string
}

// NewOpencode builds an opencode descriptor. hookConfigPath is the absolute
// path to the provisioning-installed plugin file (e.g.
// ~/.config/opencode/plugins/noisedeck-provider-hook.js). Both must be absolute.
func NewOpencode(execPath, hookConfigPath string) (*OpencodeDescriptor, error) {
	if !strings.HasPrefix(execPath, "/") {
		return nil, fmt.Errorf("provider: opencode executable %q must be an absolute path", execPath)
	}
	if !strings.HasPrefix(hookConfigPath, "/") {
		return nil, fmt.Errorf("provider: opencode hook plugin %q must be an absolute path", hookConfigPath)
	}
	return &OpencodeDescriptor{execPath: execPath, hookPath: hookConfigPath}, nil
}

func (o *OpencodeDescriptor) Name() Name                         { return Opencode }
func (o *OpencodeDescriptor) Executable() string                 { return o.execPath }
func (o *OpencodeDescriptor) IdentityContract() IdentityContract { return IdentityRetrieved }
func (o *OpencodeDescriptor) IdentityCaptureMechanisms() []IdentityCaptureMechanism {
	return []IdentityCaptureMechanism{IdentityCaptureStructuredNativeHook}
}

// IdentityHook reports the plugin callback that yields the native session id.
//
// opencode's hook mechanism differs from codex/kimi: it is a plugin FILE, not a
// config entry inside a provider-owned hooks file. The plugin subscribes to the
// "session.created" event on opencode's plugin event bus; the event payload
// carries the native id at properties.info.id, which the plugin extracts and
// POSTs to the capture endpoint named by the launch environment. Provisioning
// installs the plugin byte-for-byte: a partial match would accept a disabled
// twin.
//
// MEASURED: no native trust modal — RequiresNativeTrust is false. MEASURED
// timing: lazy session creation, no id at launch — Timing is
// IdentityHookAfterFirstSubmission. MEASURED: resume emits no session.created.
func (o *OpencodeDescriptor) IdentityHook() IdentityHook {
	return IdentityHook{
		Event:               "session.created",
		SessionIDField:      "session_id",
		ConfigPath:          o.hookPath,
		Timing:              IdentityHookAfterFirstSubmission,
		RequiresNativeTrust: false,
	}
}

// Availability reports nil: opencode reports its exact native session ID
// through the plugin hook.
func (o *OpencodeDescriptor) Availability() error { return nil }

// NewSessionArgs launches a new interactive session.
//
// opencode allocates its own id lazily, so supplying one is an error rather than
// being silently dropped: a caller that believed it assigned an id would record
// the wrong identity and never be able to resume.
func (o *OpencodeDescriptor) NewSessionArgs(spec LaunchSpec) ([]string, error) {
	if spec.SessionID != "" {
		return nil, fmt.Errorf(
			"provider: opencode allocates its own session id and cannot be assigned "+
				"%q; capture the id from the %s plugin hook instead",
			spec.SessionID, o.IdentityHook().Event)
	}
	argv := []string{o.execPath}
	if spec.SkipPermissions {
		// MEASURED against opencode 1.18.4: --auto is a TOP-LEVEL option
		// ("auto-approve permissions that are not explicitly denied
		// (dangerous!)"), so it applies to the default `opencode
		// [project]` command this launches. It is slightly weaker than
		// claude's total bypass — an explicit deny in opencode.json still
		// holds — which is the honest behavior to describe, not a reason
		// to withhold the flag.
		//
		// opencode prints --help to STDERR, so a probe that discards
		// stderr sees no flags at all and reads as "this CLI has none".
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
//
// MEASURED: -s takes the native id as a value. -c/--continue is recency-based
// and must never appear. --pure disables external plugins and silently
// kills identity capture; it must never appear in any argv.
func (o *OpencodeDescriptor) ResumeSessionArgs(spec ResumeSpec) ([]string, error) {
	id := strings.TrimSpace(spec.NativeSessionID)
	if id == "" {
		return nil, fmt.Errorf("provider: opencode resume requires a native session id")
	}
	argv := []string{o.execPath, "-s", id}
	if spec.SkipPermissions {
		argv = append(argv, "--auto")
	}
	return argv, nil
}

func (o *OpencodeDescriptor) Environment() []string { return nil }

// PermittedEnvironment is an allowlist. opencode honors XDG_CONFIG_HOME and
// friends (measured via `opencode debug paths`), so exposing them lets the
// user isolate config without touching their own ~/.config/opencode.
func (o *OpencodeDescriptor) PermittedEnvironment() []string {
	return []string{
		"HOME", "PATH", "TERM", "LANG", "LC_ALL",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
	}
}

// RequiredConfigPaths names what a deployment must persist. ~/.config/opencode
// holds the plugin; ~/.local/share/opencode holds auth and session state.
func (o *OpencodeDescriptor) RequiredConfigPaths() []string {
	return []string{"~/.config/opencode", "~/.local/share/opencode"}
}

// ExecutableIdentity: opencode is a compiled Bun ELF at an unversioned path.
// /proc/exe IS usable evidence (Interpreted: false) — unlike the node-script
// providers, the kernel executable is the provider binary itself.
//
//	Measured: launch path      /usr/local/bin/opencode (symlink)
//	          /proc/exe        /usr/local/lib/node_modules/opencode-ai/bin/opencode.exe
//	          /proc/cmdline    /usr/local/bin/opencode (the symlink)
//
// Caveat: the path is unversioned (an upgrade replaces the same file) and every
// opencode process on the host shares it — real evidence, but still only one
// member of the identity tuple.
func (o *OpencodeDescriptor) ExecutableIdentity() ExecutableIdentity {
	return ExecutableIdentity{
		Interpreted:     false,
		LaunchTarget:    "/usr/local/lib/node_modules/opencode-ai/bin/opencode.exe",
		ObservedCmdline: "/usr/local/bin/opencode",
	}
}

// Injection returns the contract measured against the real 1.18.4 TUI in a
// private tmux namespace.
//
// Measured:
//   - bracketed paste delivers; pasted newline buffers in composer until
//     separate Enter;
//   - active-turn input is queued with a visible marker and processed
//     exactly once;
//   - input while the ctrl+p palette is open goes to its filter and never
//     the composer (modal discard);
//   - interrupt: two quick Escapes (one arms, arming lapses);
//   - exit: SIGTERM handled → exit status 0 (not signal 15).
func (o *OpencodeDescriptor) Injection() InjectionContract {
	return InjectionContract{
		UseBracketedPaste:        true,
		SubmitKey:                "Enter",
		SupportsMultiline:        true,
		AcceptsInputDuringTurn:   ActiveTurnQueued,
		DiscardsInputDuringModal: true,
		Verified:                 true,
	}
}

func (o *OpencodeDescriptor) GracefulStopSignal() syscall.Signal { return syscall.SIGTERM }
func (o *OpencodeDescriptor) ForceStopSignal() syscall.Signal    { return syscall.SIGKILL }
