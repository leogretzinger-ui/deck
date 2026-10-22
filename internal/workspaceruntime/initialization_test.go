//go:build linux

package workspaceruntime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func (f *fixture) reboot(boot string) {
	f.t.Helper()
	if err := f.c.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.facts.BootID = boot
	f.facts.Mounts = f.facts.Mounts[:1]
	f.app = false
	// The fake mount uses an ordinary temporary directory. A real unmount hides
	// these synthetic empty stores and exposes the empty underlying mountpoint.
	for _, name := range []string{"data", "home", "projects", "tmp", "gh"} {
		if err := os.Remove(filepath.Join(f.cfg.MountPoint, name)); err != nil && !os.IsNotExist(err) {
			f.t.Fatal(err)
		}
	}
	f.open()
}

func TestInitializationReopensPinnedBackingAcrossRebootsWithoutInitializingAgain(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	secret := bytes.Repeat([]byte{9}, 32)
	first, err := f.c.Initialize(ctx, "original", secret)
	if err != nil || first.Status != "succeeded" {
		t.Fatal(first, err)
	}
	if _, err := f.c.Start(ctx, "original-start"); err != nil {
		t.Fatal(err)
	}
	f.reboot("second-boot")
	before := len(f.mutations())
	observed, err := f.c.Initialization(ctx, "original")
	if err != nil || !observed.State.Locked || len(f.mutations()) != before {
		t.Fatal("restart observation changed native effects", observed, err)
	}
	reopened, err := f.c.ReopenInitialization(ctx, "original", secret)
	if err != nil || reopened.Kind != "unlock" || reopened.ID == "original" || reopened.Status != "succeeded" || reopened.State.Storage != "open" {
		t.Fatal("pinned initialization was not reopened", reopened, err)
	}
	if err := f.c.Close(); err != nil {
		t.Fatal(err)
	}
	f.open()
	observed, err = f.c.Initialization(ctx, "original")
	if err != nil || observed.ID != reopened.ID || observed.State.Storage != "open" {
		t.Fatal("reopen relation lost across gateway restart", observed, err)
	}
	before = len(f.mutations())
	repeated, err := f.c.ReopenInitialization(ctx, "original", secret)
	if err != nil || repeated.ID != reopened.ID || len(f.mutations()) != before {
		t.Fatal("reopen replayed an already observed mount", repeated, err)
	}
	if _, err := f.c.Start(ctx, "reopened-start"); err != nil {
		t.Fatal(err)
	}
	f.reboot("third-boot")
	next, err := f.c.ReopenInitialization(ctx, "original", secret)
	if err != nil || next.ID == reopened.ID || next.ID == first.ID || next.Status != "succeeded" {
		t.Fatal("later reboot reused a historical mount operation", next, err)
	}
	initializations := 0
	for _, call := range f.mutations() {
		if slices.Contains(call.args, "-init") {
			initializations++
		}
	}
	if initializations != 1 {
		t.Fatal("recovery initialized replacement storage")
	}
}

func TestInitializationReopenRefusesChangedUnknownOrUncertainBacking(t *testing.T) {
	for _, fault := range []string{"changed-config", "unknown-processes", "unfinished-start", "wrong-operation"} {
		t.Run(fault, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			secret := bytes.Repeat([]byte{8}, 32)
			if _, err := f.c.Initialize(ctx, "original", secret); err != nil {
				t.Fatal(err)
			}
			if fault == "unfinished-start" {
				f.health = "starting"
				if _, err := f.c.Start(ctx, "unfinished"); err != nil {
					t.Fatal(err)
				}
			}
			f.reboot("second-boot")
			id := "original"
			switch fault {
			case "changed-config":
				if err := os.WriteFile(filepath.Join(f.cfg.CipherDir, "gocryptfs.conf"), []byte("different synthetic encrypted metadata"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unknown-processes":
				f.c.closedProcesses = func(context.Context) error { return ErrUnavailable }
			case "wrong-operation":
				id = "missing"
			}
			before := len(f.mutations())
			if _, err := f.c.ReopenInitialization(ctx, id, secret); err == nil {
				t.Fatal("unverified backing was reopened")
			}
			if len(f.mutations()) != before {
				t.Fatal("rejected reopen dispatched a command")
			}
		})
	}
}

func TestUncertainReopenNeverRepeatsTheMountCommand(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	secret := bytes.Repeat([]byte{8}, 32)
	if _, err := f.c.Initialize(ctx, "original", secret); err != nil {
		t.Fatal(err)
	}
	f.reboot("second-boot")
	f.failMount = errors.New("synthetic mount failure")
	first, err := f.c.ReopenInitialization(ctx, "original", secret)
	if err == nil || first.Status != "uncertain" {
		t.Fatal("failed reopen was confirmed", first, err)
	}
	before := len(f.mutations())
	second, _ := f.c.ReopenInitialization(ctx, "original", secret)
	if second.ID != first.ID || second.Status != "uncertain" || len(f.mutations()) != before {
		t.Fatal("uncertain reopen replayed its command", second)
	}
}

func TestReopenRelationSurvivesInterruptionBeforeNativeIntent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	secret := bytes.Repeat([]byte{8}, 32)
	if _, err := f.c.Initialize(ctx, "original", secret); err != nil {
		t.Fatal(err)
	}
	f.reboot("second-boot")
	if _, err := f.c.db.Exec(`CREATE TRIGGER refuse_reopen BEFORE INSERT ON operations WHEN NEW.kind='unlock' BEGIN SELECT RAISE(ABORT,'synthetic journal failure'); END`); err != nil {
		t.Fatal(err)
	}
	before := len(f.mutations())
	if _, err := f.c.ReopenInitialization(ctx, "original", secret); !errors.Is(err, ErrJournal) {
		t.Fatal("journal fault did not stop dispatch", err)
	}
	interrupted, err := f.c.Initialization(ctx, "original")
	if err != nil || interrupted.ID == "original" || interrupted.Stage != "prepared" || len(f.mutations()) != before {
		t.Fatal("prepared relation lost or dispatched without a native journal", interrupted, err)
	}
	if _, err := f.c.db.Exec(`DROP TRIGGER refuse_reopen`); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Close(); err != nil {
		t.Fatal(err)
	}
	f.open()
	resumed, err := f.c.ReopenInitialization(ctx, "original", secret)
	if err != nil || resumed.ID != interrupted.ID || resumed.Status != "succeeded" || len(f.mutations()) != before+1 {
		t.Fatal("proven unstarted reopen did not resume the same intent", resumed, err)
	}
}
