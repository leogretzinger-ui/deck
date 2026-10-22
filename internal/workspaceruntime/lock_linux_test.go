//go:build linux

package workspaceruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestClosedProcessObserver(t *testing.T) {
	for _, condition := range []string{"other-program", "other-volume", "daemon", "symlinked-binary", "alias", "unreadable-namespace", "malformed-namespace"} {
		t.Run(condition, func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "actual-gocryptfs")
			if err := os.WriteFile(binary, nil, 0700); err != nil {
				t.Fatal(err)
			}
			configuredBinary := binary
			if condition == "symlinked-binary" {
				configuredBinary = filepath.Join(root, "gocryptfs")
				if err := os.Symlink(binary, configuredBinary); err != nil {
					t.Fatal(err)
				}
			}
			procRoot := filepath.Join(root, "proc")
			pidRoot := filepath.Join(procRoot, "123")
			if err := os.MkdirAll(pidRoot, 0700); err != nil {
				t.Fatal(err)
			}
			c := &Controller{cfg: Config{OwnerUID: os.Geteuid(), GocryptfsPath: configuredBinary, CipherDir: "/synthetic-cipher", MountPoint: "/synthetic-plain"}}
			executable := binary
			if condition == "other-program" {
				executable = "/bin/unrelated-synthetic-program"
			}
			if err := os.Symlink(executable, filepath.Join(pidRoot, "exe")); err != nil {
				t.Fatal(err)
			}
			mounts := "1 0 1:1 / / rw - ext4 /dev/synthetic rw\n"
			if condition == "alias" {
				mounts += "2 1 0:40 / /elsewhere rw - fuse.gocryptfs /synthetic-cipher rw,user_id=1000\n"
			}
			if condition == "malformed-namespace" {
				mounts = "not mountinfo\n"
			}
			if err := os.WriteFile(filepath.Join(pidRoot, "mountinfo"), []byte(mounts), 0600); err != nil {
				t.Fatal(err)
			}
			if condition == "unreadable-namespace" {
				if err := os.Remove(filepath.Join(pidRoot, "mountinfo")); err != nil {
					t.Fatal(err)
				}
				// A directory at the kernel file location deterministically fails its
				// read; no assumption about tests running with or without root privilege.
				if err := os.Mkdir(filepath.Join(pidRoot, "mountinfo"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if condition != "other-program" {
				args := "gocryptfs\x00/synthetic-cipher\x00/synthetic-plain\x00"
				if condition == "other-volume" {
					args = "gocryptfs\x00/unrelated-cipher\x00/unrelated-plain\x00"
				}
				if err := os.WriteFile(filepath.Join(pidRoot, "cmdline"), []byte(args), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := c.observeClosedProcessesAt(context.Background(), procRoot)
			switch condition {
			case "other-program", "other-volume":
				if err != nil {
					t.Fatalf("unrelated process prevented closure: %v", err)
				}
			case "unreadable-namespace", "malformed-namespace":
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("uncertain namespace became absence: %v", err)
				}
			default:
				if !errors.Is(err, ErrUnsafeStorage) {
					t.Fatalf("retained daemon or alias became absence: %v", err)
				}
			}
		})
	}
}
