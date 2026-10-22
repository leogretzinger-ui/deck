package provider

import (
	"strings"
	"testing"
)

func mustOpencode(t *testing.T) *OpencodeDescriptor {
	t.Helper()
	d, err := NewOpencode("/usr/local/bin/opencode",
		"/home/claude/.config/opencode/plugins/noisedeck-provider-hook.js")
	if err != nil {
		t.Fatalf("NewOpencode: %v", err)
	}
	return d
}

func TestNewOpencodeRequiresAbsolutePaths(t *testing.T) {
	if _, err := NewOpencode("opencode", "/abs/plugin.js"); err == nil {
		t.Fatal("NewOpencode with a relative exec path: want error, got nil")
	}
	if _, err := NewOpencode("/usr/local/bin/opencode", "plugin.js"); err == nil {
		t.Fatal("NewOpencode with a relative plugin path: want error, got nil")
	}
}

// opencode allocates its own id lazily and reports it through a plugin hook.
// A spec carrying a session id is a wiring bug.
func TestOpencodeNewSessionArgsIsBareExecutable(t *testing.T) {
	d := mustOpencode(t)

	argv, err := d.NewSessionArgs(LaunchSpec{WorkDir: "/workspace/p"})
	if err != nil {
		t.Fatalf("NewSessionArgs: %v", err)
	}
	if len(argv) != 1 || argv[0] != "/usr/local/bin/opencode" {
		t.Fatalf("argv = %v, want exactly [/usr/local/bin/opencode]", argv)
	}
}

func TestOpencodeRejectsAnAssignedSessionID(t *testing.T) {
	d := mustOpencode(t)

	if _, err := d.NewSessionArgs(LaunchSpec{WorkDir: "/p", SessionID: testUUID}); err == nil {
		t.Fatal("NewSessionArgs with a session id: want error, got nil")
	}
}

// MEASURED: -s takes the native id. -c/--continue is recency-based and must
// never appear. --pure disables external plugins and silently kills identity
// capture — must never appear in any argv.
func TestOpencodeResumeTargetsAnExactSession(t *testing.T) {
	d := mustOpencode(t)
	const id = "ses_032d1af39ffejke3TwnEL00DxZ"

	argv, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: id})
	if err != nil {
		t.Fatalf("ResumeSessionArgs: %v", err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-s ") || !strings.Contains(joined, id) {
		t.Errorf("resume argv does not name the exact session: %v", argv)
	}
	for _, arg := range argv {
		if arg == "--continue" || arg == "-c" {
			t.Error("resume argv contains --continue/-c, which is not exact identity")
		}
		if arg == "--pure" {
			t.Error("resume argv contains --pure, which silently disables identity capture")
		}
	}

	for _, id := range []string{"", "   "} {
		if _, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: id}); err == nil {
			t.Errorf("ResumeSessionArgs accepted empty id %q", id)
		}
	}
	// Any other recorded value is the CLI's to judge: replay it verbatim
	// rather than stranding a session whose shape we do not recognize.
	for _, id := range []string{"-s", "garbage", "ses_short",
		"ses_032d1af39ffejke3TwnEL00Dx-"} {
		argv, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: id})
		if err != nil {
			t.Errorf("ResumeSessionArgs refused recorded id %q: %v", id, err)
			continue
		}
		if !containsArg(argv, id) {
			t.Errorf("resume argv = %v, want the recorded id %q verbatim", argv, id)
		}
	}
}

func TestOpencodeIdentityContractIsRetrieved(t *testing.T) {
	d := mustOpencode(t)

	if d.IdentityContract() != IdentityRetrieved {
		t.Fatalf("IdentityContract = %v, want IdentityRetrieved", d.IdentityContract())
	}
	if err := d.Availability(); err != nil {
		t.Errorf("Availability = %v, want nil", err)
	}
}

// MEASURED opencode hook facts: event is "session.created" on the plugin
// event bus, timing is after-first-submission (lazy session creation), no
// native trust modal.
func TestOpencodeHookRecordsMeasuredFacts(t *testing.T) {
	hook := mustOpencode(t).IdentityHook()

	if hook.Event != "session.created" {
		t.Errorf("hook event = %q, want session.created", hook.Event)
	}
	if hook.SessionIDField != "session_id" {
		t.Errorf("session id field = %q, want session_id", hook.SessionIDField)
	}
	if hook.ConfigPath != "/home/claude/.config/opencode/plugins/noisedeck-provider-hook.js" {
		t.Errorf("config path = %q, want the plugin file path", hook.ConfigPath)
	}
	// No receiver binary: the plugin POSTs directly to the capture URL in
	// the launch environment (spike-proven), so ReceiverPath stays empty.
	if hook.ReceiverPath != "" {
		t.Errorf("receiver path = %q, want empty — the plugin POSTs directly", hook.ReceiverPath)
	}
	if hook.Timing != IdentityHookAfterFirstSubmission {
		t.Errorf("timing = %v, want after-first-submission (measured: lazy session creation)", hook.Timing)
	}
	if hook.RequiresNativeTrust {
		t.Error("opencode has no native trust modal for plugins (measured)")
	}
}

func TestOpencodeInjectionContractRecordsMeasurements(t *testing.T) {
	d := mustOpencode(t)

	inj := d.Injection()
	if !inj.Verified {
		t.Error("opencode injection contract must be measured, not declared")
	}
	if !inj.UseBracketedPaste || inj.SubmitKey != "Enter" {
		t.Errorf("injection = %+v, want bracketed paste + Enter", inj)
	}
	if !inj.SupportsMultiline {
		t.Error("opencode supports multiline paste (measured)")
	}
	if inj.AcceptsInputDuringTurn != ActiveTurnQueued {
		t.Errorf("during-turn behaviour = %v, want queued (measured: visible QUEUED marker)", inj.AcceptsInputDuringTurn)
	}
	if !inj.DiscardsInputDuringModal {
		t.Error("ctrl+p palette discards input aimed at the composer (measured)")
	}
}

func TestOpencodeEnvironmentDoesNotCarrySecrets(t *testing.T) {
	d := mustOpencode(t)
	env := d.Environment()
	if len(env) != 0 {
		t.Errorf("Environment() = %v, want empty", env)
	}
}

func TestOpencodePermittedEnvironmentIncludesXDGVars(t *testing.T) {
	d := mustOpencode(t)
	permitted := d.PermittedEnvironment()
	hasXDG := make(map[string]bool)
	for _, name := range permitted {
		if strings.HasPrefix(name, "XDG_") {
			hasXDG[name] = true
		}
	}
	for _, want := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		if !hasXDG[want] {
			t.Errorf("permitted environment missing %q (opencode honors XDG dirs)", want)
		}
	}
}

func TestOpencodeRequiredConfigPaths(t *testing.T) {
	d := mustOpencode(t)
	paths := d.RequiredConfigPaths()

	foundConfig := false
	foundData := false
	for _, p := range paths {
		if p == "~/.config/opencode" {
			foundConfig = true
		}
		if p == "~/.local/share/opencode" {
			foundData = true
		}
	}
	if !foundConfig {
		t.Error("RequiredConfigPaths missing ~/.config/opencode (plugins)")
	}
	if !foundData {
		t.Error("RequiredConfigPaths missing ~/.local/share/opencode (auth + sessions)")
	}
}

// opencode is a compiled Bun ELF — /proc/exe IS usable evidence, unlike the
// node-script providers.
func TestOpencodeExecutableIdentityIsNotInterpreted(t *testing.T) {
	id := mustOpencode(t).ExecutableIdentity()

	if id.Interpreted {
		t.Error("opencode is a compiled Bun ELF, not interpreted")
	}
	if !id.Sufficient() {
		t.Error("/proc/exe matching should be sufficient for a native binary")
	}
	if id.LaunchTarget == "" {
		t.Error("LaunchTarget not recorded")
	}
	if id.ObservedCmdline == "" {
		t.Error("ObservedCmdline not recorded")
	}
}
