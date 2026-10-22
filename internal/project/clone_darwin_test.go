//go:build darwin

package project

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNativeCloneExecRefusesRegularFileDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "internal-clone-exec-v1", "/usr/bin/true")
	cmd.ExtraFiles = []*os.File{file}
	if err := cmd.Run(); err == nil {
		t.Fatal("regular file accepted as the clone working directory")
	}
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "internal-clone-exec-v1" {
		if err := RunNativeCloneExec(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "native clone execution refused")
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}
