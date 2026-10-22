package harnessbump

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexIsolationFlagRequiredInBothHelpCommands(t *testing.T) {
	spec, err := LoadProbe(filepath.Join("..", ".."), "codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"--help", "resume --help"} {
		for _, replacement := range []string{"", "--no-daemons"} {
			t.Run(missing+replacement, func(t *testing.T) {
				_, err := Probe(context.Background(), spec, "0.162.1", func(_ context.Context, args []string, _ string) (string, error) {
					flag := "--no-daemon"
					if strings.Join(args, " ") == missing {
						flag = replacement
					}
					return "Usage: codex resume [OPTIONS] [SESSION_ID]\n  --dangerously-bypass-approvals-and-sandbox\n  --no-alt-screen\n  " + flag + "\n", nil
				})
				if err == nil {
					t.Fatal("missing exact no-daemon flag passed")
				}
			})
		}
	}
}
