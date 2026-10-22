package provider

import (
	"reflect"
	"strings"
	"testing"
)

func mustOmp(t *testing.T) *OmpDescriptor {
	t.Helper()
	d, err := NewOmp("/usr/local/bin/omp", "/home/claude/.omp/noisedeck-provider-hook.ts")
	if err != nil {
		t.Fatalf("NewOmp: %v", err)
	}
	return d
}

func TestNewOmpRequiresAbsolutePaths(t *testing.T) {
	if _, err := NewOmp("omp", "/abs/hook.ts"); err == nil {
		t.Fatal("NewOmp with a relative exec path: want error, got nil")
	}
	if _, err := NewOmp("/usr/local/bin/omp", "hook.ts"); err == nil {
		t.Fatal("NewOmp with a relative hook path: want error, got nil")
	}
}

// omp allocates its own UUIDv7 and reports it through a session_start hook
// injected via --hook. The launch argv carries the capture hook.
func TestOmpNewSessionArgsInjectsCaptureHook(t *testing.T) {
	d := mustOmp(t)
	argv, err := d.NewSessionArgs(LaunchSpec{WorkDir: "/workspace/p"})
	if err != nil {
		t.Fatalf("NewSessionArgs: %v", err)
	}
	want := []string{
		"/usr/local/bin/omp",
		"--hook", "/home/claude/.omp/noisedeck-provider-hook.ts",
	}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("new argv = %v, want %v", argv, want)
	}
}

func TestOmpRejectsAnAssignedSessionID(t *testing.T) {
	if _, err := mustOmp(t).NewSessionArgs(LaunchSpec{WorkDir: "/p", SessionID: testUUID}); err == nil {
		t.Fatal("NewSessionArgs with a session id: want error, got nil")
	}
}

// MEASURED: --resume <full-uuid> resolves exactly. A short prefix resolves
// as a case-insensitive prefix and returns the FIRST match with no
// ambiguity failure, so Deck must replay the full uuid. --continue/-c is
// recency; the hidden --session alias shares the prefix resolver; --fork
// branches — all forbidden. The capture hook rides along on resume
// (MEASURED: session_start delivers the resumed id; --hook never leaks
// into stored session state), so the resumed process recaptures its id
// through the startup callback.
func TestOmpResumeTargetsExactFullUUID(t *testing.T) {
	d := mustOmp(t)
	const id = "019fe8dc-09c7-7000-80b9-25ea68620681"

	argv, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: id})
	if err != nil {
		t.Fatalf("ResumeSessionArgs: %v", err)
	}
	want := []string{"/usr/local/bin/omp", "--resume", id,
		"--hook", "/home/claude/.omp/noisedeck-provider-hook.ts"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("resume argv = %v, want %v", argv, want)
	}
	for _, forbidden := range []string{"--continue", "-c", "--session", "--fork"} {
		if containsArg(argv, forbidden) {
			t.Fatalf("resume argv contains ambiguous %s: %v", forbidden, argv)
		}
	}
}

// The resume flight's capture is startup-shaped: omp's session_start fires
// on a resumed process and posts the same source a fresh launch posts, so
// the descriptor declares exactly that, byte-for-byte.
func TestOmpResumeCaptureIsStartupShaped(t *testing.T) {
	capturer, ok := Descriptor(mustOmp(t)).(StartupResumeCaptureDescriptor)
	if !ok {
		t.Fatal("omp does not declare a resume capture source")
	}
	if got := capturer.ResumeCaptureHookSource(); got != "startup" {
		t.Fatalf("resume capture source = %q, want the startup source a resumed session_start posts", got)
	}
	if _, attesting := Descriptor(mustOmp(t)).(ResumeIdentityAttestationDescriptor); attesting {
		t.Fatal("omp declares a resume-shaped attestation the pinned hook never emits")
	}
}

func TestOmpResumeReplaysRecordedIDVerbatim(t *testing.T) {
	d := mustOmp(t)
	for _, id := range []string{"", "   "} {
		if _, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: id}); err == nil {
			t.Errorf("ResumeSessionArgs accepted empty id %q", id)
		}
	}
	// Prefixes, flag-like values, and non-uuidv7 strings are all the CLI's
	// to judge; a recording Deck would not mint itself is replayed verbatim
	// rather than refused.
	for _, id := range []string{
		"019fe8dc", // short prefix
		"-r",       // flag-like
		"not-a-uuid",
		"019fe8dc-09c7-7000-80b9-25ea6862068",  // 35 chars, one short
		"019fe8dc-09c7-4000-80b9-25ea68620681", // UUIDv4, not omp's UUIDv7
		"019fe8dc-09c7-7000-70b9-25ea68620681", // non-RFC variant
		"019fe8dc-09c7-7000-80b9-25ea6862068z", // non-hex
	} {
		argv, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: id})
		if err != nil {
			t.Errorf("ResumeSessionArgs refused recorded id %q: %v", id, err)
			continue
		}
		want := []string{"/usr/local/bin/omp", "--resume", id,
			"--hook", "/home/claude/.omp/noisedeck-provider-hook.ts"}
		if !reflect.DeepEqual(argv, want) {
			t.Errorf("resume argv = %v, want %v", argv, want)
		}
	}
}

func containsArg(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

func TestOmpSkipPermissionsAddsAutoApprove(t *testing.T) {
	d := mustOmp(t)
	newArgv, err := d.NewSessionArgs(LaunchSpec{SkipPermissions: true})
	if err != nil {
		t.Fatal(err)
	}
	resumeArgv, err := d.ResumeSessionArgs(ResumeSpec{
		NativeSessionID: "019fe8dc-09c7-7000-80b9-25ea68620681", SkipPermissions: true})
	if err != nil {
		t.Fatal(err)
	}
	for name, argv := range map[string][]string{"new": newArgv, "resume": resumeArgv} {
		if !containsArg(argv, "--auto-approve") {
			t.Fatalf("%s argv = %v, want --auto-approve", name, argv)
		}
	}
}

func TestOmpIdentityContractIsRetrieved(t *testing.T) {
	d := mustOmp(t)
	if d.IdentityContract() != IdentityRetrieved {
		t.Fatalf("IdentityContract = %v, want IdentityRetrieved", d.IdentityContract())
	}
	if err := d.Availability(); err != nil {
		t.Errorf("Availability = %v, want nil", err)
	}
}

// MEASURED omp hook facts: event is "session_start" on omp's event bus, the
// id is read from ctx.sessionManager.getSessionId() and delivered under
// session_id, timing is eager at launch (IdentityHookAtProcessLaunch — the
// opposite of codex/kimi/opencode's after-first-submission), no native
// trust modal for --hook-loaded hooks, and delivery happens in-process.
func TestOmpHookRecordsMeasuredFacts(t *testing.T) {
	hook := mustOmp(t).IdentityHook()

	if hook.Event != "session_start" {
		t.Errorf("hook event = %q, want session_start", hook.Event)
	}
	if hook.SessionIDField != "session_id" {
		t.Errorf("session id field = %q, want session_id", hook.SessionIDField)
	}
	if hook.ConfigPath != "/home/claude/.omp/noisedeck-provider-hook.ts" {
		t.Errorf("config path = %q, want the capture hook file path", hook.ConfigPath)
	}
	if hook.ReceiverPath != "" {
		t.Errorf("receiver path = %q, want empty for in-process delivery", hook.ReceiverPath)
	}
	if hook.Timing != IdentityHookAtProcessLaunch {
		t.Errorf("timing = %v, want at-process-launch (measured: id is eager, before first submission)", hook.Timing)
	}
	if hook.RequiresNativeTrust {
		t.Error("omp has no native trust modal for --hook-loaded hooks (measured)")
	}

	// The capture hook file must be required config: losing it breaks
	// identity capture for every new session.
	var listed bool
	for _, p := range mustOmp(t).RequiredConfigPaths() {
		if p == hook.ConfigPath {
			listed = true
		}
	}
	if !listed {
		t.Errorf("capture hook %q is not in RequiredConfigPaths()", hook.ConfigPath)
	}
}

func TestOmpEnvironmentAndIdentityAreBounded(t *testing.T) {
	d := mustOmp(t)
	if got := d.Environment(); !reflect.DeepEqual(got, []string{
		"PI_SKIP_VERSION_CHECK=1",
		"PI_TELEMETRY=0",
		"NOISEDECK_MEMPALACE_HARNESS=omp",
	}) {
		t.Fatalf("environment = %v", got)
	}
	for _, name := range d.PermittedEnvironment() {
		if strings.Contains(name, "KEY") || strings.Contains(name, "TOKEN") {
			t.Fatalf("credential environment inherited: %s", name)
		}
	}
	ident := d.ExecutableIdentity()
	if !ident.Interpreted {
		t.Fatal("omp runs under the bun interpreter, so it is interpreted")
	}
	if ident.Sufficient() {
		t.Fatal("an interpreted provider's /proc/exe match must not be sufficient for identity")
	}
	if ident.ExpectedProcExe != "/usr/local/bin/bun" {
		t.Errorf("ExpectedProcExe = %q, want the bun interpreter", ident.ExpectedProcExe)
	}
	if ident.LaunchTarget == "" || ident.ObservedCmdline == "" {
		t.Error("LaunchTarget/ObservedCmdline not recorded")
	}
}

// omp shares Pi's TUI lineage, so the injection values mirror Pi's measured
// contract as the expected shape — but the bracket-paste/submit/modal/
// queueing behaviour and the startup-splash input interception were NOT
// measured against the real CLI. Verified is false and the active-turn
// behaviour is unknown until the conformance suite runs.
func TestOmpInjectionContractIsUnverified(t *testing.T) {
	c := mustOmp(t).Injection()
	if c.Verified {
		t.Fatal("omp injection contract must not claim verification before the conformance suite runs")
	}
	if c.AcceptsInputDuringTurn != ActiveTurnUnknown {
		t.Errorf("during-turn behaviour = %v, want unknown (unmeasured)", c.AcceptsInputDuringTurn)
	}
	if !c.UseBracketedPaste || c.SubmitKey != "Enter" || !c.SupportsMultiline {
		t.Errorf("injection = %+v, want the expected-but-unmeasured shape", c)
	}
}

func TestOmpStopSignals(t *testing.T) {
	d := mustOmp(t)
	if d.GracefulStopSignal().String() != "terminated" {
		t.Errorf("GracefulStopSignal = %v, want SIGTERM", d.GracefulStopSignal())
	}
	if d.ForceStopSignal().String() != "killed" {
		t.Errorf("ForceStopSignal = %v, want SIGKILL", d.ForceStopSignal())
	}
}

// A routed session must still be running the model the routing evidence
// described when it starts editing. A user's own session is theirs: their
// prewalk setting governs it, so the flag belongs to the delegated path only.
func TestOmpPrewalkIsDisabledOnlyWhereRoutingChoseTheModel(t *testing.T) {
	desc := mustOmp(t)
	spec := LaunchSpec{WorkDir: "/workspace/p"}

	ordinary, err := desc.NewSessionArgs(spec)
	if err != nil {
		t.Fatalf("NewSessionArgs: %v", err)
	}
	for _, arg := range ordinary {
		if arg == "--no-prewalk" {
			t.Fatalf("an ordinary launch overrode the user's own prewalk setting: %q", ordinary)
		}
	}

	resumed, err := desc.ResumeSessionArgs(ResumeSpec{WorkDir: "/workspace/p", NativeSessionID: "session-1"})
	if err != nil {
		t.Fatalf("ResumeSessionArgs: %v", err)
	}
	for _, arg := range resumed {
		if arg == "--no-prewalk" {
			t.Fatalf("a resume overrode the user's own prewalk setting: %q", resumed)
		}
	}

	delegated, err := desc.NewDelegatedSessionArgs(spec, DelegatedLaunch{
		Model: "zai/glm-5.3", Prompt: "do the thing",
	})
	if err != nil {
		t.Fatalf("NewDelegatedSessionArgs: %v", err)
	}
	var found bool
	for i, arg := range delegated {
		if arg == "--no-prewalk" {
			found = true
			// Before --model, so the two read as one decision, and never last:
			// the prompt is the positional and has to stay there.
			if i > len(delegated)-3 {
				t.Errorf("--no-prewalk displaced the positional prompt: %q", delegated)
			}
		}
	}
	if !found {
		t.Errorf("a routed launch left prewalk free to change its model: %q", delegated)
	}
}

func TestOmpDelegatedSessionArgs(t *testing.T) {
	desc := mustOmp(t)
	argv, err := desc.NewDelegatedSessionArgs(LaunchSpec{WorkDir: "/workspace/p"}, DelegatedLaunch{
		Model: "zai/glm-5.3", Prompt: "do the thing",
	})
	if err != nil {
		t.Fatalf("NewDelegatedSessionArgs: %v", err)
	}
	// The whole argv is pinned, not just the parts this method appends: a
	// delegated launch that lost the capture hook would have no identity to
	// report, and one that gained a non-interactive flag would exit after the
	// prompt instead of staying available for the work. Neither is visible
	// from an assertion that only looks at the model and the prompt.
	want := []string{
		"/usr/local/bin/omp",
		"--hook", "/home/claude/.omp/noisedeck-provider-hook.ts",
		"--no-prewalk",
		"--model", "zai/glm-5.3",
		"do the thing",
	}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("delegated argv:\n got %q\nwant %q", argv, want)
	}

	skipping, err := desc.NewDelegatedSessionArgs(
		LaunchSpec{WorkDir: "/workspace/p", SkipPermissions: true},
		DelegatedLaunch{Model: "zai/glm-5.3", Prompt: "do the thing"},
	)
	if err != nil {
		t.Fatalf("NewDelegatedSessionArgs with SkipPermissions: %v", err)
	}
	// The user's unattended-run setting reaches a delegated receiver the
	// same way it reaches an ordinary launch, and still leaves the prompt last.
	wantSkipping := []string{
		"/usr/local/bin/omp",
		"--hook", "/home/claude/.omp/noisedeck-provider-hook.ts",
		"--auto-approve",
		"--no-prewalk",
		"--model", "zai/glm-5.3",
		"do the thing",
	}
	if !reflect.DeepEqual(skipping, wantSkipping) {
		t.Fatalf("delegated argv with SkipPermissions:\n got %q\nwant %q", skipping, wantSkipping)
	}

	if _, err := desc.NewDelegatedSessionArgs(LaunchSpec{}, DelegatedLaunch{
		Model: "zai/glm-5.3", Prompt: "",
	}); err == nil {
		t.Fatal("an empty delegated prompt must be rejected")
	}
	if _, err := desc.NewDelegatedSessionArgs(LaunchSpec{SessionID: "assigned"}, DelegatedLaunch{
		Model: "zai/glm-5.3", Prompt: "do the thing",
	}); err == nil {
		t.Fatal("omp allocates its own session id; an assigned one must be rejected here too")
	}
}
