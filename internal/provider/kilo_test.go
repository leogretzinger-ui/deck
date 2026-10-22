package provider

import (
	"reflect"
	"strings"
	"testing"
)

func mustKilo(t *testing.T) *KiloDescriptor {
	t.Helper()
	d, err := NewKilo("/usr/local/bin/kilo", "/home/agent/.config/kilo/plugin/noisedeck-provider-hook.js")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestKiloRequiresAbsolutePaths(t *testing.T) {
	if _, err := NewKilo("kilo", "/abs/hook.js"); err == nil {
		t.Fatal("relative executable accepted")
	}
	if _, err := NewKilo("/usr/local/bin/kilo", "hook.js"); err == nil {
		t.Fatal("relative hook accepted")
	}
}

func TestKiloLaunchAndExactResumeArgs(t *testing.T) {
	d := mustKilo(t)
	got, err := d.NewSessionArgs(LaunchSpec{SkipPermissions: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"/usr/local/bin/kilo", "--auto"}) {
		t.Fatalf("new argv = %v", got)
	}
	if _, err := d.NewSessionArgs(LaunchSpec{SessionID: testUUID}); err == nil {
		t.Fatal("assigned id accepted")
	}
	got, err = d.ResumeSessionArgs(ResumeSpec{NativeSessionID: "ses_032d1af39ffejke3TwnEL00DxZ", SkipPermissions: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"/usr/local/bin/kilo", "--session", "ses_032d1af39ffejke3TwnEL00DxZ", "--auto"}) {
		t.Fatalf("resume argv = %v", got)
	}
	for _, arg := range got {
		if arg == "--continue" || arg == "-c" {
			t.Fatalf("recency selector in %v", got)
		}
	}
}

func TestKiloResumeReplaysRecordedIDVerbatim(t *testing.T) {
	d := mustKilo(t)
	for _, id := range []string{"", "   "} {
		if _, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: id}); err == nil {
			t.Fatalf("ResumeSessionArgs accepted empty id %q", id)
		}
	}
	// Any other recorded value is the CLI's to judge: replay it verbatim
	// rather than stranding a session whose shape we do not recognize.
	for _, id := range []string{"-session", "garbage", "ses_short",
		"ses_032d1af39ffejke3TwnEL00Dx-"} {
		argv, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: id})
		if err != nil {
			t.Fatalf("ResumeSessionArgs refused recorded id %q: %v", id, err)
		}
		if !containsArg(argv, id) {
			t.Fatalf("resume argv = %v, want the recorded id %q verbatim", argv, id)
		}
	}
}

func TestKiloContractSurface(t *testing.T) {
	d := mustKilo(t)
	if d.IdentityContract() != IdentityRetrieved {
		t.Fatal("Kilo must retrieve native identity")
	}
	if d.IdentityHook().Event != "session.created" || d.IdentityHook().Timing != IdentityHookAfterFirstSubmission {
		t.Fatalf("hook = %+v", d.IdentityHook())
	}
	if !d.Injection().Verified || d.Injection().AcceptsInputDuringTurn != ActiveTurnQueued {
		t.Fatalf("injection = %+v", d.Injection())
	}
	if !strings.Contains(strings.Join(d.RequiredConfigPaths(), " "), ".config/kilo") {
		t.Fatalf("paths = %v", d.RequiredConfigPaths())
	}
}
