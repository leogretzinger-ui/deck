//go:build linux

package workspaceruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func processStat(pid, parent int, flags, start uint64) string {
	f := []string{"S", strconv.Itoa(parent), "0", "0", "0", "0", strconv.FormatUint(flags, 10)}
	for len(f) < 19 {
		f = append(f, "0")
	}
	f = append(f, strconv.FormatUint(start, 10))
	return fmt.Sprintf("%d (arbitrary ) name) %s\n", pid, strings.Join(f, " "))
}

func TestProtectedSupervisorObservation(t *testing.T) {
	for _, condition := range []string{"manager", "fork-child", "adopted-orphan", "exec-child", "grandchild", "wrong-parent", "unknown-pid", "query-error", "query-zero", "changed-main-pid", "changed-manager-start", "changed-child-start", "child-exec-race", "late-child-exec", "late-child-reuse", "late-child-reparent", "late-child-exit", "alias-manager", "alias-child", "alias-adopted-orphan", "unreadable-mounts", "malformed-mounts", "non-eacces", "malformed-stat"} {
		t.Run(condition, func(t *testing.T) {
			root := t.TempDir()
			proc := filepath.Join(root, "proc")
			binary := filepath.Join(root, "gocryptfs")
			mustWrite := func(path, value string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			mustWrite(binary, "")
			mounts := "1 0 1:1 / / rw - ext4 /dev/synthetic rw\n"
			for _, pid := range []int{123, 124} {
				p := filepath.Join(proc, strconv.Itoa(pid))
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
				mustWrite(filepath.Join(p, "mountinfo"), mounts)
			}
			mustWrite(filepath.Join(proc, "123/stat"), processStat(123, 1, 0, 100))
			mustWrite(filepath.Join(proc, "124/stat"), processStat(124, 123, 64, 200))
			switch condition {
			case "exec-child":
				mustWrite(filepath.Join(proc, "124/stat"), processStat(124, 123, 0, 200))
			case "grandchild":
				mustWrite(filepath.Join(proc, "124/stat"), processStat(124, 125, 64, 200))
			case "adopted-orphan":
				mustWrite(filepath.Join(proc, "124/stat"), processStat(124, 125, 64, 200))
			case "wrong-parent":
				mustWrite(filepath.Join(proc, "123/stat"), processStat(123, 99, 0, 100))
			case "alias-manager":
				mustWrite(filepath.Join(proc, "123/mountinfo"), mounts+"2 1 0:40 / /alias rw - fuse.gocryptfs /cipher rw\n")
			case "alias-child":
				mustWrite(filepath.Join(proc, "124/mountinfo"), mounts+"2 1 0:40 / /alias rw - fuse.gocryptfs /cipher rw\n")
			case "alias-adopted-orphan":
				mustWrite(filepath.Join(proc, "124/stat"), processStat(124, 125, 64, 200))
				mustWrite(filepath.Join(proc, "124/mountinfo"), mounts+"2 1 0:40 / /alias rw - fuse.gocryptfs /cipher rw\n")
			case "unreadable-mounts":
				if err := os.Remove(filepath.Join(proc, "124/mountinfo")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(proc, "124/mountinfo"), 0700); err != nil {
					t.Fatal(err)
				}
			case "malformed-mounts":
				mustWrite(filepath.Join(proc, "124/mountinfo"), "invalid\n")
			case "malformed-stat":
				mustWrite(filepath.Join(proc, "124/stat"), "invalid\n")
			}
			queries := 0
			query := func(context.Context, int) (int, error) {
				queries++
				if queries == 1 && (condition == "adopted-orphan" || condition == "alias-adopted-orphan") {
					// Adoption occurs while observing the manager, before PID 124
					// is scanned. Its current parent changes, but its fork flag
					// does not: original ancestry cannot be inferred afterward.
					mustWrite(filepath.Join(proc, "124/stat"), processStat(124, 123, 64, 200))
				}
				if queries == 3 {
					switch condition {
					case "late-child-exec":
						mustWrite(filepath.Join(proc, "124/stat"), processStat(124, 123, 0, 200))
					case "late-child-reuse":
						mustWrite(filepath.Join(proc, "124/stat"), processStat(124, 123, 64, 201))
					case "late-child-reparent":
						mustWrite(filepath.Join(proc, "124/stat"), processStat(124, 1, 64, 200))
					case "late-child-exit":
						if err := os.Remove(filepath.Join(proc, "124/stat")); err != nil {
							t.Fatal(err)
						}
					}
				}
				switch condition {
				case "query-error":
					return 0, errors.New("unavailable supervisor")
				case "query-zero":
					return 0, nil
				case "unknown-pid":
					return 999, nil
				case "changed-main-pid":
					if queries >= 3 {
						return 999, nil
					}
				case "changed-manager-start":
					if queries >= 3 {
						mustWrite(filepath.Join(proc, "123/stat"), processStat(123, 1, 0, 300))
					}
				}
				return 123, nil
			}
			readlink := func(path string) (string, error) {
				if strings.Contains(path, "/124/") {
					switch condition {
					case "manager":
						return "/unrelated", nil
					case "changed-child-start":
						mustWrite(filepath.Join(proc, "124/stat"), processStat(124, 123, 64, 201))
					case "child-exec-race":
						mustWrite(filepath.Join(proc, "124/stat"), processStat(124, 123, 0, 200))
					case "non-eacces":
						return "", unix.EIO
					}
				}
				return "", unix.EACCES
			}
			c := &Controller{cfg: Config{OwnerUID: os.Geteuid(), GocryptfsPath: binary, CipherDir: "/cipher", MountPoint: "/open"}}
			err := c.observeClosedProcessesWith(context.Background(), proc, readlink, query)
			if condition == "manager" || condition == "fork-child" || condition == "adopted-orphan" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			want := ErrUnavailable
			if strings.HasPrefix(condition, "alias-") {
				want = ErrUnsafeStorage
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v; want %v", err, want)
			}
		})
	}
}

func TestManagerPIDParsing(t *testing.T) {
	for _, s := range []string{"", "0\n", "1\n", "-2\n", "123\n456\n", "123\n\n", "MainPID=123\n", " 123\n", "123 \n", "99999999999999999999999\n"} {
		if _, err := parseManagerPID(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	if p, err := parseManagerPID("123\n"); p != 123 || err != nil {
		t.Fatalf("valid PID: %d %v", p, err)
	}
}

func TestSupervisorMembership(t *testing.T) {
	m := processSnapshot{pid: 123, parent: 1, uid: 1006, start: 100}
	s := protectedSupervisor{manager: m}
	for _, p := range []processSnapshot{
		{pid: 124, parent: 123, uid: 1007, start: 200, forkWithoutExec: true},
		{pid: 124, parent: 123, uid: 1006, start: 99, forkWithoutExec: true},
		{pid: 124, parent: 123, uid: 1006, start: 200},
		{pid: 123, parent: 1, uid: 1006, start: 101},
	} {
		if s.contains(p) {
			t.Fatalf("unattested process admitted: %+v", p)
		}
	}
}
