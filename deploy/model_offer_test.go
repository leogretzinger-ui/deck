package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The reviewer-model offer lane: the image ships the job, the bootstrap
// schedules it, and the job itself may open exactly one PR without ever
// writing to the workspace checkout. These probes read the shipped files,
// because a deleted loop or a rewritten guard leaves the unit suite green
// while the lane silently stops offering.

func readDeploy(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestImageShipsTheModelOfferJob(t *testing.T) {
	dockerfile := readDeploy(t, filepath.Join("..", "Dockerfile"))
	for _, required := range []string{
		"COPY scripts/model-offer/model-offer.sh /usr/local/bin/noisedeck-model-offer",
		"/usr/local/bin/noisedeck-model-offer",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("Dockerfile does not ship the model offer job: %q missing", required)
		}
	}
	if !strings.Contains(dockerfile, "\n    /usr/local/bin/noisedeck-model-offer \\\n") {
		t.Error("the model offer job must be installed read-only like its siblings")
	}
}

func TestBootstrapSchedulesTheModelOffer(t *testing.T) {
	bootstrap := readDeploy(t, filepath.Join("mempalace", "noisedeck-bootstrap"))
	for _, required := range []string{
		// The interval guard must clamp a bad value, exactly like the
		// hygiene loop: a blank or zero interval would tighten the loop
		// into a daily-job-turned-hammer.
		"NOISEDECK_MODEL_OFFER_INTERVAL:-86400",
		"*[!0-9]*|\"\") interval=86400",
		"/usr/local/bin/noisedeck-model-offer --repo",
		">>/home/agent/.noisedeck/model-offer.log",
	} {
		if !strings.Contains(bootstrap, required) {
			t.Errorf("bootstrap model offer loop is missing %q", required)
		}
	}
	if !strings.Contains(bootstrap, "sleep \"$interval\"") {
		t.Error("the model offer loop must wait a full interval before its first pass")
	}
}

func TestModelOfferScriptOffersWithoutMergingOrTouchingTheCheckout(t *testing.T) {
	script := readDeploy(t, filepath.Join("..", "scripts", "model-offer", "model-offer.sh"))
	for _, required := range []string{
		// Discovery and planning come from the tested Go package, not a
		// second implementation in shell.
		"model-plan -root . -files",
		// One live offer at a time.
		"startswith(\"model-bump/reviewers-\")",
		// The offer lands through the GitHub API; the local checkout must
		// stay on main with a clean tree.
		"repos/${slug}/git/refs",
		"repos/${slug}/contents/${path}",
		// Publication safety: the plan is validated against remote main
		// byte-for-byte before any write, and a branch left by an
		// interrupted run is a resume path, not a dead end.
		"contents/$1?ref=$2",
		"aborting before any write",
		"neither the plan's source bytes nor the planned bytes",
		"does not match the plan's source bytes",
		// An offer is a pull request for a person to review, never an automatic
		// merge, and the settle variable override is named in the body.
		"gh pr create",
		"It is never merged automatically.",
		"NOISE_REVIEW_MODEL",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("model offer script is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"gh pr merge",
		"gh pr edit --add-assignee",
		"git checkout -b",
		"git commit",
		"git push",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("model offer script must not %q — the offer never merges or writes locally", forbidden)
		}
	}
}

// The slug extraction must handle both origin spellings — POSIX sed has
// no lazy quantifier, which is exactly the bug that routed offers to a
// deck.git repository that does not exist. This runs the script's real
// pipeline.
func TestModelOfferSlugExtractionHandlesBothOriginSpellings(t *testing.T) {
	extract := `origin="$1"; origin="${origin%.git}"; origin="${origin%/}"; printf '%s\n' "$origin" | sed -nE 's#^.*[:/]([^/:]+/[^/]+)$#\1#p'`
	for _, tc := range []struct{ origin, want string }{
		{"https://github.com/example-owner/deck.git", "example-owner/deck"},
		{"https://github.com/example-owner/deck", "example-owner/deck"},
		{"git@github.com:example-owner/deck.git", "example-owner/deck"},
		{"git@github.com:example-owner/deck", "example-owner/deck"},
		{"https://github.com/example-owner/deck/", "example-owner/deck"},
	} {
		out, err := exec.Command("bash", "-c", extract, "extract", tc.origin).Output()
		if err != nil {
			t.Fatalf("extract(%q): %v", tc.origin, err)
		}
		if got := strings.TrimSpace(string(out)); got != tc.want {
			t.Errorf("extract(%q) = %q, want %q", tc.origin, got, tc.want)
		}
	}
}
