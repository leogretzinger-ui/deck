package provider

import (
	"io"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func mustShell(t *testing.T) *ShellDescriptor {
	t.Helper()
	d, err := NewShell("/bin/bash")
	if err != nil {
		t.Fatalf("NewShell: %v", err)
	}
	return d
}

func TestShellNewSessionArgsIsTheBareExecutable(t *testing.T) {
	d := mustShell(t)

	argv, err := d.NewSessionArgs(LaunchSpec{WorkDir: "/workspace/p"})
	if err != nil {
		t.Fatalf("NewSessionArgs: %v", err)
	}
	if len(argv) != 1 || argv[0] != "/bin/bash" {
		t.Fatalf("argv = %v, want exactly [/bin/bash]", argv)
	}
}

// Identity-none means there is nothing to assign. A spec carrying a
// session id is a wiring bug, and the descriptor fails rather than launching
// with identity nobody recorded.
func TestShellRejectsAnAssignedSessionID(t *testing.T) {
	d := mustShell(t)

	if _, err := d.NewSessionArgs(LaunchSpec{WorkDir: "/p", SessionID: testUUID}); err == nil {
		t.Fatal("NewSessionArgs with a session id: want error, got nil")
	}
}

// Resume is an explicit refusal, never an approximation like "the
// last shell in this directory".
func TestShellResumeAlwaysRefuses(t *testing.T) {
	d := mustShell(t)

	if _, err := d.ResumeSessionArgs(ResumeSpec{NativeSessionID: testUUID}); err == nil {
		t.Fatal("ResumeSessionArgs: want refusal, got nil")
	}
}

func TestNewShellRequiresAbsolutePath(t *testing.T) {
	if _, err := NewShell("bash"); err == nil {
		t.Fatal("NewShell with a relative path: want error, got nil")
	}
}

func TestShellInjectionContractRecordsMeasurements(t *testing.T) {
	d := mustShell(t)

	inj := d.Injection()
	if !inj.Verified {
		t.Error("shell injection contract must be measured, not declared")
	}
	if !inj.UseBracketedPaste || inj.SubmitKey != "Enter" {
		t.Errorf("injection = %+v, want bracketed paste + Enter", inj)
	}
	if inj.AcceptsInputDuringTurn != ActiveTurnQueued {
		t.Errorf("during-turn behaviour = %v, want the tty's buffered queue", inj.AcceptsInputDuringTurn)
	}
	if inj.DiscardsInputDuringModal {
		t.Error("a shell has no modal surface; nothing discards input")
	}
}

func TestShellIdentityContractIsNoneNotUnavailable(t *testing.T) {
	d := mustShell(t)

	if d.IdentityContract() != IdentityNone {
		t.Fatalf("IdentityContract = %v, want IdentityNone", d.IdentityContract())
	}
	if got := d.IdentityContract().String(); !strings.Contains(got, "none") {
		t.Errorf("IdentityContract.String() = %q, want the none-by-contract form", got)
	}
	// Supported by contract, so Availability is nil — distinct from
	// IdentityUnavailable, which marks a Phase 0 failure.
	if err := d.Availability(); err != nil {
		t.Errorf("Availability = %v, want nil for an identity-none provider", err)
	}
}

// The prompt has to survive bash's own startup files. Debian's
// /etc/bash.bashrc and ~/.bashrc both assign PS1 unconditionally and both
// run after the pane environment is applied, so an exported PS1 is
// overwritten before the first prompt is drawn. PROMPT_COMMAND runs after
// them, which is why it is the mechanism.
func TestShellPromptSurvivesStartupFiles(t *testing.T) {
	d, err := NewShell("/bin/bash")
	if err != nil {
		t.Fatalf("NewShell: %v", err)
	}
	env := d.Environment()
	if len(env) == 0 {
		t.Fatal("Environment() is empty, want the prompt")
	}
	if !strings.HasPrefix(env[0], "PROMPT_COMMAND=") {
		t.Errorf("Environment() = %q; a bare PS1= is overwritten by the rc files", env[0])
	}
	if !strings.Contains(env[0], `PS1=`) {
		t.Errorf("Environment() = %q, want it to assign PS1", env[0])
	}
	if env[0] != `PROMPT_COMMAND=PS1="\w \$ "` {
		t.Errorf("Environment() = %q, want exact directory-marker spacing", env[0])
	}

	// bash EXECUTES PROMPT_COMMAND before every prompt, so inheriting one
	// from the service environment would be arbitrary execution in the
	// user's shell. Only the literal value above may ever be set.
	for _, name := range d.PermittedEnvironment() {
		if name == "PROMPT_COMMAND" {
			t.Error("PROMPT_COMMAND must not be inheritable: bash runs it as a command each prompt")
		}
	}
}

func TestShellEnvironment(t *testing.T) {
	for _, tc := range []struct {
		goos string
		want []string
	}{
		{"linux", []string{`PROMPT_COMMAND=PS1="\w \$ "`}},
		{"darwin", []string{`PROMPT_COMMAND=PS1="\w \$ "`, "BASH_SILENCE_DEPRECATION_WARNING=1"}},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			if got := shellEnvironment(tc.goos); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("shellEnvironment(%q) = %q, want %q", tc.goos, got, tc.want)
			}
			if runtime.GOOS == tc.goos {
				if got := mustShell(t).Environment(); !reflect.DeepEqual(got, tc.want) {
					t.Errorf("Environment() = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

func TestShellPromptSpacingInPTY(t *testing.T) {
	d, err := NewShell("/bin/bash")
	if err != nil {
		t.Fatalf("NewShell: %v", err)
	}
	home := t.TempDir()
	if err := os.WriteFile(home+"/.bashrc", []byte("PS1='overwritten> '\n"), 0o600); err != nil {
		t.Fatalf("write bashrc: %v", err)
	}
	cmd := exec.Command("/bin/bash", "--noprofile", "-i")
	cmd.Dir = home
	cmd.Env = append([]string{"HOME=" + home, "PATH=/usr/bin:/bin", "TERM=dumb"}, d.Environment()...)
	terminal, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("start bash pty: %v", err)
	}
	defer terminal.Close()
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	_ = terminal.SetReadDeadline(time.Now().Add(30 * time.Second))
	var output strings.Builder
	buffer := make([]byte, 256)
	for !strings.Contains(output.String(), "~ $ ") {
		n, readErr := terminal.Read(buffer)
		if n > 0 {
			output.Write(buffer[:n])
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			t.Fatalf("read bash prompt: %v; output = %q", readErr, output.String())
		}
	}
	if !strings.Contains(output.String(), "~ $ ") {
		t.Fatalf("prompt = %q, want directory, space, marker, space", output.String())
	}
	if _, err := io.WriteString(terminal, "exit\n"); err != nil {
		t.Fatalf("exit bash: %v", err)
	}
	_ = terminal.SetReadDeadline(time.Time{})
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait bash: %v", err)
	}
}
