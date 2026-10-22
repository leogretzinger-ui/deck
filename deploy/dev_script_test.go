package deploy

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// devLoop stages a fake checkout for scripts/dev.sh: the script cds to its
// parent, so the copy lives under <root>/scripts; web/node_modules and the
// embedded index.html exist so the install and build steps are skipped; and
// a stub toolchain on PATH stands in for go, pnpm, and node. The stub
// backend and the stub Vite each record their PID so a test can prove the
// survivor was reaped, not merely abandoned.
type devLoop struct {
	root, backendPID, vitePID string
	env                       []string
}

func stageDevLoop(t *testing.T) devLoop {
	t.Helper()
	root := t.TempDir()
	script, err := os.ReadFile(filepath.Join("..", "scripts", "dev.sh"))
	if err != nil {
		t.Fatal(err)
	}
	must := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(root, "scripts", "dev.sh"), string(script), 0o755)
	must(filepath.Join(root, "configs", "config.example.yaml"), "server: {}\n", 0o644)
	must(filepath.Join(root, "web", "node_modules", "vite", "bin", "vite.js"), "", 0o644)
	must(filepath.Join(root, "internal", "server", "webassets", "dist", "index.html"), "<!doctype html>", 0o644)

	bin := filepath.Join(root, "stubs")
	backendPID := filepath.Join(root, "backend.pid")
	vitePID := filepath.Join(root, "vite.pid")
	// `go build -o <out> ...` writes the stub backend; FAKE_BACKEND picks
	// whether it serves (sleeps) or fails at startup like a bad config does.
	must(filepath.Join(bin, "go"), `#!/bin/sh
out=""
while [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done
[ -n "$out" ] || exit 0
cat > "$out" <<'EOF'
#!/bin/sh
# The pid is the readiness signal, so everything a test reads goes first.
echo "$NOISEDECK_AUTH_MODE" > "$FAKE_BACKEND_PID.mode"
echo $$ > "$FAKE_BACKEND_PID"
[ "$FAKE_BACKEND" = fail ] && { echo "config: refused" >&2; exit 3; }
exec sleep 300
EOF
chmod +x "$out"
`, 0o755)
	must(filepath.Join(bin, "node"), `#!/bin/sh
echo $$ > "$FAKE_VITE_PID"
[ "$FAKE_VITE" = exit ] && { sleep 1; exit 0; }
[ "$FAKE_VITE" = fail ] && { sleep 1; exit 7; }
exec sleep 300
`, 0o755)
	must(filepath.Join(bin, "pnpm"), "#!/bin/sh\nexit 0\n", 0o755)
	// `make build-frontend` stands in for the embedded build; it records
	// that it ran, so a test can assert the build was or was not wanted.
	must(filepath.Join(bin, "make"), `#!/bin/sh
echo "$@" > "$FAKE_MAKE_LOG"
mkdir -p internal/server/webassets/dist && echo built > internal/server/webassets/dist/index.html
`, 0o755)

	return devLoop{
		root: root, backendPID: backendPID, vitePID: vitePID,
		env: []string{
			"PATH=" + bin + ":/usr/bin:/bin",
			"FAKE_BACKEND_PID=" + backendPID,
			"FAKE_VITE_PID=" + vitePID,
			"FAKE_MAKE_LOG=" + filepath.Join(root, "make.log"),
			"HOME=" + root,
		},
	}
}

func (d devLoop) start(t *testing.T, extraEnv ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", filepath.Join(d.root, "scripts", "dev.sh"))
	cmd.Env = append(append([]string{}, d.env...), extraEnv...)
	cmd.Stderr = os.Stderr
	// Its own process group, so a signal to the script alone is exactly
	// that — a supervisor's signal, not a terminal's group-wide one.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// The group dies with the test whatever happens — including the failure
	// these tests exist to catch, where the script has abandoned a child.
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	})
	return cmd
}

func waitPID(t *testing.T, file string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(file); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never recorded a pid", filepath.Base(file))
	return 0
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func waitExit(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0
	case <-time.After(15 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		t.Fatal("dev.sh did not exit")
		return -1
	}
}

func waitDead(t *testing.T, name string, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if alive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("%s (pid %d) survived the loop ending", name, pid)
	}
}

func modeSeen(t *testing.T, d devLoop) string {
	t.Helper()
	waitPID(t, d.backendPID)
	raw, err := os.ReadFile(d.backendPID + ".mode")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

// The dev loop runs without sign-in: auth.mode none reaches the backend
// unless the user chose a mode, and the embedded UI — which exists only
// to serve a sign-in page — is not built for it.
func TestDevScriptDefaultsToNoAuthAndSkipsTheEmbeddedBuild(t *testing.T) {
	d := stageDevLoop(t)
	if err := os.Remove(filepath.Join(d.root, "internal", "server", "webassets", "dist", "index.html")); err != nil {
		t.Fatal(err)
	}
	cmd := d.start(t)
	if got := modeSeen(t, d); got != "none" {
		t.Fatalf("backend saw NOISEDECK_AUTH_MODE=%q, want none", got)
	}
	if _, err := os.Stat(filepath.Join(d.root, "make.log")); err == nil {
		t.Fatal("the embedded UI was built for a loop that has no sign-in page to serve")
	}
	_ = cmd.Process.Signal(syscall.SIGINT)
	waitExit(t, cmd)
}

// An exported-but-empty mode is not a choice: it would fail validation, so
// it counts as unset and the loop still defaults to none.
func TestDevScriptTreatsAnEmptyAuthModeAsUnset(t *testing.T) {
	d := stageDevLoop(t)
	cmd := d.start(t, "NOISEDECK_AUTH_MODE=")
	if got := modeSeen(t, d); got != "none" {
		t.Fatalf("backend saw NOISEDECK_AUTH_MODE=%q for an empty export, want none", got)
	}
	_ = cmd.Process.Signal(syscall.SIGINT)
	waitExit(t, cmd)
}

// Defaulting to none is only safe on a loopback bind, and the validator
// judges exposure by origins, not bind. So the script refuses to
// default to none when the effective bind — the environment's, else the
// config's — is not loopback, before starting anything. A user who
// chose none explicitly is not second-guessed.
func TestDevScriptRefusesToDefaultToNoneOnAPublicBind(t *testing.T) {
	for name, extra := range map[string][]string{
		"exported bind":     {"NOISEDECK_SERVER_BIND=0.0.0.0:8837"},
		"config bind":       {"CONFIG=server:\n  bind: \"0.0.0.0:8837\"\n  allowed_origins:\n    - \"http://127.0.0.1:8837\"\n"},
		"flow-style config": {"CONFIG=server: {bind: \"0.0.0.0:8837\", allowed_origins: [\"http://127.0.0.1:8837\"]}\n"},
		// The usual edit: the loopback default commented out, the real bind
		// below it. Reading the comment would pass the public bind.
		"commented-out default above": {"CONFIG=server:\n  # bind: \"127.0.0.1:8837\"\n  bind: \"0.0.0.0:8837\"\n"},
	} {
		t.Run(name, func(t *testing.T) {
			d := stageDevLoop(t)
			var env []string
			for _, e := range extra {
				if strings.HasPrefix(e, "CONFIG=") {
					if err := os.WriteFile(filepath.Join(d.root, "configs", "config.example.yaml"), []byte(strings.TrimPrefix(e, "CONFIG=")), 0o644); err != nil {
						t.Fatal(err)
					}
					continue
				}
				env = append(env, e)
			}
			cmd := d.start(t, env...)
			if code := waitExit(t, cmd); code == 0 {
				t.Fatal("dev.sh defaulted to none on a public bind")
			}
			if _, err := os.Stat(d.backendPID); err == nil {
				t.Fatal("the backend was started before the bind was judged")
			}
		})
	}
	// The same public bind with an explicit choice is the user's call.
	d := stageDevLoop(t)
	cmd := d.start(t, "NOISEDECK_SERVER_BIND=0.0.0.0:8837", "NOISEDECK_AUTH_MODE=none")
	if got := modeSeen(t, d); got != "none" {
		t.Fatalf("an explicit none was overridden: %q", got)
	}
	_ = cmd.Process.Signal(syscall.SIGINT)
	waitExit(t, cmd)
}

// A user's own mode wins, and under a real mode the sign-in page is
// built when it is missing.
func TestDevScriptHonoursAChosenAuthModeAndBuildsTheSignInPage(t *testing.T) {
	d := stageDevLoop(t)
	if err := os.Remove(filepath.Join(d.root, "internal", "server", "webassets", "dist", "index.html")); err != nil {
		t.Fatal(err)
	}
	cmd := d.start(t, "NOISEDECK_AUTH_MODE=password")
	if got := modeSeen(t, d); got != "password" {
		t.Fatalf("backend saw NOISEDECK_AUTH_MODE=%q, want the user's password", got)
	}
	raw, err := os.ReadFile(filepath.Join(d.root, "make.log"))
	if err != nil || !strings.Contains(string(raw), "build-frontend") {
		t.Fatalf("make build-frontend did not run for a mode that needs the sign-in page (log %q, %v)", raw, err)
	}
	_ = cmd.Process.Signal(syscall.SIGINT)
	waitExit(t, cmd)
}

// A backend that fails at startup — a bad config, a port already taken —
// must end the loop with its failure, not leave Vite serving alone behind a
// script that looks healthy.
func TestDevScriptEndsWhenTheBackendFails(t *testing.T) {
	d := stageDevLoop(t)
	cmd := d.start(t, "FAKE_BACKEND=fail")
	vite := waitPID(t, d.vitePID)
	if code := waitExit(t, cmd); code == 0 {
		t.Fatal("dev.sh exited 0 after the backend failed")
	}
	waitDead(t, "vite", vite)
}

// Vite failing — its strict port already taken is the common case — is the
// loop's failure, even though the backend was still healthy.
func TestDevScriptPropagatesViteFailure(t *testing.T) {
	d := stageDevLoop(t)
	cmd := d.start(t, "FAKE_VITE=fail")
	backend := waitPID(t, d.backendPID)
	if code := waitExit(t, cmd); code != 7 {
		t.Fatalf("dev.sh exited %d after Vite exited 7", code)
	}
	waitDead(t, "backend", backend)
}

// Vite exiting on its own ends the loop and takes the backend with it.
func TestDevScriptEndsWhenViteExits(t *testing.T) {
	d := stageDevLoop(t)
	cmd := d.start(t, "FAKE_VITE=exit")
	backend := waitPID(t, d.backendPID)
	// A clean Vite exit is the ordinary way out, and the loop's status is 0.
	if code := waitExit(t, cmd); code != 0 {
		t.Fatalf("dev.sh exited %d after Vite exited cleanly", code)
	}
	waitDead(t, "backend", backend)
}

// A signal to the script alone — a supervisor, not a terminal's process
// group — stops both halves. sh cannot run a trap while waiting on a
// foreground child, which is why both run in the background.
func TestDevScriptStopsBothOnInterrupt(t *testing.T) {
	d := stageDevLoop(t)
	cmd := d.start(t)
	backend := waitPID(t, d.backendPID)
	vite := waitPID(t, d.vitePID)
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	// Reported as the interrupt it was, not as a backend failure.
	if code := waitExit(t, cmd); code != 130 {
		t.Fatalf("dev.sh exited %d on SIGINT, want 130", code)
	}
	waitDead(t, "backend", backend)
	waitDead(t, "vite", vite)
}
