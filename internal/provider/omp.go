package provider

import (
	"fmt"
	"strings"
	"syscall"
)

// The launch surface, hook event shape, eager firing, UUIDv7 identity,
// exact/prefix resume semantics, bun process identity, and SIGTERM/SIGKILL
// exit shapes were measured against omp 17.2.12. The injection contract and
// the full authenticated resume round-trip were NOT measured (credential
// gap) — see Injection.

// OmpDescriptor implements Descriptor for the Oh My Pi (omp) CLI.
//
// omp is a Pi-derived coding agent that runs under bun. Like Codex, Kimi,
// and opencode it allocates its own session id and reports it through a
// hook rather than accepting one at launch, so its IdentityContract is
// IdentityRetrieved. The hook is a TypeScript event module loaded per
// launch via `--hook <path>` (Deck controls argv); the user's hook
// auto-discovery is unmeasured and must not be relied on. Verified against
// 17.2.12 by loading a capture hook via `--hook` and reading the delivered
// id:
//
//	{"hook_event_name":"SessionStart",
//	 "session_id":"019fe8dc-09c7-7000-80b9-25ea68620681",
//	 "cwd":"/workspace/project","source":"startup"}
//
// The session_start payload carries no id; the capture hook reads it from
// `ctx.sessionManager.getSessionId()`. The id is a UUIDv7 (version nibble
// 7), minted at session construction.
//
// Timing is the measured distinction from Codex/Kimi/opencode: omp mints
// the id EAGERLY and fires session_start at launch — ~2-4s after process
// start, while the startup splash is still on screen, BEFORE any user
// submission. Deck attaches once the process is running and records the
// native ID when the hook delivers it; a slow hook must not delay the terminal.
//
// Resume is exact by full uuid — observed resolving a known id — and loud
// on a miss:
//
//	omp --resume 019fe8dc-09c7-7000-80b9-25ea68620681  -> resumed
//	omp --resume 00000000-0000-7000-8000-000000000000  -> exit 1, not found
//
// The footgun is that `--resume <prefix>` resolves as a case-insensitive
// prefix and returns the FIRST match with no ambiguity failure, so Deck
// must always replay the FULL uuid (or the file path) and never a short
// prefix. `-c/--continue` is recency-based and forbidden for the same
// reason. The hidden `--session` alias maps to the same prefix resolver as
// `--resume` (NOT Pi's exact-uuid semantics), so it must never appear.
type OmpDescriptor struct {
	execPath string
	hookPath string
}

// NewOmp builds an omp descriptor. hookPath is the absolute path to the
// provisioning-owned capture hook that NewSessionArgs injects via `--hook`.
// Provisioning installs it byte-for-byte; Deck never writes it.
func NewOmp(execPath, hookPath string) (*OmpDescriptor, error) {
	if !strings.HasPrefix(execPath, "/") {
		return nil, fmt.Errorf("provider: omp executable %q must be an absolute path", execPath)
	}
	if !strings.HasPrefix(hookPath, "/") {
		return nil, fmt.Errorf("provider: omp capture hook %q must be an absolute path", hookPath)
	}
	return &OmpDescriptor{execPath: execPath, hookPath: hookPath}, nil
}

func (o *OmpDescriptor) Name() Name                         { return Omp }
func (o *OmpDescriptor) Executable() string                 { return o.execPath }
func (o *OmpDescriptor) IdentityContract() IdentityContract { return IdentityRetrieved }
func (o *OmpDescriptor) IdentityCaptureMechanisms() []IdentityCaptureMechanism {
	return []IdentityCaptureMechanism{IdentityCaptureStructuredNativeHook}
}

// IdentityHook reports the callback that yields the native session id.
//
// omp's hook mechanism is a TypeScript event module loaded via `--hook`,
// like opencode's plugin file rather than Codex/Kimi's config entry. The
// hook subscribes to the session_start event, whose payload carries no id;
// it reads `ctx.sessionManager.getSessionId()` and POSTs directly to the
// capture endpoint inherited in the launch environment.
//
// MEASURED: no native trust modal for `--hook`-loaded hooks
// (RequiresNativeTrust is false). MEASURED timing: the id is eager at
// launch — IdentityHookAtProcessLaunch — the opposite of
// Codex/Kimi/opencode's after-first-submission timing. Neither timing
// makes identity delivery a prerequisite for attaching a live terminal.
// ReceiverPath is empty because the byte-identity-pinned hook delivers
// in-process; no payload is staged for an external receiver.
func (o *OmpDescriptor) IdentityHook() IdentityHook {
	return IdentityHook{
		Event:               "session_start",
		SessionIDField:      "session_id",
		ConfigPath:          o.hookPath,
		Timing:              IdentityHookAtProcessLaunch,
		RequiresNativeTrust: false,
	}
}

// Availability reports nil: omp reports its exact native session ID through the
// session_start hook.
func (o *OmpDescriptor) Availability() error { return nil }

// NewSessionArgs launches a new interactive session.
//
// omp allocates its own UUIDv7 and reports it through the hook, so
// supplying one is an error rather than being silently dropped: a caller
// that believed it assigned an id would record the wrong identity and
// never be able to resume. The capture hook is injected every launch via
// `--hook`; Deck controls argv, so it does not depend on omp's unmeasured
// hook auto-discovery.
//
// Deck selects no model or reasoning level: whatever the provider itself
// resolves is authoritative.
func (o *OmpDescriptor) NewSessionArgs(spec LaunchSpec) ([]string, error) {
	if spec.SessionID != "" {
		return nil, fmt.Errorf(
			"provider: omp allocates its own session id and cannot be assigned "+
				"%q; capture the id from the %s hook instead",
			spec.SessionID, o.IdentityHook().Event)
	}
	argv := []string{
		o.execPath,
		"--hook", o.hookPath,
	}
	if spec.SkipPermissions {
		// MEASURED against omp 17.2.12: --auto-approve is "auto-approve all
		// tool calls (skip approval prompts)". --approval-mode=yolo is the
		// finer-grained equivalent; --auto-approve is the honest top-level
		// match for a setting that says permissions are skipped.
		argv = append(argv, "--auto-approve")
	}
	return argv, nil
}

// NewDelegatedSessionArgs uses omp's documented positional MESSAGES form.
//
// MEASURED against omp 17.3.5: a positional message is processed at startup
// and the session stays interactive, which is what a delegated launch needs;
// `-p` would process the same prompt and exit instead.
//
// This names a model where the ordinary and resume paths deliberately do not.
// Those paths must not override a selection the user already made, but a
// delegated session has no prior selection: the caller names provider and
// model together as one routing decision, so passing it through is honoring
// that choice rather than overriding one.
//
// --no-prewalk keeps that decision. A configured prewalk switches off the
// active model once the plan's todo list exists and the session first
// edits — to the "smol" role, which may be another subscription — so the
// routed model is not necessarily the one still running when work begins.
// prewalk.enabled is the user's setting, which Deck does not own, and
// --no-prewalk disables it even when set. Only the delegated path carries
// it: a user's own session should follow their own config.
func (o *OmpDescriptor) NewDelegatedSessionArgs(spec LaunchSpec, delegated DelegatedLaunch) ([]string, error) {
	if err := validateDelegatedLaunch(delegated); err != nil {
		return nil, err
	}
	argv, err := o.NewSessionArgs(spec)
	if err != nil {
		return nil, err
	}
	return append(argv, "--no-prewalk", "--model", delegated.Model, delegated.Prompt), nil
}

// ResumeSessionArgs resumes an exact recorded session.
//
// The recorded id is replayed verbatim: the CLI owns its own id format, and
// a format change upstream must never strand a recorded session. Only an
// empty id is refused — an empty recording is a bookkeeping bug on our
// side, not a provider format change.
//
// MEASURED: `--resume <full-uuid>` resolves exactly. A short prefix
// resolves as a case-insensitive prefix and returns the FIRST match with
// NO ambiguity failure — a silent footgun — so the recorded value is the
// full id the capture hook delivered at launch, replayed whole and never
// shortened or reformatted on this side. `--continue`/`-c` is
// recency-based, the hidden `--session` alias shares the prefix resolver,
// and `--fork` branches: all are forbidden. An unknown id exits 1, which
// is what makes a successful resume meaningful.
//
// MEASURED against omp 18.10.3 (bun /usr/local/bin/omp; scratch cwd, scratch
// tmux socket, and a fabricated throwaway conversation under the scratch
// session dir; no prompts sent, zero API spend):
//
//	omp --resume <id> --hook <capture-hook>  -> session_start fires and
//	  ctx.sessionManager.getSessionId() returns the RESUMED id
//	omp --resume <id> (no --hook)            -> the resumed conversation's
//	  stored header, the session dir, and ~/.omp/agent/config.yml contain
//	  no reference to any hook: --hook is argv-only and never leaks into
//	  stored session state
//
// So the capture hook rides along on resume exactly as on launch: it does
// not rewrite what the resumed header restores, the resumed process
// redelivers its id through the startup callback, and the capture channel
// stays live in the resumed pane.
//
// An in-pane /resume is reported the moment it happens. MEASURED against
// omp 18.12.1: the switch emits session_before_switch (context still the
// OLD id) then session_switch with ctx.sessionManager.getSessionId() already
// the NEW id, in the same process — including a /resume that re-picks the
// conversation already running — and omp discards the abandoned empty
// conversation. The capture hook subscribes to session_switch and reports
// the landed-on id through the same generation-bound channel, so the row
// tracks the conversation the pane is actually running instead of going
// stale until the next launch is refused. A stale id can still reach a
// launch (a switch from before this wiring, or a channel failure): the
// dead id exits 1 ("Session not found"), Deck records the refusal through
// recordRefusedResume, and the conversation itself is never destroyed.
// Neither this path nor an ordinary launch selects a model or reasoning
// level; only a delegated launch does, because there the caller's choice
// is the routing decision itself.
func (o *OmpDescriptor) ResumeSessionArgs(spec ResumeSpec) ([]string, error) {
	id := strings.TrimSpace(spec.NativeSessionID)
	if id == "" {
		return nil, fmt.Errorf("provider: omp resume requires a native session id")
	}
	argv := []string{o.execPath, "--resume", id, "--hook", o.hookPath}
	if spec.SkipPermissions {
		argv = append(argv, "--auto-approve")
	}
	return argv, nil
}

// ResumeCaptureHookSource: MEASURED against omp 18.10.3 — the session_start
// callback a resumed process fires posts the same startup-shaped source a
// fresh launch posts, carrying the RESUMED id. The resume flight therefore
// takes a startup-shaped capture grant, never a resume-shaped one: the
// pinned hook emits no resume source, and inventing one would attest a
// distinction the provider does not make.
func (o *OmpDescriptor) ResumeCaptureHookSource() string { return "startup" }

func (o *OmpDescriptor) Environment() []string {
	return []string{
		"PI_SKIP_VERSION_CHECK=1",
		"PI_TELEMETRY=0",
		"NOISEDECK_MEMPALACE_HARNESS=omp",
	}
}

// PermittedEnvironment is an allowlist. omp (a Pi fork) honors the XDG
// data/state/cache split for its data directories (measured), so exposing
// those lets the user isolate state without touching ~/.omp. Its
// documented environment surface names no XDG_CONFIG_HOME override for
// the ~/.omp config root, so that var is not listed.
func (o *OmpDescriptor) PermittedEnvironment() []string {
	return []string{
		"HOME", "PATH", "TERM", "LANG", "LC_ALL",
		"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
	}
}

// RequiredConfigPaths names what a deployment must persist. ~/.omp is the
// config root: it holds credentials, the agent
// directory, sessions, and the SQLite databases. The capture hook is
// provisioning-owned and verified byte-for-byte before every launch, so
// losing it breaks identity capture for every new session.
func (o *OmpDescriptor) RequiredConfigPaths() []string {
	return []string{"~/.omp", o.hookPath}
}

// ExecutableIdentity: omp is a bun-interpreted script, so nothing in /proc
// identifies it uniquely — the same situation as the node-script providers.
//
//	Measured: launch path      /usr/local/bin/omp (symlink)
//	          /proc/exe        the bun interpreter
//	          /proc/cmdline    bun /usr/local/bin/omp
//
// /proc/exe is the bun runtime (the same value for every bun process on
// the host), so Sufficient is false and identity rests on the rest of the
// identity tuple, as for codex/kimi/pi. The deployment interpreter and
// launch-target paths follow the image's canonical layout (analogous to
// pi's node paths); confirm them when omp is provisioned.
func (o *OmpDescriptor) ExecutableIdentity() ExecutableIdentity {
	return ExecutableIdentity{
		Interpreted:     true,
		ExpectedProcExe: "/usr/local/bin/bun",
		LaunchTarget:    "/usr/local/lib/node_modules/@oh-my-pi/pi-coding-agent/dist/cli.js",
		ObservedCmdline: "bun /usr/local/bin/omp",
	}
}

// Injection returns the contract DECLARED for omp, not yet measured.
//
// omp shares Pi's TUI lineage, so the values mirror Pi's measured contract
// as the expected shape, but Verified is false and the active-turn
// behaviour is unknown: the bracket-paste/submit/modal/queueing behaviour
// and the unprovisioned-home startup splash (which can intercept injected
// input the way codex's /permissions modal does) were not measured against
// the real CLI. No release claim may rest on this until the conformance
// suite runs. Provisioning must complete onboarding and/or disable the
// startup splash, or the pane shows a modal at launch that would intercept
// injected input — a provisioning concern, not something this descriptor
// can enforce.
func (o *OmpDescriptor) Injection() InjectionContract {
	return InjectionContract{
		UseBracketedPaste:      true,
		SubmitKey:              "Enter",
		SupportsMultiline:      true,
		AcceptsInputDuringTurn: ActiveTurnUnknown,
		Verified:               false,
	}
}

func (o *OmpDescriptor) GracefulStopSignal() syscall.Signal { return syscall.SIGTERM }
func (o *OmpDescriptor) ForceStopSignal() syscall.Signal    { return syscall.SIGKILL }
