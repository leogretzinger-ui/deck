package deploy

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCachePruneRequiresVerifiedDeploymentAndExplicitBuilders(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "no acknowledgement", args: []string{"default"}},
		{name: "no builders", args: []string{"--after-verified-deploy"}},
		{name: "malformed builder", args: []string{"--after-verified-deploy", "default;prune"}},
		{name: "option-shaped builder", args: []string{"--after-verified-deploy", "--all"}},
		{name: "duplicate builder", args: []string{"--after-verified-deploy", "default", "default"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, err := runBuildCachePrune(t, tt.args...)
			if err == nil {
				t.Fatal("cleanup unexpectedly succeeded")
			}
			if calls != "" {
				t.Fatalf("invalid invocation called docker: %q", calls)
			}
		})
	}
}

func TestBuildCachePruneResolvesEveryBuilderBeforeMutation(t *testing.T) {
	calls, err := runBuildCachePrune(t,
		"--after-verified-deploy", "default", "missing")
	if err == nil {
		t.Fatal("cleanup unexpectedly accepted a missing builder")
	}
	if strings.Contains(calls, "buildx prune") {
		t.Fatalf("cleanup mutated before preflight completed: %q", calls)
	}
	if calls != "buildx inspect default\nbuildx inspect missing\n" {
		t.Fatalf("preflight calls = %q", calls)
	}
}

func TestBuildCachePruneRemainsWithinNamedBuildCache(t *testing.T) {
	calls, err := runBuildCachePrune(t,
		"--after-verified-deploy", "default", "noisefloor-builder")
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	for _, want := range []string{
		"buildx inspect default\n",
		"buildx inspect noisefloor-builder\n",
		"buildx prune --builder default --all --force\n",
		"buildx prune --builder noisefloor-builder --all --force\n",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("docker calls missing %q: %q", want, calls)
		}
	}
	if strings.Count(calls, "system df\n") != 2 {
		t.Fatalf("accounting calls = %q", calls)
	}
	for _, forbidden := range []string{"system prune", "image prune", "container prune", "volume prune"} {
		if strings.Contains(calls, forbidden) {
			t.Fatalf("cleanup crossed the build-cache boundary with %q: %q", forbidden, calls)
		}
	}
}

func runBuildCachePrune(t *testing.T, args ...string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	fakeDocker := `#!/bin/sh
printf '%s\n' "$*" >>"$DOCKER_CALLS"
if test "$*" = "buildx inspect missing"; then
  exit 1
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fakeDocker), 0o555); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", append([]string{"prune-build-cache.sh"}, args...)...)
	cmd.Env = []string{
		"PATH=" + dir + ":" + os.Getenv("PATH"),
		"DOCKER_CALLS=" + calls,
		"NOISEDECK_DEPLOYMENT_FILESYSTEM_PATH=" + dir,
	}
	cmd.Dir = "."
	_, err := cmd.CombinedOutput()
	raw, readErr := os.ReadFile(calls)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
	return string(raw), err
}
