package boot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/sealedstate"
)

// A qualified own-host layout as the container sees it, plus a separate
// plaintext mount at /srv/other that no store covers.
const sealedFixtureMountInfo = `1 0 0:90 / / ro,relatime - overlay overlay rw
10 1 253:0 /vol/data /data rw,relatime - ext4 /dev/mapper/cryptroot rw
11 1 253:0 /vol/home /home/agent rw,relatime - ext4 /dev/mapper/cryptroot rw
12 1 253:0 /srv/deck/projects /workspace rw,relatime - ext4 /dev/mapper/cryptroot rw
13 1 0:50 / /tmp rw,nosuid,nodev - tmpfs tmpfs rw,size=2g
14 1 8:1 /other /srv/other rw,relatime - ext4 /dev/sda1 rw
`

func sealedFixtureFacts(t *testing.T) func(string) (sealedstate.Facts, error) {
	t.Helper()
	return func(string) (sealedstate.Facts, error) {
		mounts, err := sealedstate.ParseMountInfo(strings.NewReader(sealedFixtureMountInfo))
		if err != nil {
			return sealedstate.Facts{}, err
		}
		facts := sealedstate.Facts{
			Mounts:      mounts,
			BootID:      "boot-1",
			ContainerID: "container-1",
			UID:         os.Getuid(),
			DMUUID: func(device string) string {
				if device == "253:0" {
					return "CRYPT-LUKS2-0000-cryptroot"
				}
				return ""
			},
			FuseAllowSysAdminAccess: "N",
			ResumeDevice:            "0:0",
			Report: &sealedstate.Report{
				BootID: "boot-1", ContainerID: "container-1", Profile: sealedstate.ProfileOwn,
				Stores: map[string]sealedstate.ReportStore{
					"data":     {Path: "/data", MountID: 10, Device: "253:0", Root: "/vol/data", Backing: sealedstate.BackingDMCrypt},
					"home":     {Path: "/home/agent", MountID: 11, Device: "253:0", Root: "/vol/home", Backing: sealedstate.BackingDMCrypt},
					"projects": {Path: "/workspace", MountID: 12, Device: "253:0", Root: "/srv/deck/projects", Backing: sealedstate.BackingDMCrypt},
					"tmp":      {Path: "/tmp", MountID: 13, Device: "0:50", Root: "/", Backing: sealedstate.BackingTmpfs},
				},
			},
		}
		return facts, nil
	}
}

// With every store qualified but the database configured on a plaintext
// mount outside them, boot refuses before it creates the database directory,
// the installation mode file, or the database itself.
func TestUpRefusesAConfiguredDestinationOutsideTheSealedStores(t *testing.T) {
	cfg := testConfig(t)
	cfg.Sealed.RequireEncryptedState = true
	cfg.Sealed.Profile = string(sealedstate.ProfileOwn)
	// The test's temp directory is not one of the fixture's sealed stores;
	// point the database at a path the fixture shows as plaintext.
	cfg.Database.Path = "/srv/other/noisedeck/control.db"
	cfg.Database.LockPath = "/srv/other/noisedeck/backend.lock"
	cfg.Projects.AllowedRoots = []string{"/workspace"}

	_, err := Up(context.Background(), cfg, zerolog.Nop(), WithSealedFacts(sealedFixtureFacts(t)))
	if err == nil {
		t.Fatal("Up accepted a database outside the sealed stores")
	}
	if !strings.Contains(err.Error(), "configured database (/srv/other/noisedeck) is outside every sealed store") {
		t.Fatalf("Up error = %v, want the stray database named", err)
	}
	if _, statErr := os.Stat(filepath.Dir(cfg.Database.Path)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Up created %s before refusing (stat: %v)", filepath.Dir(cfg.Database.Path), statErr)
	}
}

// The same layout with the database inside a sealed store passes the gate;
// what fails afterwards is the ordinary boot against a path that does not
// exist here, never the sealed check.
func TestUpAcceptsDestinationsInsideTheSealedStores(t *testing.T) {
	cfg := testConfig(t)
	cfg.Sealed.RequireEncryptedState = true
	cfg.Sealed.Profile = string(sealedstate.ProfileOwn)
	cfg.Database.Path = "/data/noisedeck/control.db"
	cfg.Database.LockPath = "/data/noisedeck/backend.lock"
	cfg.Projects.AllowedRoots = []string{"/workspace"}
	results, _, err := SealedCheck(cfg, mustFacts(t, sealedFixtureFacts(t)), true)
	if err != nil {
		t.Fatalf("SealedCheck: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("results = %d, want the four stores", len(results))
	}
}

// A symlink from inside a sealed store to a plaintext mount is the same
// escape, and is resolved before the coverage check.
func TestSealedDestinationsResolveSymlinkEscapes(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.Database.Path = filepath.Join(link, "control.db")
	cfg.Database.LockPath = filepath.Join(link, "backend.lock")
	cfg.Server.UnixSocket = filepath.Join(link, "backend.sock")
	dest := SealedDestinations(cfg)
	resolved, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	if dest["database"] != resolved {
		t.Fatalf("database destination = %q, want the link resolved to %q", dest["database"], resolved)
	}
	if dest["gateway socket directory"] != resolved {
		t.Fatalf("gateway socket destination = %q, want the link resolved to %q", dest["gateway socket directory"], resolved)
	}
}

func TestSealedGateCoversPrivateSocketDirectory(t *testing.T) {
	cfg := testConfig(t)
	cfg.Sealed.RequireEncryptedState = true
	cfg.Sealed.Profile = string(sealedstate.ProfileOwn)
	cfg.Database.Path = "/data/noisedeck/control.db"
	cfg.Database.LockPath = "/data/noisedeck/backend.lock"
	cfg.Projects.AllowedRoots = []string{"/workspace"}
	cfg.Server.UnixSocket = "/srv/other/gateway/backend.sock"
	if _, _, err := SealedCheck(cfg, mustFacts(t, sealedFixtureFacts(t)), true); err == nil || !strings.Contains(err.Error(), "gateway socket directory") {
		t.Fatalf("unprotected socket directory accepted: %v", err)
	}
	cfg.Server.UnixSocket = "/data/gateway/backend.sock"
	if _, _, err := SealedCheck(cfg, mustFacts(t, sealedFixtureFacts(t)), true); err != nil {
		t.Fatalf("protected socket directory refused: %v", err)
	}
}

// Without the requirement the same layouts still draw a warning each, so the
// log previews what requiring encrypted state would refuse.
func TestSealedAdvisoryWarnsAboutDestinationsAndSymlinks(t *testing.T) {
	cfg := testConfig(t)
	cfg.Sealed.Profile = string(sealedstate.ProfileOwn)
	cfg.Database.Path = "/srv/other/noisedeck/control.db"
	cfg.Database.LockPath = "/srv/other/noisedeck/backend.lock"
	cfg.Projects.AllowedRoots = []string{"/workspace"}
	_, warnings, err := SealedCheck(cfg, mustFacts(t, sealedFixtureFacts(t)), false)
	if err != nil {
		t.Fatalf("advisory SealedCheck: %v", err)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "configured database (/srv/other/noisedeck) is outside every sealed store") {
		t.Fatalf("warnings = %q, want the stray database named", warnings)
	}

	dir := t.TempDir()
	link := filepath.Join(dir, "control.db")
	if err := os.Symlink(filepath.Join(dir, "elsewhere.db"), link); err != nil {
		t.Fatal(err)
	}
	cfg.Database.Path = link
	_, warnings, err = SealedCheck(cfg, mustFacts(t, sealedFixtureFacts(t)), false)
	if err != nil {
		t.Fatalf("advisory SealedCheck: %v", err)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "configured database file ("+link+") is a symlink") {
		t.Fatalf("warnings = %q, want the symlinked database file named", warnings)
	}

	var buf bytes.Buffer
	if err := verifySealedState(cfg, sealedFixtureFacts(t), zerolog.New(&buf)); err != nil {
		t.Fatalf("verifySealedState: %v", err)
	}
	if !strings.Contains(buf.String(), "is a symlink") {
		t.Fatalf("log = %s, want the symlink warning", buf.String())
	}
}

func mustFacts(t *testing.T, read func(string) (sealedstate.Facts, error)) sealedstate.Facts {
	t.Helper()
	facts, err := read("")
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

// A database file that is a symlink from inside a sealed store to an
// existing database on a plaintext mount is refused before the target is
// touched. The fixture's stores are the test's own directories, so the link
// and its target are real.
func TestUpRefusesASymlinkedDatabaseFile(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	other := filepath.Join(root, "other")
	for _, d := range []string{data, filepath.Join(root, "home"), filepath.Join(root, "projects"), filepath.Join(root, "tmp"), other} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(other, "control.db")
	if err := os.WriteFile(target, []byte("plaintext database on another mount"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(data, "control.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}

	stores := []sealedstate.Store{
		{Name: "data", Path: data},
		{Name: "home", Path: filepath.Join(root, "home")},
		{Name: "projects", Path: filepath.Join(root, "projects")},
		{Name: "tmp", Path: filepath.Join(root, "tmp")},
	}
	mountInfo := "1 0 0:90 / / ro,relatime - overlay overlay rw\n"
	report := &sealedstate.Report{BootID: "boot-1", ContainerID: "container-1", Profile: sealedstate.ProfileOwn, Stores: map[string]sealedstate.ReportStore{}}
	for i, s := range stores {
		id := 10 + i
		mountInfo += fmt.Sprintf("%d 1 253:0 /vol/%s %s rw,relatime - ext4 /dev/mapper/cryptroot rw\n", id, s.Name, s.Path)
		report.Stores[s.Name] = sealedstate.ReportStore{Path: s.Path, MountID: id, Device: "253:0", Root: "/vol/" + s.Name, Backing: sealedstate.BackingDMCrypt}
	}
	mountInfo += fmt.Sprintf("20 1 8:1 /other %s rw,relatime - ext4 /dev/sda1 rw\n", other)
	read := func(string) (sealedstate.Facts, error) {
		mounts, err := sealedstate.ParseMountInfo(strings.NewReader(mountInfo))
		if err != nil {
			return sealedstate.Facts{}, err
		}
		return sealedstate.Facts{
			Mounts: mounts, BootID: "boot-1", ContainerID: "container-1", UID: os.Getuid(),
			DMUUID: func(device string) string {
				if device == "253:0" {
					return "CRYPT-LUKS2-0000-cryptroot"
				}
				return ""
			},
			FuseAllowSysAdminAccess: "N", ResumeDevice: "0:0", Report: report,
		}, nil
	}
	previous := sealedStores
	sealedStores = func() []sealedstate.Store { return stores }
	t.Cleanup(func() { sealedStores = previous })

	cfg := testConfig(t)
	cfg.Sealed.RequireEncryptedState = true
	cfg.Sealed.Profile = string(sealedstate.ProfileOwn)
	cfg.Database.Path = link
	cfg.Database.LockPath = filepath.Join(data, "backend.lock")
	cfg.Projects.AllowedRoots = []string{filepath.Join(root, "projects")}

	_, upErr := Up(context.Background(), cfg, zerolog.Nop(), WithSealedFacts(read))
	if upErr == nil {
		t.Fatal("Up accepted a symlinked database file")
	}
	if !strings.Contains(upErr.Error(), "configured database file ("+link+") is a symlink") {
		t.Fatalf("Up error = %v, want the symlinked database file named by its path", upErr)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(upErr.Error(), "configured database file ("+resolvedTarget+") is outside every sealed store") {
		t.Fatalf("Up error = %v, want the resolved target %s named as outside the sealed stores", upErr, resolvedTarget)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("the symlink target was modified before the refusal")
	}
	if _, err := os.Stat(filepath.Join(data, "backend.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Up created the lock before refusing (stat: %v)", err)
	}
}
