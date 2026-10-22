package deploy

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBrowserProbeLanesRetainAllAcceptance(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			If       string
			Strategy struct {
				FailFast bool `yaml:"fail-fast"`
				Matrix   struct{ Shard string }
			}
			Steps []struct {
				Name string
				If   string
				Run  string
				Uses string
				Env  map[string]string
				With map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(readWorkflow(t, "ci-fast.yaml"), &workflow); err != nil {
		t.Fatal(err)
	}
	job := workflow.Jobs["probes"]
	if job.Strategy.FailFast || job.Strategy.Matrix.Shard != `${{ fromJSON(needs.scope.outputs.probe_shards || '[1,2,3,"gateway","previews"]') }}` {
		t.Fatal("browser acceptance must use scoped lanes with a full fallback")
	}
	if job.If != "(github.event_name != 'pull_request' || github.event.pull_request.head.repo.full_name == github.repository) && contains(fromJSON(needs.scope.outputs.jobs || '[]'), 'probes')" {
		t.Fatal("browser lane trust boundary changed")
	}
	want := map[string]struct{ condition, command string }{
		"Run the browser probes":                                     {"matrix.shard == 1 || matrix.shard == 2 || matrix.shard == 3", "node tools/run-probes.mjs"},
		"Test workspace browser cryptography against the Go gateway": {"matrix.shard == 'gateway'", "node web/tools/security-go-probe.mjs"},
		"Test plain workspace onboarding against the Go gateway":     {"matrix.shard == 'gateway'", "node web/tools/onboarding-go-probe.mjs"},
		"Test private project previews through HTTPS ingress":        {"matrix.shard == 'previews'", "tmux -V\npnpm test:project-preview\npnpm test:project-preview:managed"},
	}
	seen := map[string]int{}
	goSetup, goCache, chromium := false, false, false
	for _, step := range job.Steps {
		if expected, ok := want[step.Name]; ok {
			seen[step.Name]++
			if step.If != expected.condition || strings.TrimSpace(step.Run) != expected.command {
				t.Errorf("%s changed admission or assertions", step.Name)
			}
			if step.Name == "Run the browser probes" && step.Env["NOISEDECK_PROBE_SHARD"] != "${{ matrix.shard }}/3" {
				t.Fatal("UI shard coverage changed")
			}
		}
		if step.Name == "Install pinned Go for backend browser probes" {
			goSetup = step.If == "matrix.shard == 'gateway' || matrix.shard == 'previews'" && step.With["go-version-file"] == "go.mod"
		}
		if step.Name == "Restore backend browser Go build and module caches" {
			goCache = step.If == "runner.environment != 'self-hosted' && (matrix.shard == 'gateway' || matrix.shard == 'previews')" && strings.HasPrefix(step.Uses, "actions/cache@") && strings.TrimSpace(step.With["path"]) == "~/.cache/go-build\n~/go/pkg/mod" && strings.Contains(step.With["key"], "hashFiles('go.sum')") && strings.Contains(step.With["key"], "github.sha") && strings.Contains(step.With["key"], "matrix.shard")
		}
		if step.Name == "Install Playwright Chromium" {
			chromium = step.If == "" && strings.Contains(step.Run, "playwright install --with-deps chromium")
		}
	}
	for name := range want {
		if seen[name] != 1 {
			t.Errorf("%s appears %d times, want once", name, seen[name])
		}
	}
	if !goSetup || !goCache || !chromium {
		t.Fatal("all backend lanes require pinned Go, build caches and real Chromium")
	}
	for _, probe := range []string{"security-go-probe.mjs", "onboarding-go-probe.mjs"} {
		body, err := os.ReadFile("../web/tools/" + probe)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"-count=1"`) {
			t.Errorf("%s must execute tests rather than reuse cached results", probe)
		}
	}
}
