package provider

import (
	"reflect"
	"testing"
)

func TestCodexLaunchIsolationCompleteArgv(t *testing.T) {
	d, err := NewCodex("/opt/provider/codex", "/fixture/hooks.json", HookReceiverPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, skip := range []bool{false, true} {
		base := []string{d.Executable(), "--no-daemon", "--no-alt-screen"}
		if skip {
			base = append(base, "--dangerously-bypass-approvals-and-sandbox")
		}
		for _, tc := range []struct {
			name  string
			build func() ([]string, error)
			tail  []string
		}{
			{"new", func() ([]string, error) { return d.NewSessionArgs(LaunchSpec{SkipPermissions: skip}) }, nil},
			{"delegated", func() ([]string, error) {
				return d.NewDelegatedSessionArgs(LaunchSpec{SkipPermissions: skip}, DelegatedLaunch{Model: "gpt-5.6-sol", Prompt: "accept delegated task"})
			}, []string{"--model", "gpt-5.6-sol", "accept delegated task"}},
			{"resume", func() ([]string, error) {
				return d.ResumeSessionArgs(ResumeSpec{SkipPermissions: skip, NativeSessionID: testUUID})
			}, []string{"resume", testUUID}},
		} {
			t.Run(tc.name+map[bool]string{false: "/permissions", true: "/skip-permissions"}[skip], func(t *testing.T) {
				argv, err := tc.build()
				if err != nil {
					t.Fatal(err)
				}
				want := append(append([]string(nil), base...), tc.tail...)
				if !reflect.DeepEqual(argv, want) {
					t.Fatalf("argv=%q want=%q", argv, want)
				}
			})
		}
	}
}

func TestCodexResumeReplaysOpaqueIdentityVerbatim(t *testing.T) {
	d := mustCodex(t)
	for _, id := range []string{testUUID, "opaque:id", "  opaque:id with spaces  "} {
		for _, skip := range []bool{false, true} {
			argv, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: id, SkipPermissions: skip})
			if err != nil {
				t.Fatal(err)
			}
			if len(argv) < 4 || argv[len(argv)-2] != "resume" || argv[len(argv)-1] != id {
				t.Fatalf("resume and exact opaque id not adjacent: %q", argv)
			}
		}
	}
}
