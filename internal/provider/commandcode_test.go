package provider

import (
	"reflect"
	"testing"
)

const commandCodeID = "123e4567-e89b-42d3-a456-426614174000"

func commandCodeDescriptor(t *testing.T) *CommandCodeDescriptor {
	t.Helper()
	d, err := NewCommandCode("/usr/local/bin/command-code", "/home/agent/.commandcode/settings.json", HookReceiverPath)
	if err != nil {
		t.Fatalf("NewCommandCode: %v", err)
	}
	return d
}

func TestCommandCodeDescriptorLaunchAndResume(t *testing.T) {
	d := commandCodeDescriptor(t)
	if d.Name() != CommandCode || d.IdentityContract() != IdentityRetrieved {
		t.Fatalf("descriptor identity = %s/%s", d.Name(), d.IdentityContract())
	}
	got, err := d.NewSessionArgs(LaunchSpec{SkipPermissions: true})
	if err != nil {
		t.Fatalf("NewSessionArgs: %v", err)
	}
	want := []string{"/usr/local/bin/command-code", "--no-auto-update", "--skip-onboarding", "--yolo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("launch argv = %#v, want %#v", got, want)
	}
	got, err = d.ResumeSessionArgs(ResumeSpec{NativeSessionID: commandCodeID, SkipPermissions: true})
	if err != nil {
		t.Fatalf("ResumeSessionArgs: %v", err)
	}
	want = []string{"/usr/local/bin/command-code", "--no-auto-update", "--skip-onboarding", "--session", commandCodeID, "--yolo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resume argv = %#v, want %#v", got, want)
	}
}

func TestCommandCodeDescriptorRefusesAssignedIDAndReplaysNativeID(t *testing.T) {
	d := commandCodeDescriptor(t)
	if _, err := d.NewSessionArgs(LaunchSpec{SessionID: commandCodeID}); err == nil {
		t.Fatal("assigned id accepted")
	}
	for _, id := range []string{"", "   "} {
		if _, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: id}); err == nil {
			t.Fatalf("ResumeSessionArgs accepted empty id %q", id)
		}
	}
	// Any other recorded value is the CLI's to judge: replay it verbatim
	// rather than stranding a session whose shape we do not recognize.
	for _, id := range []string{"123e4567", "-continue", "garbage",
		"123e4567-e89b-12d3-a456-42661417400z"} {
		argv, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: id})
		if err != nil {
			t.Fatalf("ResumeSessionArgs refused recorded id %q: %v", id, err)
		}
		if !containsArg(argv, id) {
			t.Fatalf("resume argv = %v, want the recorded id %q verbatim", argv, id)
		}
	}
}

func TestCommandCodeDescriptorContract(t *testing.T) {
	d := commandCodeDescriptor(t)
	hook := d.IdentityHook()
	if hook.Event != "SessionStart" || hook.SessionIDField != "session_id" || hook.Timing != IdentityHookAtProcessLaunch || hook.RequiresNativeTrust {
		t.Fatalf("hook = %+v", hook)
	}
	if d.Injection().Verified {
		t.Fatal("unauthenticated integration must not claim the input contract is verified")
	}
	if got := d.RequiredConfigPaths(); !reflect.DeepEqual(got, []string{"~/.commandcode", "/home/agent/.commandcode/settings.json"}) {
		t.Fatalf("config paths = %#v", got)
	}
}
