//go:build linux

package workspaceruntime

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/noisefloor-oss/deck/internal/workspacemigrate"
)

func migrationFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	if err := f.c.Close(); err != nil {
		t.Fatal(err)
	}
	f.c = nil
	f.cfg.ControlDir = t.TempDir()
	if err := os.Chmod(f.cfg.ControlDir, 0700); err != nil {
		t.Fatal(err)
	}
	snapshot := t.TempDir()
	if err := os.Chmod(snapshot, 0700); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(snapshot, "tree")
	if err := os.Mkdir(tree, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"data", "home", "projects", "gh"} {
		if err := os.Mkdir(filepath.Join(tree, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tree, "home", "opaque"), []byte("synthetic provider bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(tree)
	if err != nil {
		t.Fatal(err)
	}
	m, err := workspacemigrate.ManifestFor(t.Context(), root)
	_ = root.Close()
	if err != nil {
		t.Fatal(err)
	}
	digest, raw, err := workspacemigrate.Digest(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	f.cfg.Migration = &MigrationConfig{SnapshotDir: snapshot, ManifestSHA256: digest}
	f.open()
	return f
}

func TestMigrationBlocksStartupUntilVerifiedImport(t *testing.T) {
	f := migrationFixture(t)
	if _, err := f.c.Initialize(t.Context(), "enroll", bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	before := len(f.mutations())
	if _, err := f.c.Start(t.Context(), "start"); !errors.Is(err, ErrMigrationRequired) {
		t.Fatal(err)
	}
	if len(f.mutations()) != before {
		t.Fatal("migration dispatched a workload before import")
	}
	if _, err := f.c.Operation(t.Context(), "start"); !errors.Is(err, ErrNotFound) {
		t.Fatal("refused start consumed its operation ID")
	}
	if err := f.c.Import(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Close(); err != nil {
		t.Fatal(err)
	}
	f.c = nil
	f.open()
	if err := os.RemoveAll(f.cfg.Migration.SnapshotDir); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Import(t.Context()); err != nil {
		t.Fatal("completed import required plaintext snapshot", err)
	}
	if _, err := f.c.Start(t.Context(), "start"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(f.cfg.MountPoint, "home", "opaque"))
	if err != nil || string(got) != "synthetic provider bytes" {
		t.Fatal("import lost content")
	}
}

func TestMigrationRefusesClosedMountAndWrongCipher(t *testing.T) {
	f := migrationFixture(t)
	if _, err := f.c.Initialize(t.Context(), "enroll", bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	mounted := f.facts.Mounts
	f.facts.Mounts = mounted[:1]
	if err := f.c.Import(t.Context()); !errors.Is(err, ErrPrecondition) {
		t.Fatal("closed mount imported", err)
	}
	names, err := os.ReadDir(f.cfg.MountPoint)
	if err != nil || len(names) != 0 {
		t.Fatal("closed mount received plaintext")
	}
	f.facts.Mounts = mounted
	if err := f.c.Import(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.cfg.CipherDir, "gocryptfs.conf"), []byte("other volume"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Start(t.Context(), "start"); !errors.Is(err, ErrUnsafeStorage) {
		t.Fatal("receipt admitted another volume", err)
	}
}

func TestMigrationConfigCannotChangeAfterPinning(t *testing.T) {
	f := migrationFixture(t)
	if err := f.c.Close(); err != nil {
		t.Fatal(err)
	}
	f.c = nil
	f.cfg.Migration.ManifestSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if c, err := open(f.cfg, f, func() (kernelFacts, error) { return f.facts, nil }); !errors.Is(err, ErrConfiguration) {
		if c != nil {
			_ = c.Close()
		}
		t.Fatal("migration digest changed")
	}
}

func TestImportDescriptorCannotFallThroughToReplacementDirectory(t *testing.T) {
	f := migrationFixture(t)
	if _, err := f.c.Initialize(t.Context(), "enroll", bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	pinned, err := f.c.importRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	old := f.cfg.MountPoint + "-detached"
	if err := os.Rename(f.cfg.MountPoint, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.cfg.MountPoint, 0700); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenRoot(f.cfg.Migration.SnapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := workspacemigrate.Import(t.Context(), source, pinned, f.cfg.Migration.ManifestSHA256); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(f.cfg.MountPoint)
	if err != nil || len(entries) != 0 {
		t.Fatal("copy fell through to replacement mountpoint")
	}
	if _, err := os.Stat(filepath.Join(old, "home", "opaque")); err != nil {
		t.Fatal("copy did not stay on opened root")
	}
}

func TestImportJournalRepairRechecksPublishedStores(t *testing.T) {
	for _, mutation := range []string{"intact", "missing", "corrupt", "no-manifest"} {
		t.Run(mutation, func(t *testing.T) {
			f := migrationFixture(t)
			if _, err := f.c.Initialize(t.Context(), "enroll", bytes.Repeat([]byte{1}, 32)); err != nil {
				t.Fatal(err)
			}
			// Simulate a process exit after encrypted publication, before the native
			// control journal commits. No application has been admitted in this window.
			source, err := os.OpenRoot(f.cfg.Migration.SnapshotDir)
			if err != nil {
				t.Fatal(err)
			}
			target, err := f.c.importRoot()
			if err != nil {
				t.Fatal(err)
			}
			err = workspacemigrate.Import(t.Context(), source, target, f.cfg.Migration.ManifestSHA256)
			_ = source.Close()
			_ = target.Close()
			if err != nil {
				t.Fatal(err)
			}
			if err := f.c.migrationReady(t.Context()); !errors.Is(err, ErrMigrationRequired) {
				t.Fatal("publication already authorized start", err)
			}
			switch mutation {
			case "missing":
				if err := os.RemoveAll(filepath.Join(f.cfg.MountPoint, "home")); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(filepath.Join(f.cfg.MountPoint, "home", "opaque"), []byte("corrupt published data"), 0600); err != nil {
					t.Fatal(err)
				}
			case "no-manifest":
				if err := os.RemoveAll(f.cfg.Migration.SnapshotDir); err != nil {
					t.Fatal(err)
				}
			}
			err = f.c.Import(t.Context())
			if mutation == "intact" {
				if err != nil {
					t.Fatal("intact publication did not repair journal", err)
				}
				return
			}
			if !errors.Is(err, workspacemigrate.ErrSnapshot) {
				t.Fatal("damaged publication authorized journal repair", err)
			}
			if _, err := f.c.Start(t.Context(), "start"); !errors.Is(err, ErrMigrationRequired) {
				t.Fatal("damaged publication enabled startup", err)
			}
		})
	}
}

func TestCompletedNativeImportPreservesLaterOwnerWrites(t *testing.T) {
	f := migrationFixture(t)
	if _, err := f.c.Initialize(t.Context(), "enroll", bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Import(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.cfg.MountPoint, "home", "opaque")
	if err := os.WriteFile(path, []byte("owner changed after completed import"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(f.cfg.Migration.SnapshotDir); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Close(); err != nil {
		t.Fatal(err)
	}
	f.c = nil
	f.open()
	if err := f.c.Import(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "owner changed after completed import" {
		t.Fatal("completed import rewrote owner data")
	}
}
