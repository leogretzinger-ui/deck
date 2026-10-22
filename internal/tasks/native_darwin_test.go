//go:build darwin

package tasks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noisefloor-oss/deck/internal/tmux"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == nativeTaskExecCommand {
		if err := RunNativeTaskExec(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "native Tasks execution refused")
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func TestNativeTaskCommandRetainsDirectoryAcrossRootSwap(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "project")
	saved := path + ".anchored"
	outside := filepath.Join(parent, "outside")
	for _, dir := range []string{path, outside} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	root, err := openTaskRoot(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := newTaskCommand(context.Background(), "/bin/sh", root,
		"-c", "test ! -e /dev/fd/3 && printf anchored > created.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("anchored command: %v: %s", err, out)
	}
	assertFileBody(t, filepath.Join(saved, "created.txt"), "anchored")
	assertFileMissing(t, filepath.Join(outside, "created.txt"))
	if after, err := os.Getwd(); err != nil || before != after {
		t.Fatalf("parent cwd changed: %q -> %q, %v", before, after, err)
	}
}

func TestNativeTaskRootRefusesSymlinkComponents(t *testing.T) {
	parent := t.TempDir()
	actual := filepath.Join(parent, "actual")
	link := filepath.Join(parent, "link")
	if err := os.MkdirAll(filepath.Join(actual, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, filepath.Join(link, "child"), "relative", actual + "/../actual"} {
		root, err := openTaskRoot(path, nil)
		if root != nil {
			root.Close()
		}
		if !errors.Is(err, ErrUnsafeTree) {
			t.Fatalf("accepted unsafe root %q: %v", path, err)
		}
	}
}

func TestNativeTaskExecRefusesInvalidDirectoryOrExecutable(t *testing.T) {
	regular, err := os.CreateTemp(t.TempDir(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{}, {"relative"}, {"/bin/../bin/true"}, {"/usr/bin/true"}} {
		cmd := exec.Command(self, append([]string{nativeTaskExecCommand}, args...)...)
		cmd.ExtraFiles = []*os.File{regular}
		if err := cmd.Run(); err == nil {
			t.Fatalf("accepted invalid helper invocation: %v", args)
		}
	}
	cmd := exec.Command(self, nativeTaskExecCommand, "/usr/bin/true")
	if err := cmd.Run(); err == nil {
		t.Fatal("accepted missing directory descriptor")
	}
}

func TestNativeGenerationMissingEngineRefusesBeforeRunOrSession(t *testing.T) {
	e := newJobEnv(t, "/nonexistent/noise")
	if _, err := e.runner.Generate(context.Background(), e.project.ID, "PRD.md", 0, ""); err == nil || errors.Is(err, ErrGenerationUnsupported) {
		t.Fatalf("generation refusal: %v", err)
	}
	runs, err := e.runs.ListRuns(context.Background(), e.project.ID, 20)
	if err != nil || len(runs) != 0 {
		t.Fatalf("generation created runs: %v, %v", runs, err)
	}
	sessions, err := e.tmux.ListSessions(context.Background())
	if (err != nil && !errors.Is(err, tmux.ErrNoServer)) || len(sessions) != 0 {
		t.Fatalf("generation created sessions: %v, %v", sessions, err)
	}
}

func TestNativeTaskBackendHelperRoute(t *testing.T) {
	binaries := buildTaskBinaries(t)
	backend := filepath.Join(t.TempDir(), "noise-deck")
	build := exec.Command("go", "build", "-o", backend, "../../cmd/noisedeck")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build backend: %v: %s", err, out)
	}
	path := gitProject(t)
	root, err := openTaskRoot(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, args := range [][]string{{"tasks", "init", "-json"}, {"tasks", "list", "--json"}} {
		cmd := exec.Command(backend, append([]string{nativeTaskExecCommand, binaries.noise}, args...)...)
		cmd.Dir = "/"
		cmd.ExtraFiles = []*os.File{root.anchor}
		cmd.Env = withoutEnvironment(os.Environ(), "NOISE_PROJECT_DIR")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("backend helper %v: %v: %s", args, err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(path, ".git", "noise-tasks", "tasks.sqlite")); err != nil {
		t.Fatal(err)
	}
}

func TestNativeGenerationBackendHelperRoute(t *testing.T) {
	backend := filepath.Join(t.TempDir(), "noise-deck")
	build := exec.Command("go", "build", "-o", backend, "../../cmd/noisedeck")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build backend: %v: %s", err, out)
	}
	e := newJobEnv(t, writeStub(t, "cat \"$4\" || exit 41\nprintf generated > .generated-marker\n"))
	e.runner.handoffPrefix = []string{backend, generationHandoffCommand}
	run, err := e.runner.Generate(context.Background(), e.project.ID, "PRD.md", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	final := e.awaitRun(t, run.ID)
	if final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("generation failed: %+v", final)
	}
	if final.Argv[4] != "/dev/fd/10" || final.Stdout == nil || !strings.Contains(*final.Stdout, "# prd") {
		t.Fatalf("generation did not read the PRD descriptor: %+v", final)
	}
	assertFileBody(t, filepath.Join(e.project.CanonicalPath, ".generated-marker"), "generated")
}
