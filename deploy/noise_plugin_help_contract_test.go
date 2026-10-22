package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pluginHelpContractProbes are the §5.3 help-contract probes every noise
// plugin must satisfy: each flag prints help on stdout and exits 0 from any
// cwd, and version prints a version string on stdout with exit 0.
var pluginHelpContractProbes = []string{"--help", "help", "-h", "version"}

// knownPluginHelpContractGaps names the plugins that do not yet satisfy the
// §5.3 help contract, with the exact probes they fail today. This is an
// explicit, loud allowlist, not a skip: every probe not listed here is
// hard-required, and a listed probe that starts passing fails the test so
// the entry is removed. Remove each plugin's entry when its help lands
// (sdlc v0.1.2 still lacks the contracted `version` subcommand — it ships
// only `--version`; the tasks `help` verb still needs its help; visual-diff
// v0.7.1 landed `--help` and `help` but `-h` still exits 2 with empty
// stdout).
var knownPluginHelpContractGaps = map[string][]string{
	"sdlc":        {"version"},
	"tasks":       {"help"},
	"visual-diff": {"-h"},
}

func TestNoisePluginHelpContract(t *testing.T) {
	pluginRoot := os.Getenv("NOISE_DEPLOY_PLUGIN_ROOT")
	if pluginRoot == "" {
		pluginRoot = "/usr/local/libexec/noise"
	}
	entries, err := os.ReadDir(pluginRoot)
	if err != nil {
		t.Skipf("noise plugin root %s absent (non-image host): %v", pluginRoot, err)
	}
	var plugins []string
	for _, entry := range entries {
		if name, ok := strings.CutPrefix(entry.Name(), "noise-"); ok {
			plugins = append(plugins, name)
		}
	}
	if len(plugins) == 0 {
		t.Skipf("no noise plugins installed under %s (non-image host)", pluginRoot)
	}

	for _, plugin := range plugins {
		t.Run(plugin, func(t *testing.T) {
			bin := filepath.Join(pluginRoot, "noise-"+plugin)
			gaps := map[string]bool{}
			for _, probe := range knownPluginHelpContractGaps[plugin] {
				gaps[probe] = true
			}
			cwd := t.TempDir()
			for _, probe := range pluginHelpContractProbes {
				cmd := exec.Command(bin, probe)
				cmd.Dir = cwd
				stdout, err := cmd.Output()
				conforms := err == nil && len(strings.TrimSpace(string(stdout))) > 0
				if gaps[probe] {
					if conforms {
						t.Errorf("probe %q now passes for noise-%s: remove it from knownPluginHelpContractGaps", probe, plugin)
					}
					continue
				}
				if !conforms {
					t.Errorf("noise-%s %s: help contract requires exit 0 with help on stdout from any cwd, got err=%v stdout=%q", plugin, probe, err, stdout)
				}
			}
		})
	}
}

func TestDockerfileProbesPluginHelpContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, required := range []string{
		`test -n "$(noise tasks --help)"`,
		`test -n "$(noise tasks -h)"`,
		`test -n "$(noise deck --help)"`,
		`test -n "$(noise deck help)"`,
		`test -n "$(noise deck -h)"`,
		`test -n "$(noise sdlc --help)"`,
		`test -n "$(noise sdlc help)"`,
		`test -n "$(noise sdlc -h)"`,
		`test -n "$(noise visual-diff --help)"`,
		`test -n "$(noise visual-diff help)"`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("Dockerfile is missing plugin help-contract probe %q", required)
		}
	}
}
