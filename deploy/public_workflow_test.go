package deploy

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type publicWorkflow struct {
	Permissions map[string]string `yaml:"permissions"`
	Env         map[string]string `yaml:"env"`
	Events      map[string]any    `yaml:"on"`
	Jobs        map[string]struct {
		If          string            `yaml:"if"`
		Permissions map[string]string `yaml:"permissions"`
		Env         map[string]string `yaml:"env"`
		Steps       []struct {
			Name string            `yaml:"name"`
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			With map[string]any    `yaml:"with"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func readPublicWorkflow(t *testing.T, name string) publicWorkflow {
	t.Helper()
	raw := readWorkflow(t, name)
	assertActionsPinnedBySHA(t, raw)
	var workflow publicWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

func TestPublicWorkflowRepositoryAdmission(t *testing.T) {
	const policy = "github.event.repository.visibility == 'public' && github.event.repository.fork == false && (github.repository == 'noisefloor-oss/deck' || vars.NOISEDECK_PUBLIC_RELEASE_REPOSITORY == github.repository)"
	for _, file := range []string{"public-image.yaml", "release.yml"} {
		workflow := readPublicWorkflow(t, file)
		for event := range workflow.Events {
			if event != "push" && (file != "public-image.yaml" || event != "workflow_dispatch") {
				t.Errorf("%s must not admit untrusted event %s", file, event)
			}
		}
		for name, job := range workflow.Jobs {
			condition := strings.Join(strings.Fields(job.If), " ")
			if condition != policy {
				t.Fatalf("%s/%s admission = %q, want public non-fork repository-bound opt-in", file, name, condition)
			}
			for _, scenario := range []struct {
				name, repository, visibility, optIn string
				fork, want                          bool
			}{
				{"canonical-default", "noisefloor-oss/deck", "public", "", false, true},
				{"canonical-unrelated-variable", "noisefloor-oss/deck", "public", "other/deck", false, true},
				{"canary-opt-in", "canary/deck", "public", "canary/deck", false, true},
				{"case-insensitive-exact-slug", "Canary/Deck", "public", "canary/deck", false, true},
				{"canary-default-off", "canary/deck", "public", "", false, false},
				{"boolean-is-not-a-slug", "canary/deck", "public", "true", false, false},
				{"other-repository-opt-in", "canary/deck", "public", "other/deck", false, false},
				{"private-canonical", "noisefloor-oss/deck", "private", "", false, false},
				{"private-canary-opt-in", "canary/deck", "private", "canary/deck", false, false},
				{"internal-canary-opt-in", "canary/deck", "internal", "canary/deck", false, false},
				{"missing-visibility", "canary/deck", "", "canary/deck", false, false},
				{"fork-canonical", "noisefloor-oss/deck", "public", "", true, false},
				{"fork-canary-opt-in", "canary/deck", "public", "canary/deck", true, false},
			} {
				t.Run(file+"/"+name+"/"+scenario.name, func(t *testing.T) {
					// Evaluate the actual parsed scalar, not a second copy of the
					// admission formula. These expressions share Bash's boolean
					// operators; lowercase string operands match Actions equality.
					quote := func(v string) string { return strconv.Quote(strings.ToLower(v)) }
					condition := strings.NewReplacer(
						"github.event.repository.visibility", quote(scenario.visibility),
						"github.event.repository.fork", strconv.FormatBool(scenario.fork),
						"vars.NOISEDECK_PUBLIC_RELEASE_REPOSITORY", quote(scenario.optIn),
						"github.repository", quote(scenario.repository),
					).Replace(condition)
					cmd := exec.Command("bash", "-c", "[[ "+condition+" ]]")
					cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
					out, err := cmd.CombinedOutput()
					if (err == nil) != scenario.want {
						t.Fatalf("admission = %v, want %v: %v %s", err == nil, scenario.want, err, out)
					}
				})
			}
		}
	}
}

func TestPublicWorkflowTokenIsolation(t *testing.T) {
	writeScripts := map[string]string{
		"Ensure the release for the tag exists": `gh release view "$GITHUB_REF_NAME" >/dev/null 2>&1 && exit 0
gh release create "$GITHUB_REF_NAME" --verify-tag \
  --title "$GITHUB_REF_NAME" \
  --notes "noise-deck $GITHUB_REF_NAME — binaries and SHA256SUMS attached by the release workflow."`,
		"Attach binaries and checksums to the release": `sha256sum noise-deck-linux-* > SHA256SUMS
cat SHA256SUMS
gh release upload "$GITHUB_REF_NAME" \
  noise-deck-linux-* SHA256SUMS --clobber`,
	}
	for _, file := range []string{"public-image.yaml", "release.yml"} {
		workflow := readPublicWorkflow(t, file)
		if len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
			t.Fatalf("%s default token permissions must be read-only", file)
		}
		for key := range workflow.Env {
			if strings.Contains(key, "TOKEN") {
				t.Fatalf("%s must not set workflow-scoped tokens", file)
			}
		}
		for name, job := range workflow.Jobs {
			writer := file == "release.yml" && (name == "release-create" || name == "asset-publish")
			if writer {
				if len(job.Permissions) != 1 || job.Permissions["contents"] != "write" {
					t.Fatalf("%s publication requires only contents:write", name)
				}
			} else if len(job.Permissions) != 0 {
				t.Fatalf("repository-code job %s must inherit read-only permissions", name)
			}
			for key := range job.Env {
				if strings.Contains(key, "TOKEN") {
					t.Fatalf("%s token %s must not be job-scoped", name, key)
				}
			}
			for _, step := range job.Steps {
				if strings.HasPrefix(step.Uses, "actions/checkout@") {
					if writer || step.With["persist-credentials"] != false {
						t.Fatalf("%s must not check out repository code with writing authority or retained credentials", name)
					}
				}
				if writer {
					if step.Uses != "" && !strings.HasPrefix(step.Uses, "actions/download-artifact@") {
						t.Fatalf("writer %s may only use the pinned artifact download action", name)
					}
					if step.Run != "" {
						if want, ok := writeScripts[step.Name]; !ok || strings.TrimSpace(step.Run) != want {
							t.Fatalf("write-token job %s executes more than the fixed release/artifact commands", name)
						}
						if step.Env["GH_TOKEN"] != "${{ github.token }}" || step.Env["GH_REPO"] != "${{ github.repository }}" {
							t.Fatalf("%s must scope publication authority to its own repository", name)
						}
					}
				} else {
					for key := range step.Env {
						if strings.Contains(key, "TOKEN") {
							t.Fatalf("repository-code step %s must not receive publication tokens", step.Name)
						}
					}
				}
			}
		}
	}
}
