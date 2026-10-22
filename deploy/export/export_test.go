package exportgate

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

const requiredPublicFiles = `LICENSE
README.md
SECURITY.md
DEVELOPMENT.md
THIRD_PARTY_NOTICES.md
.github/ISSUE_TEMPLATE/config.yml
skills/install-deck/SKILL.md
skills/update-deck/SKILL.md
scripts/demo/capacity-routing-demo.sh
deploy/design_docs_private_test.go
internal/harnessbump/catalogue_docs_private_test.go
internal/supervisor/sandbox_plan_docs_private_test.go
deploy/verify-merge-gate.py
deploy/verify-merge-gate-test.py
deploy/ci_scope_private_test.go`

// Keep the established export boundary even if an attribute is removed.
var excludedPaths = []string{
	"docs/adr/",
	"docs/decisions/",
	"docs/sealed-deck-prd.md",
	"docs/sealed-storage-runbook.md",
	"docs/design-brief.md",
	"docs/design-canonical.md",
	"docs/global-menu-visual-gate.md",
	"docs/tutorial-coverage.md",
	"docs/PRINCIPLES.md",
	"docs/release-images.md",
	"docs/continuity-protocol-v1.md",
	"docs/continuity-protocol-v1-vectors.json",
	"docs/project-app-preview-design.md",
	"docs/project-app-preview-loopback.md",
	"docs/project-app-preview-api.md",
	"docs/project-app-preview-operations.md",
	"docs/security/",
	"docs/provider-contracts/*.md",
	"AGENTS.md", "SDLC.md", ".noise/", ".tasks/", "docs/internal/",
	".github/workflows/harness-bump.yaml", ".github/workflows/harness-bump-merge.yaml",
	".github/workflows/ci-heal.yaml",
	"internal/ciheal/", "scripts/ciheal/",
	"deploy/export/ciheal_private_test.go", "deploy/ciheal_workflow_private_test.go",
	"deploy/jev_advisor_private_test.go", ".github/workflows/ci-advisor.yaml",
	"deploy/harness_batch_test.go", "deploy/harness_review_source_test.go",
	"deploy/systemd/", "deploy/runner_watchdog_test.go",
	"deploy/ci_workflow_private_test.go", "deploy/subscription_review_private_test.go",
	"deploy/harness-review-runner-group.json",
	"internal/harnessbump/reviewprofile_test.go", "internal/harnessbump/reviewworktree_test.go",
	"internal/harnessbump/settle_workflow_test.go", ".gitattributes",
	"docs/production-deployment.md",
	"docs/project-preview-cloudflare.md",
	"web/public/fonts/*.woff2", "web/public/fonts/*.woff",
	"web/public/fonts/*.otf", "web/public/fonts/*.ttf",
}

// Fragments keep the scanner's source from matching its own deny-list.
const (
	privateOrg       = "noisefloor" + "-app"
	privateDomain    = "noisefloor" + `\.app`
	privateProject   = "research" + "-agent"
	privateWorkspace = "/workspace/" + "agent-console"
	taskCitation     = `[Tt]` + `ask[ -]?#?\d{2,4}`
	prCitation       = `P` + `R ?#\d+`
	requirement      = `\bN?` + `FR-[A-Z]*\d`
)

var privatePatterns = []*regexp.Regexp{
	regexp.MustCompile(privateOrg), regexp.MustCompile(privateDomain),
	regexp.MustCompile(privateWorkspace), regexp.MustCompile(privateProject),
	regexp.MustCompile("brain" + "company"), regexp.MustCompile("doug" + "@"),
	regexp.MustCompile("mac" + "mini"), regexp.MustCompile("Syn" + "ology"),
	regexp.MustCompile(taskCitation), regexp.MustCompile(prCitation),
	regexp.MustCompile(`\(#` + `\d{3,4}\)`), regexp.MustCompile(requirement),
	regexp.MustCompile("review" + " round"),
}

type exception struct {
	path, pattern, reason string
}

func TestArchive(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	// Do not discover a parent checkout when testing an extracted source tree.
	if _, err := os.Stat(filepath.Join(root, ".git")); os.IsNotExist(err) {
		t.Skip("source export has no Git checkout")
	} else if err != nil {
		t.Fatal(err)
	}
	gitEnv := gitRunner(t, root)
	git := func(args ...string) []byte {
		t.Helper()
		return gitEnv(nil, nil, args...)
	}
	files := readArchive(t, git("archive", "HEAD"))
	tracked := strings.Split(string(git("ls-tree", "-r", "--name-only", "-z", "HEAD")), "\x00")

	t.Run("excluded_paths", func(t *testing.T) {
		// The baseline list keeps the established export boundary even if an
		// attribute is removed. exportIgnoredPaths additionally derives every
		// committed rule — file and directory rules alike, nested attribute
		// files included — through Git's own attribute resolution, so newly
		// added rules are enforced before the baseline is updated. A curated
		// mirror snapshot carries no .gitattributes at all (the file
		// export-ignores itself), contributes nothing there, and only the
		// baseline applies.
		patterns := append(slices.Clone(excludedPaths), exportIgnoredPaths(t, gitEnv)...)
		for _, name := range sortedNames(files) {
			for _, pattern := range patterns {
				if excluded(name, pattern) {
					t.Errorf("excluded archive member: %s (%s)", name, pattern)
					break
				}
			}
		}
	})
	t.Run("open_fonts", func(t *testing.T) {
		var want, got []string
		for _, name := range tracked {
			if strings.HasPrefix(name, "web/public/fonts/free/") {
				want = append(want, name)
			}
		}
		for name := range files {
			if strings.HasPrefix(name, "web/public/fonts/") {
				got = append(got, name)
			}
		}
		slices.Sort(want)
		slices.Sort(got)
		if len(want) == 0 || !slices.Equal(want, got) {
			t.Errorf("exported fonts = %q; want exactly the tracked open set %q", got, want)
		}
	})
	t.Run("public_files", func(t *testing.T) {
		for _, name := range strings.Fields(requiredPublicFiles) {
			if _, ok := files[name]; ok {
				continue
			}
			t.Errorf("required public file missing from archive: %s", name)
		}
	})
	t.Run("allowlist", func(t *testing.T) {
		for _, entry := range allowedPrivateReferences {
			if !regexp.MustCompile(entry.pattern).Match(files[entry.path]) {
				t.Errorf("stale private-reference exception: %s / %s", entry.path, entry.pattern)
			}
		}
	})
	t.Run("private_markers", func(t *testing.T) {
		for _, name := range sortedNames(files) {
			for _, finding := range privateFileFindings(name, files[name]) {
				t.Error(finding)
			}
		}
	})
	t.Run("markdown_links", func(t *testing.T) {
		used := make([]bool, len(allowedBrokenMarkdownLinks))
		for _, name := range sortedNames(files) {
			if strings.EqualFold(path.Ext(name), ".md") {
				for _, finding := range brokenMarkdownLinks(name, string(files[name]), files) {
					allowed := false
					for i, entry := range allowedBrokenMarkdownLinks {
						if name == entry.path && strings.HasSuffix(finding, entry.finding) {
							used[i], allowed = true, true
						}
					}
					if !allowed {
						t.Error(finding)
					}
				}
			}
		}
		for i, entry := range allowedBrokenMarkdownLinks {
			if entry.reason == "" || !used[i] {
				t.Errorf("missing reason or stale Markdown-link exception: %+v", entry)
			}
		}
	})
}

// gitRunner returns a runner for Git commands in root. Optional environment
// overrides and stdin let callers drive check-attr through a temporary index.
func gitRunner(t *testing.T, root string) func(env []string, stdin []byte, args ...string) []byte {
	t.Helper()
	return func(env []string, stdin []byte, args ...string) []byte {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if env != nil {
			cmd.Env = env
		}
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
		return out
	}
}

// exportIgnoredPaths returns the tracked paths and directory prefixes
// `git archive` keeps out of an export, derived from the committed attribute
// files — nested ones included — by asking Git itself. File rules resolve per
// tracked path; directory rules only report "set" for the directory entry
// itself, spelled with its trailing slash, which is exactly the prefix form
// excluded matches. A temporary index avoids reading uncommitted attributes
// or touching the user's index, and the read-tree/check-attr combination
// works on older Git releases. A tree without any .gitattributes, such as a
// curated mirror snapshot, yields an empty result instead of an error.
func exportIgnoredPaths(t *testing.T, gitEnv func(env []string, stdin []byte, args ...string) []byte) []string {
	t.Helper()
	var inputs []string
	add := func(listing []byte, suffix string) {
		for _, name := range strings.Split(strings.TrimSuffix(string(listing), "\x00"), "\x00") {
			if name != "" {
				inputs = append(inputs, name+suffix)
			}
		}
	}
	add(gitEnv(nil, nil, "ls-tree", "-r", "--name-only", "-z", "HEAD"), "")
	add(gitEnv(nil, nil, "ls-tree", "-r", "-d", "-t", "--name-only", "-z", "HEAD"), "/")
	var stdin []byte
	if len(inputs) > 0 {
		stdin = []byte(strings.Join(inputs, "\x00") + "\x00")
	}
	env := append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(t.TempDir(), "index"))
	gitEnv(env, nil, "read-tree", "HEAD")
	attrs := gitEnv(env, stdin, "check-attr", "--cached", "-z", "--stdin", "export-ignore")
	fields := strings.Split(strings.TrimSuffix(string(attrs), "\x00"), "\x00")
	var paths []string
	for i := 0; i+2 < len(fields); i += 3 {
		if fields[i+2] == "set" {
			paths = append(paths, fields[i])
		}
	}
	return paths
}

func readArchive(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeDir || header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if !filepath.IsLocal(header.Name) || path.Clean(header.Name) != header.Name {
			t.Fatalf("unsafe archive member %q", header.Name)
		}
		if header.Typeflag != tar.TypeReg {
			t.Fatalf("unsupported archive member %s (type %d)", header.Name, header.Typeflag)
		}
		if _, duplicate := files[header.Name]; duplicate {
			t.Fatalf("duplicate archive member %s", header.Name)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name] = body
	}
	if len(files) == 0 {
		t.Fatal("empty archive")
	}
	return files
}

func sortedNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func excluded(name, pattern string) bool {
	pattern = strings.TrimPrefix(pattern, "/")
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(name, pattern)
	}
	match, err := path.Match(pattern, name)
	return err == nil && match
}

func privateFileFindings(name string, data []byte) []string {
	switch strings.ToLower(path.Ext(name)) {
	case ".woff2", ".png", ".ico":
		return nil
	}
	var findings []string
	for i, line := range bytes.Split(data, []byte{'\n'}) {
		for _, hit := range privateHits(name, line) {
			findings = append(findings, fmt.Sprintf("%s:%d: private marker %q: %s", name, i+1, hit, bytes.TrimSpace(line)))
		}
	}
	return findings
}

var compiledExceptions = sync.OnceValue(func() map[string][]*regexp.Regexp {
	result := make(map[string][]*regexp.Regexp)
	for _, entry := range allowedPrivateReferences {
		result[entry.path] = append(result[entry.path], regexp.MustCompile(entry.pattern))
	}
	return result
})

func privateHits(name string, line []byte) []string {
	var allowed [][2]int
	for _, pattern := range compiledExceptions()[name] {
		for _, span := range pattern.FindAllIndex(line, -1) {
			allowed = append(allowed, [2]int{span[0], span[1]})
		}
	}
	var hits []string
	for _, pattern := range privatePatterns {
		for _, hit := range pattern.FindAllIndex(line, -1) {
			exempt := slices.ContainsFunc(allowed, func(span [2]int) bool { return span[0] <= hit[0] && span[1] >= hit[1] })
			if !exempt {
				hits = append(hits, string(line[hit[0]:hit[1]]))
			}
		}
	}
	return hits
}

func TestPrivateMarkerRules(t *testing.T) {
	for _, pattern := range privatePatterns {
		if pattern.MatchString("Public source, task data, pull requests, requirements and review results.") {
			t.Errorf("overbroad pattern %s", pattern)
		}
	}
	for _, marker := range []string{privateOrg, "noisefloor" + ".app", privateWorkspace, privateProject, "brain" + "company", "doug" + "@", "mac" + "mini", "Syn" + "ology", "Task " + "123", "task-" + "123", "task#" + "123", "PR #" + "123", "(#" + "123)", "FR-" + "S1", "NFR-" + "SEC1", "review" + " round"} {
		if len(privateHits("README.md", []byte(marker))) == 0 {
			t.Errorf("missed %q", marker)
		}
	}
	seen := make(map[string]bool)
	for _, entry := range allowedPrivateReferences {
		key := entry.path + "\x00" + entry.pattern
		if entry.reason == "" || seen[key] {
			t.Errorf("missing reason or duplicate exception: %+v", entry)
		}
		seen[key] = true
		regexp.MustCompile(entry.pattern)
		t.Logf("allow %s / %s: %s", entry.path, entry.pattern, entry.reason)
	}
	line := "ARG NOISE_TASKS_REPO=https://github.com/" + privateOrg + "/tasks.git # copied from " + privateOrg
	if got := privateHits("Dockerfile", []byte(line)); len(got) != 2 {
		t.Fatalf("private build default or prose escaped the gate: %v", got)
	}
	for _, name := range []string{"web/src/terminal/task-ref-hover.test.ts", "web/src/ui/markdown.test.ts", "web/src/ui/task-ref.test.ts"} {
		line := "Task " + "126; see task " + "777 for why"
		if got := privateHits(name, []byte(line)); !slices.Equal(got, []string{"task " + "777"}) {
			t.Errorf("%s: exception swallowed unrelated prose: %v", name, got)
		}
	}
}

func TestPrivateMarkersInNonUTF8Files(t *testing.T) {
	for _, invalid := range []byte{0, 0xff} {
		data := append([]byte("first line\n"+privateOrg+" task "+"123 "), invalid)
		for _, name := range []string{"docs/probe.md", "probe.bin", "probe"} {
			got := privateFileFindings(name, data)
			if len(got) != 2 {
				t.Fatalf("%s with byte %#x: want two findings, got %v", name, invalid, got)
			}
			for _, finding := range got {
				if !strings.HasPrefix(finding, name+":2:") {
					t.Errorf("incorrect finding location: %s", finding)
				}
			}
		}
		for _, name := range []string{"font.woff2", "image.png", "favicon.ico"} {
			if got := privateFileFindings(name, data); len(got) != 0 {
				t.Errorf("binary asset %s: %v", name, got)
			}
		}
	}
}

func TestExcludedPaths(t *testing.T) {
	for _, name := range []string{
		"docs/internal/runbook.md", "web/public/fonts/licensed.woff2", "AGENTS.md",
		"docs/adr/README.md", "docs/decisions/authentication-deployment-model.md",
		"docs/security/README.md", "docs/provider-contracts/README.md",
		"docs/continuity-protocol-v1-vectors.json",
		"deploy/systemd/runner.service", "deploy/runner_watchdog_test.go",
		"deploy/ci_workflow_private_test.go", "deploy/subscription_review_private_test.go",
		"deploy/harness-review-runner-group.json",
		"docs/production-deployment.md", "docs/project-preview-cloudflare.md",
	} {
		if !slices.ContainsFunc(excludedPaths, func(pattern string) bool { return excluded(name, pattern) }) {
			t.Errorf("missed excluded path %s", name)
		}
	}
	for _, name := range []string{"docs/internalized.md", "web/public/fonts/free/open.woff2", "README.md",
		"web/src/styles/fonts/free.css", "web/src/styles/fonts/select.ts",
		"web/src/styles/fonts/installation.ts", "web/vite.config.ts",
		"web/src/bench/main.ts", "web/bench.html", "compose.sealed.yaml", "compose.workspace-plain.yaml",
		"deploy/sealed-compose.sh", "deploy/sealed_compose_test.go",
		"web/private/fonts/licensed.css", "web/src/styles/fonts/licensed.test.ts",
		"web/tools/font-docker-probe.mjs", "scripts/provider-spike/conformance/runner/main.go",
		"compose.licensed-fonts.yaml", "compose.go-licensed-fonts.yaml",
		"compose.relay-tunnel.yaml", "compose.preview-tunnel.yaml", "compose.production.yaml",
		"deploy/production-compose.sh", "deploy/verify-production-compose.py",
		"deploy/release-images.py", "deploy/release-images-test.py",
		".github/workflows/release-acceptance.yaml", ".github/workflows/release-images.yaml",
		"deploy/production_compose_test.go", "deploy/release_images_test.go",
		"docs/project-app-previews.md", "docs/provider-contracts/codex.yaml",
		"docs/provider-contracts/opencode.yaml", "docs/provider-contracts/shell.yaml",
		"scripts/demo/capacity-routing-demo.sh", "deploy/design_docs_private_test.go",
		"internal/harnessbump/catalogue_docs_private_test.go", "internal/supervisor/sandbox_plan_docs_private_test.go",
		"deploy/verify-merge-gate.py", "deploy/verify-merge-gate-test.py", "deploy/ci_scope_private_test.go"} {
		if slices.ContainsFunc(excludedPaths, func(pattern string) bool { return excluded(name, pattern) }) {
			t.Errorf("excluded public path %s", name)
		}
	}
}

// TestExportIgnoredPaths pins the derivation against a fixture repository:
// directory rules must arrive in their trailing-slash prefix form, file rules
// per path, and nested attribute files must contribute. This is the coverage
// the derived half of TestArchive exists for; a plain file listing alone
// drops directory rules, because check-attr reports a file under
// "dir/ export-ignore" as unspecified.
func TestExportIgnoredPaths(t *testing.T) {
	root := t.TempDir()
	write := func(name, data string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitattributes", "priv/ export-ignore\nsecret.txt export-ignore\n")
	write("sub/.gitattributes", "local.txt export-ignore\n")
	write("priv/a.md", "x")
	write("priv/deep/b.md", "x")
	write("secret.txt", "x")
	write("keep.txt", "x")
	write("sub/local.txt", "x")
	write("sub/other.txt", "x")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", ".")
	run("config", "user.email", "export-gate@example.invalid")
	run("config", "user.name", "export gate test")
	run("add", "-A")
	run("commit", "-qm", "fixture")
	got := exportIgnoredPaths(t, gitRunner(t, root))
	slices.Sort(got)
	want := []string{"priv/", "secret.txt", "sub/local.txt"}
	if !slices.Equal(got, want) {
		t.Errorf("exportIgnoredPaths = %q, want %q", got, want)
	}
}
