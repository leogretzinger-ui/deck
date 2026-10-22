package session

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/noisefloor-oss/deck/internal/provider"
)

func TestProviderLaunchPreservesHome(t *testing.T) {
	codex, err := provider.NewCodex("/bin/true", "/fixture/hooks.json", "/fixture/receiver")
	if err != nil {
		t.Fatal(err)
	}
	claude, err := provider.NewClaude("/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{"/Users/Your Home", "/home/agent"} {
		t.Run(home, func(t *testing.T) {
			t.Setenv("HOME", home)
			for _, name := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
				t.Setenv(name, "")
				if err := os.Unsetenv(name); err != nil {
					t.Fatal(err)
				}
			}
			for _, desc := range []provider.Descriptor{codex, claude} {
				t.Run(string(desc.Name()), func(t *testing.T) {
					argv := launchArgv(desc, []string{"/usr/bin/env"}, "xterm-256color", nil)
					out, err := exec.Command(argv[0], argv[1:]...).Output()
					if err != nil {
						t.Fatal(err)
					}
					env := "\n" + string(out)
					if !strings.Contains(env, "\nHOME="+home+"\n") {
						t.Fatalf("provider did not inherit HOME %q: %s", home, out)
					}
					for _, name := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
						if strings.Contains(env, "\n"+name+"=") {
							t.Fatalf("launch added a provider-home override: %s", out)
						}
					}
				})
			}
		})
	}
}
