package deploy

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const macosScripts = "../skills/install-deck/scripts"

type plistNode struct {
	Name     string
	Text     string
	Children []*plistNode
}

func parsePlist(t *testing.T, doc string) *plistNode {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(doc))
	dec.Strict = true
	root := &plistNode{}
	stack := []*plistNode{root}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("plist is not well-formed XML: %v", err)
		}
		switch v := tok.(type) {
		case xml.StartElement:
			n := &plistNode{Name: v.Name.Local}
			top := stack[len(stack)-1]
			top.Children = append(top.Children, n)
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			stack[len(stack)-1].Text += string(v)
		}
	}
	if len(root.Children) != 1 || root.Children[0].Name != "plist" || len(root.Children[0].Children) != 1 {
		t.Fatal("plist must hold exactly one root dict")
	}
	return root.Children[0].Children[0]
}

// dict pairs each <key> with the element that follows it.
func (n *plistNode) dict(t *testing.T) map[string]*plistNode {
	t.Helper()
	if n.Name != "dict" || len(n.Children)%2 != 0 {
		t.Fatalf("element %q is not a key/value dict", n.Name)
	}
	out := map[string]*plistNode{}
	for i := 0; i < len(n.Children); i += 2 {
		if n.Children[i].Name != "key" {
			t.Fatalf("expected a key, got %q", n.Children[i].Name)
		}
		out[n.Children[i].Text] = n.Children[i+1]
	}
	return out
}

func runMacosScript(t *testing.T, env []string, script string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{filepath.Join(macosScripts, script)}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchAgentRenderIsValidPlistWithRecordedRuntime(t *testing.T) {
	home := "/Users/Your Home & Files"
	out, stderr, err := runMacosScript(t, []string{"HOME=" + home, "CODEX_HOME=/old/service/.codex", "GH_CONFIG_DIR=/old/service/gh"}, "macos-launch-agent.sh", "render",
		"--label", "dev.example.deck", "--binary", "/Users/me/Deck Home/noise-deck",
		"--config", "/Users/me/Deck Home/config.yaml", "--path", "/Users/me/Deck Home/tools:/usr/bin:/bin",
		"--log-dir", "/Users/me/Deck Home/logs", "--env", "NOTE=a&b<c>")
	if err != nil {
		t.Fatalf("render failed: %v\n%s", err, stderr)
	}
	top := parsePlist(t, out).dict(t)
	for _, key := range []string{"Label", "ProgramArguments", "EnvironmentVariables", "RunAtLoad", "KeepAlive", "StandardOutPath", "StandardErrorPath"} {
		if top[key] == nil {
			t.Errorf("plist lacks %s", key)
		}
	}
	if top["RunAtLoad"].Name != "true" || top["KeepAlive"].Name != "true" {
		t.Error("RunAtLoad and KeepAlive must both be true")
	}
	var args []string
	for _, c := range top["ProgramArguments"].Children {
		args = append(args, c.Text)
	}
	want := []string{"/Users/me/Deck Home/noise-deck", "serve", "-config", "/Users/me/Deck Home/config.yaml"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("ProgramArguments = %q, want %q", args, want)
	}
	env := top["EnvironmentVariables"].dict(t)
	if env["HOME"] == nil {
		t.Fatal("plist must record your login HOME")
	}
	if env["PATH"].Text != "/Users/me/Deck Home/tools:/usr/bin:/bin" {
		t.Errorf("PATH = %q; the pinned tools directory must come first", env["PATH"].Text)
	}
	if env["HOME"].Text != home || env["NOTE"].Text != "a&b<c>" {
		t.Errorf("environment not recorded verbatim: HOME=%q NOTE=%q", env["HOME"].Text, env["NOTE"].Text)
	}
	for _, name := range []string{"CODEX_HOME", "GH_CONFIG_DIR"} {
		if env[name] != nil {
			t.Errorf("ambient %s must not redirect native sign-ins", name)
		}
	}
	if top["StandardOutPath"].Text != "/Users/me/Deck Home/logs/deck.log" {
		t.Errorf("StandardOutPath = %q", top["StandardOutPath"].Text)
	}
}

func TestLaunchAgentRenderRefusesUnsafeInput(t *testing.T) {
	base := []string{"render", "--label", "dev.example.deck", "--binary", "/b", "--config", "/c", "--path", "/t:/usr/bin", "--log-dir", "/l"}
	for name, extra := range map[string][]string{
		"PATH through --env":   {"--env", "PATH=/evil"},
		"HOME through --env":   {"--env", "HOME=/service/home"},
		"relative path":        {"--path", "tools"},
		"label with a slash":   {"--label", "../x"},
		"multi-line env value": {"--env", "A=b\nc"},
		"malformed env":        {"--env", "novalue"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := runMacosScript(t, nil, "macos-launch-agent.sh", append(append([]string{}, base...), extra...)...); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}
}

func TestLaunchAgentRenderRefusesInvalidHome(t *testing.T) {
	for _, home := range []string{"", "relative/home", "/Users/me\nother"} {
		t.Run(home, func(t *testing.T) {
			_, _, err := runMacosScript(t, []string{"HOME=" + home}, "macos-launch-agent.sh", "render",
				"--label", "dev.example.deck", "--binary", "/b", "--config", "/c", "--path", "/t:/usr/bin", "--log-dir", "/l")
			if err == nil {
				t.Fatal("expected an absolute, single-line HOME")
			}
		})
	}
}

type launchStage struct {
	root, home, bin, calls, opened, openArgs, logDir, setup, stuck string
	env                                                            []string
}

// stageLaunch fakes the macOS tools the helper calls. The fake launchctl
// writes the setup link after bootstrap, as a Deck release with first-run
// password creation does; an empty setupMode leaves the file unwritten, as an
// older release does. Every fixture path reaches the stubs through quoted
// environment variables, and the root holds a space.
func stageLaunch(t *testing.T, uid, setupMode string) launchStage {
	t.Helper()
	if uid != "0" && os.Getuid() == 0 {
		t.Skip("the setup-link ownership check needs a non-root test user")
	}
	root := filepath.Join(t.TempDir(), "Deck Test")
	s := launchStage{root: root, home: filepath.Join(root, "home"), bin: filepath.Join(root, "bin"), calls: filepath.Join(root, "calls"),
		opened: filepath.Join(root, "opened"), openArgs: filepath.Join(root, "open-args"), logDir: filepath.Join(root, "logs"),
		setup: filepath.Join(root, "state", "setup-url"), stuck: filepath.Join(root, "stuck")}
	writeStub(t, s.bin, "id", "echo \"$FAKE_UID\"\n")
	writeStub(t, s.bin, "open", "echo called > \"$FAKE_OPENED.open\"\n")
	writeStub(t, s.bin, "osascript", "echo \"$*\" > \"$FAKE_OPEN_ARGS\"\ncat > \"$FAKE_OPENED\"\n")
	writeStub(t, s.bin, "sleep", "exec /bin/sleep 0.05\n")
	writeStub(t, s.bin, "launchctl", `echo "$*" >> "$FAKE_CALLS"
case "$1" in
bootstrap)
  : > "$FAKE_LOADED"
  if [ -n "$FAKE_SETUP_MODE" ]; then
    /bin/sleep 0.05
    mkdir -p "$(dirname "$FAKE_SETUP")"
    printf 'https://127.0.0.1:1/setup?token=SECRET\n' > "$FAKE_SETUP.tmp"
    chmod "$FAKE_SETUP_MODE" "$FAKE_SETUP.tmp"
    mv "$FAKE_SETUP.tmp" "$FAKE_SETUP"
  fi ;;
bootout)
  [ ! -e "$FAKE_STUCK" ] || exit 5
  [ -e "$FAKE_LOADED" ] || exit 113
  rm -f "$FAKE_LOADED" ;;
print) [ -e "$FAKE_LOADED" ] ;;
esac
`)
	if err := os.MkdirAll(s.home, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "database:\n  path: \"" + filepath.Join(root, "state", "control.db") + "\"\n"
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	writeStub(t, root, "noise-deck", "exit 0\n")
	s.env = []string{"HOME=" + s.home, "PATH=" + s.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_UID=" + uid, "FAKE_CALLS=" + s.calls, "FAKE_OPENED=" + s.opened, "FAKE_OPEN_ARGS=" + s.openArgs,
		"FAKE_LOADED=" + filepath.Join(root, "loaded"), "FAKE_STUCK=" + s.stuck, "FAKE_SETUP=" + s.setup, "FAKE_SETUP_MODE=" + setupMode}
	return s
}

func (s launchStage) install(t *testing.T, extra ...string) (string, string, error) {
	args := append([]string{"install", "--label", "dev.example.deck", "--binary", filepath.Join(s.root, "noise-deck"),
		"--config", filepath.Join(s.root, "config.yaml"), "--path", "/t:/usr/bin", "--log-dir", s.logDir}, extra...)
	return runMacosScript(t, s.env, "macos-launch-agent.sh", args...)
}

func (s launchStage) uninstall(t *testing.T) (string, string, error) {
	return runMacosScript(t, s.env, "macos-launch-agent.sh", "uninstall", "--label", "dev.example.deck")
}

func (s launchStage) plist() string {
	return filepath.Join(s.home, "Library", "LaunchAgents", "dev.example.deck.plist")
}

func TestLaunchAgentInstallBootstrapsAndOpensSetupLinkPrivately(t *testing.T) {
	uid := strconv.Itoa(os.Getuid())
	s := stageLaunch(t, uid, "600")
	stdout, stderr, err := s.install(t)
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, stderr)
	}
	body, err := os.ReadFile(s.plist())
	if err != nil {
		t.Fatal(err)
	}
	env := parsePlist(t, string(body)).dict(t)["EnvironmentVariables"].dict(t)
	if env["HOME"] == nil || env["HOME"].Text != s.home {
		t.Fatalf("installed agent must use your home %q, got %v", s.home, env["HOME"])
	}
	entries, err := os.ReadDir(s.home)
	if err != nil || len(entries) != 1 || entries[0].Name() != "Library" {
		t.Fatalf("install must only add the LaunchAgents directory to your home: %v, %v", entries, err)
	}
	calls, _ := os.ReadFile(s.calls)
	if strings.TrimSpace(string(calls)) != "bootstrap gui/"+uid+" "+s.plist() {
		t.Errorf("launchctl calls = %q", calls)
	}
	if opened, _ := os.ReadFile(s.opened); string(opened) != "open location \"https://127.0.0.1:1/setup?token=SECRET\"\n" {
		t.Errorf("browser opener received %q on stdin", opened)
	}
	if args, _ := os.ReadFile(s.openArgs); strings.Contains(string(args), "SECRET") || strings.TrimSpace(string(args)) != "" {
		t.Errorf("browser opener was given arguments: %q", args)
	}
	if _, err := os.Stat(s.opened + ".open"); err == nil {
		t.Error("the link was passed to open, which would expose it in the process list")
	}
	if strings.Contains(stdout+stderr, "SECRET") {
		t.Errorf("setup link leaked into output: %q %q", stdout, stderr)
	}
	if info, err := os.Stat(s.logDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("log directory must be owner-only: %v %v", info, err)
	}
	if _, _, err := s.install(t); err == nil {
		t.Error("a second install over an existing agent must refuse")
	}

	if _, stderr, err := s.uninstall(t); err != nil {
		t.Fatalf("uninstall failed: %v\n%s", err, stderr)
	}
	if _, err := os.Stat(s.plist()); !os.IsNotExist(err) {
		t.Error("uninstall left the plist behind")
	}
	calls, _ = os.ReadFile(s.calls)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	target := "gui/" + uid + "/dev.example.deck"
	if len(lines) < 3 || lines[len(lines)-2] != "bootout "+target || lines[len(lines)-1] != "print "+target {
		t.Errorf("uninstall must boot the job out and confirm it is gone; launchctl calls = %q", lines)
	}
}

func TestLaunchAgentInstallHonorsSetupFileOverride(t *testing.T) {
	s := stageLaunch(t, strconv.Itoa(os.Getuid()), "600")
	elsewhere := filepath.Join(s.root, "elsewhere", "link")
	s.env = append(s.env, "FAKE_SETUP="+elsewhere)
	if _, stderr, err := s.install(t, "--setup-file", elsewhere); err != nil {
		t.Fatalf("install failed: %v\n%s", err, stderr)
	}
	if opened, _ := os.ReadFile(s.opened); !strings.Contains(string(opened), "token=SECRET") {
		t.Errorf("the override file was not used; opener received %q", opened)
	}
}

func TestLaunchAgentInstallExplainsAMissingSetupLink(t *testing.T) {
	s := stageLaunch(t, strconv.Itoa(os.Getuid()), "")
	stdout, stderr, err := s.install(t)
	if err != nil {
		t.Fatalf("a missing setup link must not fail an install that started Deck: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, filepath.Join(s.logDir, "deck.log")) || !strings.Contains(stdout, "do not share it with an agent") {
		t.Errorf("the fallback must say where to read the one-time password yourself: %q", stdout)
	}
	if _, statErr := os.Stat(s.opened); statErr == nil {
		t.Error("a browser opened without a setup link")
	}
}

func TestLaunchAgentInstallDoesNotOpenAWorldReadableSetupLink(t *testing.T) {
	s := stageLaunch(t, strconv.Itoa(os.Getuid()), "644")
	stdout, stderr, err := s.install(t)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if _, statErr := os.Stat(s.opened); statErr == nil {
		t.Error("the browser opened a setup link anyone could read")
	}
	if strings.Contains(stdout+stderr, "SECRET") {
		t.Error("setup link leaked into output")
	}
}

func TestLaunchAgentUninstallKeepsThePlistWhileDeckIsStillLoaded(t *testing.T) {
	s := stageLaunch(t, strconv.Itoa(os.Getuid()), "600")
	if _, stderr, err := s.install(t); err != nil {
		t.Fatalf("install failed: %v\n%s", err, stderr)
	}
	if err := os.WriteFile(s.stuck, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := s.uninstall(t)
	if err == nil || !strings.Contains(stderr, "still loaded") {
		t.Fatalf("expected a refusal while launchd still holds Deck, got %v %q", err, stderr)
	}
	if _, statErr := os.Stat(s.plist()); statErr != nil {
		t.Error("uninstall removed the plist of a job that is still loaded")
	}
}

func TestLaunchAgentUninstallSucceedsWhenDeckIsAlreadyUnloaded(t *testing.T) {
	s := stageLaunch(t, strconv.Itoa(os.Getuid()), "600")
	if _, stderr, err := s.install(t); err != nil {
		t.Fatalf("install failed: %v\n%s", err, stderr)
	}
	if err := os.Remove(filepath.Join(s.root, "loaded")); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := s.uninstall(t); err != nil {
		t.Fatalf("uninstall of an unloaded agent failed: %v\n%s", err, stderr)
	}
	if _, err := os.Stat(s.plist()); !os.IsNotExist(err) {
		t.Error("uninstall left the plist behind")
	}
}

func TestLaunchAgentInstallRefusesRoot(t *testing.T) {
	s := stageLaunch(t, "0", "600")
	if _, _, err := s.install(t); err == nil {
		t.Fatal("install must refuse to run as root")
	}
	if _, err := os.Stat(s.calls); err == nil {
		t.Error("launchctl ran as root")
	}
}

func stageGHPin(t *testing.T, dockerSum string, ghVersionOutput string) (checkout, tools, stubs string, env []string, curlLog string) {
	t.Helper()
	if _, err := exec.LookPath("unzip"); err != nil {
		t.Skip("unzip is not installed")
	}
	root := filepath.Join(t.TempDir(), "Deck Test")
	checkout, tools, stubs = filepath.Join(root, "checkout"), filepath.Join(root, "tools"), filepath.Join(root, "stubs")
	for _, d := range []string{checkout, tools, stubs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: "gh_9.9.9_macOS_arm64/bin/gh", Method: zip.Deflate}
	hdr.SetMode(0o755)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, "#!/bin/sh\necho '"+ghVersionOutput+"'\n")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(root, "fixture.zip")
	if err := os.WriteFile(fixture, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	if dockerSum == "" {
		dockerSum = hex.EncodeToString(sum[:])
	}
	dockerfile := "ARG GH_CLI_VERSION=9.9.9\nARG GH_CLI_SHA256_ARM64=" + strings.Repeat("0", 64) + "\nARG GH_CLI_SHA256_DARWIN_ARM64=" + dockerSum + "\n"
	if err := os.WriteFile(filepath.Join(checkout, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	curlLog = filepath.Join(root, "curl-url")
	writeStub(t, stubs, "curl", `while [ $# -gt 0 ]; do
  case "$1" in -o) out=$2; shift ;; https://*) echo "$1" > "$FAKE_CURL_URL" ;; esac
  shift
done
cp "$FAKE_FIXTURE" "$out"
`)
	env = []string{"PATH=" + stubs + string(os.PathListSeparator) + os.Getenv("PATH"), "TMPDIR=" + root,
		"FAKE_CURL_URL=" + curlLog, "FAKE_FIXTURE=" + fixture}
	return
}

func TestPinGHInstallsOnlyAfterTheChecksumMatches(t *testing.T) {
	checkout, tools, _, env, curlLog := stageGHPin(t, "", "gh version 9.9.9 (2026-01-01)")
	stdout, stderr, err := runMacosScript(t, env, "macos-pin-gh.sh", checkout, tools)
	if err != nil {
		t.Fatalf("pin failed: %v\n%s%s", err, stdout, stderr)
	}
	info, err := os.Stat(filepath.Join(tools, "gh"))
	if err != nil || info.Mode().Perm() != 0o555 {
		t.Errorf("gh not installed read-only executable: %v %v", info, err)
	}
	url, _ := os.ReadFile(curlLog)
	if strings.TrimSpace(string(url)) != "https://github.com/cli/cli/releases/download/v9.9.9/gh_9.9.9_macOS_arm64.zip" {
		t.Errorf("downloaded %q", url)
	}
	if _, _, err := runMacosScript(t, env, "macos-pin-gh.sh", checkout, tools); err == nil {
		t.Error("an existing gh must not be replaced")
	}
}

func TestPinGHRefusesAChecksumMismatch(t *testing.T) {
	checkout, tools, _, env, _ := stageGHPin(t, strings.Repeat("a", 64), "gh version 9.9.9 (2026-01-01)")
	_, stderr, err := runMacosScript(t, env, "macos-pin-gh.sh", checkout, tools)
	if err == nil || !strings.Contains(stderr, "SHA-256") {
		t.Fatalf("expected a checksum refusal, got %v %q", err, stderr)
	}
	if _, statErr := os.Stat(filepath.Join(tools, "gh")); statErr == nil {
		t.Error("gh was installed despite a checksum mismatch")
	}
}

func TestPinGHRefusesAWrongVersion(t *testing.T) {
	checkout, tools, _, env, _ := stageGHPin(t, "", "gh version 1.0.0 (2020-01-01)")
	if _, _, err := runMacosScript(t, env, "macos-pin-gh.sh", checkout, tools); err == nil {
		t.Fatal("expected a version refusal")
	}
	if _, statErr := os.Stat(filepath.Join(tools, "gh")); statErr == nil {
		t.Error("gh was installed with the wrong version")
	}
}

func TestDockerfileDeclaresTheDarwinGHPin(t *testing.T) {
	body, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^ARG GH_CLI_VERSION=\d+\.\d+\.\d+$`).Match(body) ||
		!regexp.MustCompile(`(?m)^ARG GH_CLI_SHA256_DARWIN_ARM64=[0-9a-f]{64}$`).Match(body) {
		t.Fatal("the Dockerfile must declare GH_CLI_VERSION and GH_CLI_SHA256_DARWIN_ARM64 for the native macOS install")
	}
}
