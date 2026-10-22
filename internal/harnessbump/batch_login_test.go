package harnessbump

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func catalogueFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dockerfile, err := os.ReadFile(filepath.Join("../..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	pins := ReadPins(dockerfile)
	var readme strings.Builder
	files := map[string]bool{"Dockerfile": true, "internal/supervisor/provider_profile.go": true, "internal/supervisor/provider_profile_test.go": true}
	for _, h := range Catalogue {
		for _, repeat := range h.Repeats {
			if repeat.Path == "docs/provider-contracts/README.md" {
				fmt.Fprintf(&readme, repeat.Format+"\n", pins[h.VersionArg])
				continue
			}
			files[repeat.Path] = true
		}
	}
	for file := range files {
		data, err := os.ReadFile(filepath.Join("../..", file))
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	readmePath := filepath.Join(root, "docs", "provider-contracts", "README.md")
	if err := os.MkdirAll(filepath.Dir(readmePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readmePath, []byte(readme.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestEveryHarnessAutomaticallyMovesItsRequiredPins(t *testing.T) {
	root := catalogueFixture(t)
	for _, h := range Catalogue {
		if h.Source == SourceManual {
			t.Fatalf("%s is excluded from automatic discovery", h.Name)
		}
		from, _, err := Pinned(root, h)
		if err != nil {
			t.Fatal(err)
		}
		digests := map[string]string{}
		for _, artifact := range h.Artifacts {
			digests[artifact.SHAArg] = strings.Repeat("a", 64)
		}
		if _, err := Apply(root, Bump{Harness: h, From: from, To: "999.0.0", Digests: digests}); err != nil {
			t.Fatalf("%s: %v", h.Name, err)
		}
		pin, _, err := Pinned(root, h)
		if err != nil || pin != "999.0.0" {
			t.Fatalf("%s did not move: %s %v", h.Name, pin, err)
		}
		for _, repeat := range h.Repeats {
			if repeat.Required {
				assertContains(t, filepath.Join(root, repeat.Path), fmt.Sprintf(repeat.Format, "999.0.0"))
			}
		}
	}
}

func TestLoginUpdatesAreAppliedAndReconstructedLikeOtherHarnesses(t *testing.T) {
	root := catalogueFixture(t)
	saved := Catalogue
	codex, _ := Lookup("codex")
	pi, _ := Lookup("pi")
	crush, _ := Lookup("crush")
	Catalogue = []Harness{codex, pi, crush}
	t.Cleanup(func() { Catalogue = saved })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/latest") {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"version":"999.0.0"}`)
	}))
	t.Cleanup(server.Close)
	resolver := &Resolver{HTTP: server.Client(), NPMRegistry: server.URL}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_COMMITTER_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("add", ".")
	git("commit", "-m", "base")
	git("remote", "add", "origin", root)
	git("checkout", "-b", "candidate")
	result, err := ApplyBatch(context.Background(), root, resolver)
	if err != nil || strings.Join(result.Updated, ",") != "codex,pi,crush" {
		t.Fatalf("incomplete automatic update: %+v %v", result, err)
	}
	assertContains(t, filepath.Join(root, "internal/harnesslogin/versions.go"), `codexVersion = "999.0.0"`)
	assertContains(t, filepath.Join(root, "internal/harnesslogin/versions.go"), `piVersion = "999.0.0"`)
	again, err := ApplyBatch(context.Background(), root, resolver)
	if err != nil || len(again.Updated) != 0 || again.ID != result.ID {
		t.Fatalf("not idempotent: %+v %v", again, err)
	}
	git("add", ".")
	git("commit", "-m", "automatic update")
	head := git("rev-parse", "HEAD")
	git("checkout", "main")
	if ok, err := Reconstruct(root, resolver)(context.Background(), Harness{Name: "all"}, result.ID, head); err != nil || !ok {
		t.Fatalf("login update refused in settlement: %v %v", ok, err)
	}
}

func TestMissingLoginBindingFailsBeforeChangingImagePin(t *testing.T) {
	root := catalogueFixture(t)
	h, _ := Lookup("codex")
	from, _, err := Pinned(root, h)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal/harnesslogin/versions.go"), []byte("package harnesslogin\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(root, Bump{Harness: h, From: from, To: "999.0.0"}); err == nil {
		t.Fatal("silently left login binding behind")
	}
	after, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil || string(before) != string(after) {
		t.Fatal("invalid binding partially changed the pin")
	}
}
