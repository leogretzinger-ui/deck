package provider

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mustPi(t *testing.T) *PiDescriptor {
	t.Helper()
	d, err := NewPi("/usr/local/bin/pi", "/home/claude/.pi/noisedeck-provider-hook.js")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func requireInstalledPi(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/usr/local/bin/pi"); err != nil {
		if os.IsNotExist(err) {
			t.Skip("pinned Pi CLI is not installed outside the runtime image")
		}
		t.Fatalf("stat pinned Pi CLI: %v", err)
	}
}

func TestProviderSessionArgsDoNotOverrideModelOrThinking(t *testing.T) {
	providers := []struct {
		name   string
		new    func() ([]string, error)
		resume func() ([]string, error)
	}{
		{
			name: "omp",
			new: func() ([]string, error) {
				return mustOmp(t).NewSessionArgs(LaunchSpec{})
			},
			resume: func() ([]string, error) {
				return mustOmp(t).ResumeSessionArgs(ResumeSpec{NativeSessionID: testUUID})
			},
		},
		{
			name: "pi",
			new: func() ([]string, error) {
				return mustPi(t).NewSessionArgs(LaunchSpec{})
			},
			resume: func() ([]string, error) {
				return mustPi(t).ResumeSessionArgs(ResumeSpec{NativeSessionID: testUUID})
			},
		},
	}
	for _, provider := range providers {
		t.Run(provider.name, func(t *testing.T) {
			for _, path := range []struct {
				name string
				args func() ([]string, error)
			}{
				{name: "new", args: provider.new},
				{name: "resume", args: provider.resume},
			} {
				args, err := path.args()
				if err != nil {
					t.Fatalf("%s args: %v", path.name, err)
				}
				for _, arg := range args {
					if strings.HasPrefix(arg, "--model") || strings.HasPrefix(arg, "--thinking") {
						t.Fatalf("%s argv = %v, contains provider selector %q", path.name, args, arg)
					}
				}
			}
		})
	}
}

func TestPiRetrievedIdentityArgs(t *testing.T) {
	d := mustPi(t)
	if d.IdentityContract() != IdentityRetrieved {
		t.Fatalf("identity = %s", d.IdentityContract())
	}
	got, err := d.NewSessionArgs(LaunchSpec{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/local/bin/pi",
		"--provider", "zai",
		"--extension", "/home/claude/.pi/noisedeck-provider-hook.js",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("new argv = %v, want %v", got, want)
	}
	if strings.Contains(strings.Join(got, " "), "--session-id") {
		t.Fatalf("new argv = %v; --session-id warns on every genuinely new session", got)
	}
	got, err = d.ResumeSessionArgs(ResumeSpec{NativeSessionID: testUUID})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"/usr/local/bin/pi", "--provider", "zai", "--session", testUUID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resume argv = %v, want %v", got, want)
	}
	for _, forbidden := range []string{"--continue", "--resume", "--fork"} {
		if strings.Contains(strings.Join(got, " "), forbidden) {
			t.Fatalf("resume argv contains ambiguous %s: %v", forbidden, got)
		}
	}
}

// pi allocates its own UUIDv7 and reports it through the session_start
// hook: a caller that believed it assigned an id would record the wrong
// identity and never be able to resume.
func TestPiRefusesAssignedIDAndReplaysNativeID(t *testing.T) {
	d := mustPi(t)
	if _, err := d.NewSessionArgs(LaunchSpec{SessionID: testUUID}); err == nil {
		t.Fatal("assigned id accepted")
	}
	if _, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: ""}); err == nil {
		t.Fatal("empty recorded id accepted")
	}
	argv, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: "-c"})
	if err != nil {
		t.Fatalf("ResumeSessionArgs: %v", err)
	}
	if !containsArg(argv, "-c") {
		t.Fatalf("resume argv = %v, want the recorded id %q verbatim", argv, "-c")
	}
}

func TestPiSkipPermissionsApprovesProjectResources(t *testing.T) {
	d := mustPi(t)
	newArgv, err := d.NewSessionArgs(LaunchSpec{SkipPermissions: true})
	if err != nil {
		t.Fatal(err)
	}
	resumeArgv, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: testUUID, SkipPermissions: true})
	if err != nil {
		t.Fatal(err)
	}
	for name, argv := range map[string][]string{"new": newArgv, "resume": resumeArgv} {
		if argv[len(argv)-1] != "--approve" {
			t.Fatalf("%s argv = %v, want --approve", name, argv)
		}
	}
}

// The identity hook mirrors omp's measured contract: eager at launch, no
// native trust prerequisite, delivered by the byte-identity-pinned file.
func TestPiIdentityHookIsEagerAndUntrusted(t *testing.T) {
	d := mustPi(t)
	hook := d.IdentityHook()
	if hook.Event != "session_start" {
		t.Errorf("hook event = %q, want session_start", hook.Event)
	}
	if hook.SessionIDField != "session_id" {
		t.Errorf("session id field = %q, want session_id", hook.SessionIDField)
	}
	if hook.ConfigPath != "/home/claude/.pi/noisedeck-provider-hook.js" {
		t.Errorf("config path = %q, want the capture hook file path", hook.ConfigPath)
	}
	if hook.Timing != IdentityHookAtProcessLaunch {
		t.Errorf("hook timing = %v, want at-process-launch", hook.Timing)
	}
	if hook.RequiresNativeTrust {
		t.Error("pi hook claims a native trust prerequisite without measurement")
	}
	var listed bool
	for _, p := range d.RequiredConfigPaths() {
		if p == hook.ConfigPath {
			listed = true
		}
	}
	if !listed {
		t.Errorf("hook config %q is not in RequiredConfigPaths()", hook.ConfigPath)
	}
}

func TestPiUnknownExactSessionFailsLoudly(t *testing.T) {
	requireInstalledPi(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/local/bin/pi",
		"--session", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"--print", "synthetic unknown-session probe")
	cmd.Env = []string{
		"HOME=" + t.TempDir(),
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"TERM=xterm-256color",
		"PI_OFFLINE=1",
		"PI_TELEMETRY=0",
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("unknown exact session started successfully: %s", out)
	}
	if ctx.Err() != nil {
		t.Fatalf("unknown exact session did not fail promptly: %v", ctx.Err())
	}
	if !strings.Contains(string(out), "No session found matching") {
		t.Fatalf("unknown exact session failed ambiguously: %s", out)
	}
}

func TestPiEnvironmentAndIdentityAreBounded(t *testing.T) {
	d := mustPi(t)
	if got := d.Environment(); !reflect.DeepEqual(got, []string{
		"PI_SKIP_VERSION_CHECK=1",
		"PI_TELEMETRY=0",
		"NOISEDECK_MEMPALACE_HARNESS=pi",
	}) {
		t.Fatalf("environment = %v", got)
	}
	for _, name := range d.PermittedEnvironment() {
		if strings.Contains(name, "KEY") || strings.Contains(name, "TOKEN") {
			t.Fatalf("credential environment inherited: %s", name)
		}
	}
	ident := d.ExecutableIdentity()
	if !ident.Interpreted || ident.ExpectedProcExe != "/usr/local/bin/node" || ident.ObservedCmdline != "node /usr/local/bin/pi" {
		t.Fatalf("identity = %+v", ident)
	}
}

func TestPiInjectionContract(t *testing.T) {
	c := mustPi(t).Injection()
	if !c.Verified || !c.UseBracketedPaste || c.SubmitKey != "Enter" || !c.SupportsMultiline || c.AcceptsInputDuringTurn != ActiveTurnQueued || !c.DiscardsInputDuringModal {
		t.Fatalf("injection = %+v", c)
	}
}
