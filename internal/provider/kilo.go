package provider

import (
	"fmt"
	"strings"
	"syscall"
)

// The contracts below were measured against Kilo 7.4.21.

// KiloDescriptor launches the Kilo CLI as its real interactive TUI. Kilo
// allocates session ids lazily and exposes them through its documented plugin
// event bus; Deck records that structured callback without reading CLI output.
type KiloDescriptor struct {
	execPath string
	hookPath string
}

func NewKilo(execPath, hookPath string) (*KiloDescriptor, error) {
	if !strings.HasPrefix(execPath, "/") {
		return nil, fmt.Errorf("provider: kilo executable %q must be an absolute path", execPath)
	}
	if !strings.HasPrefix(hookPath, "/") {
		return nil, fmt.Errorf("provider: kilo hook plugin %q must be an absolute path", hookPath)
	}
	return &KiloDescriptor{execPath: execPath, hookPath: hookPath}, nil
}

func (k *KiloDescriptor) Name() Name                         { return Kilo }
func (k *KiloDescriptor) Executable() string                 { return k.execPath }
func (k *KiloDescriptor) IdentityContract() IdentityContract { return IdentityRetrieved }
func (k *KiloDescriptor) IdentityCaptureMechanisms() []IdentityCaptureMechanism {
	return []IdentityCaptureMechanism{IdentityCaptureStructuredNativeHook}
}
func (k *KiloDescriptor) Availability() error { return nil }

func (k *KiloDescriptor) IdentityHook() IdentityHook {
	return IdentityHook{
		Event: "session.created", SessionIDField: "session_id",
		ConfigPath: k.hookPath, Timing: IdentityHookAfterFirstSubmission,
	}
}

func (k *KiloDescriptor) NewSessionArgs(spec LaunchSpec) ([]string, error) {
	if spec.SessionID != "" {
		return nil, fmt.Errorf("provider: kilo allocates its own session id and cannot be assigned %q", spec.SessionID)
	}
	argv := []string{k.execPath}
	if spec.SkipPermissions {
		argv = append(argv, "--auto")
	}
	return argv, nil
}

// ResumeSessionArgs resumes an exact recorded session. The recorded id is
// replayed verbatim: the CLI owns its own id format, and a format change
// upstream must never strand a recorded session. Only an empty id is
// refused — an empty recording is a bookkeeping bug on our side, not a
// provider format change.
func (k *KiloDescriptor) ResumeSessionArgs(spec ResumeSpec) ([]string, error) {
	id := strings.TrimSpace(spec.NativeSessionID)
	if id == "" {
		return nil, fmt.Errorf("provider: kilo resume requires a native session id")
	}
	argv := []string{k.execPath, "--session", id}
	if spec.SkipPermissions {
		argv = append(argv, "--auto")
	}
	return argv, nil
}

func (k *KiloDescriptor) Environment() []string { return nil }
func (k *KiloDescriptor) PermittedEnvironment() []string {
	return []string{"HOME", "PATH", "TERM", "LANG", "LC_ALL", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"}
}
func (k *KiloDescriptor) RequiredConfigPaths() []string {
	return []string{"~/.config/kilo", "~/.local/share/kilo"}
}
func (k *KiloDescriptor) ExecutableIdentity() ExecutableIdentity {
	// The npm bin is a Node launcher that owns the pane and spawns Kilo's
	// platform binary in the same process group. /proc identifies Node, not a
	// sufficient provider executable, so the rest of the identity tuple remains
	// mandatory.
	return ExecutableIdentity{
		Interpreted:     true,
		ExpectedProcExe: "/usr/local/bin/node",
		LaunchTarget:    "/usr/local/lib/node_modules/@kilocode/cli/bin/kilo",
		ObservedCmdline: "node /usr/local/bin/kilo",
	}
}
func (k *KiloDescriptor) Injection() InjectionContract {
	return InjectionContract{UseBracketedPaste: true, SubmitKey: "Enter", SupportsMultiline: true, AcceptsInputDuringTurn: ActiveTurnQueued, DiscardsInputDuringModal: true, Verified: true}
}
func (k *KiloDescriptor) GracefulStopSignal() syscall.Signal { return syscall.SIGTERM }
func (k *KiloDescriptor) ForceStopSignal() syscall.Signal    { return syscall.SIGKILL }
