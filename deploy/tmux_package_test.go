package deploy

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The image builds tmux from a pinned release rather than taking the
// distribution's. Debian bookworm ships 3.3a; every tmux behaviour this
// product measured was measured against 3.6a.
func TestDockerfileBuildsPinnedTmux(t *testing.T) {
	raw, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(raw)
	for _, required := range []string{
		"ARG TMUX_VERSION=3.6a",
		"ARG TMUX_SHA256=b6d8d9c76585db8ef5fa00d4931902fa4b8cbe8166f528f44fc403961a3f3759",
		"https://github.com/tmux/tmux/releases/download/${TMUX_VERSION}/tmux-${TMUX_VERSION}.tar.gz",
		"sha256sum --check --strict",
		"--enable-static",
		`test "$(tmux -V)" = "tmux 3.6a"`,
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("Dockerfile does not contain %q", required)
		}
	}
	// The distribution package would shadow the built binary on PATH and
	// silently reinstate the untested version.
	if regexp.MustCompile(`(?m)^\s+tmux \\$`).MatchString(dockerfile) {
		t.Error("Dockerfile still apt-installs tmux; the built binary would be shadowed")
	}
}

// The shipped tmux and the tested tmux must be one artifact. They drifted
// once — CI built 3.6a while the image took Debian's 3.3a — which left every
// tmux measurement in this repo describing a binary production never ran.
func TestShippedTmuxMatchesTheTestedTmux(t *testing.T) {
	dockerfile, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	workflow, err := os.ReadFile("../.github/workflows/ci-fast.yaml")
	if err != nil {
		t.Fatalf("read ci-fast.yaml: %v", err)
	}
	image := regexp.MustCompile(`ARG TMUX_VERSION=(\S+)`).FindStringSubmatch(string(dockerfile))
	tested := regexp.MustCompile(`TMUX_VERSION:\s*(\S+)`).FindStringSubmatch(string(workflow))
	if image == nil || tested == nil {
		t.Fatalf("could not read both tmux versions: image=%v tested=%v", image, tested)
	}
	if image[1] != tested[1] {
		t.Fatalf("image ships tmux %s but CI tests tmux %s", image[1], tested[1])
	}

	imageSHA := regexp.MustCompile(`ARG TMUX_SHA256=(\S+)`).FindStringSubmatch(string(dockerfile))
	testedSHA := regexp.MustCompile(`TMUX_SHA256:\s*(\S+)`).FindStringSubmatch(string(workflow))
	if imageSHA == nil || testedSHA == nil {
		t.Fatalf("could not read both tmux digests: image=%v tested=%v", imageSHA, testedSHA)
	}
	if imageSHA[1] != testedSHA[1] {
		t.Fatalf("image pins tmux digest %s but CI pins %s", imageSHA[1], testedSHA[1])
	}
}
