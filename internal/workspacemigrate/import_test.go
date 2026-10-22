package workspacemigrate

import (
	"errors"
	"os"
	"testing"
)

func snapshotFixture(t *testing.T) (*os.Root, string) {
	t.Helper()
	source, m := fixture(t)
	snapshot := destination(t)
	if err := snapshot.Mkdir("tree", 0700); err != nil {
		t.Fatal(err)
	}
	tree, err := snapshot.OpenRoot("tree")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tree.Close() }()
	if err := Copy(t.Context(), source, tree, m); err != nil {
		t.Fatal(err)
	}
	digest, raw, err := Digest(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.WriteFile("manifest.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	return snapshot, digest
}

func TestImportResumesEachPublicationBoundary(t *testing.T) {
	for _, phase := range []string{"copied", "published-data", "published-home", "published-projects", "published-gh"} {
		t.Run(phase, func(t *testing.T) {
			source, digest := snapshotFixture(t)
			target := destination(t)
			interrupted := errors.New("interrupted")
			err := importSnapshot(t.Context(), source, target, digest, func(got string) error {
				if got == phase {
					return interrupted
				}
				return nil
			})
			if !errors.Is(err, interrupted) {
				t.Fatalf("interruption: %v", err)
			}
			receipt, err := readReceipt(target)
			if err != nil || receipt.Phase == "complete" {
				t.Fatal("interrupted copy admitted the workload")
			}
			if err := Import(t.Context(), source, target, digest); err != nil {
				t.Fatal(err)
			}
			m, err := ReadManifest(source, digest)
			if err != nil {
				t.Fatal(err)
			}
			for _, store := range stores {
				if err := verifyStore(t.Context(), target, m, store); err != nil {
					t.Fatal(err)
				}
			}
			if err := Import(t.Context(), source, target, digest); err != nil {
				t.Fatal("completed publication did not verify", err)
			}
			if err := Import(t.Context(), nil, target, digest); !errors.Is(err, ErrSnapshot) {
				t.Fatal("receipt alone repaired missing native completion")
			}

		})
	}
}

func TestImportRefusesUnrelatedDataAndChangedSnapshot(t *testing.T) {
	for _, mutation := range []string{"target", "manifest", "source", "digest"} {
		t.Run(mutation, func(t *testing.T) {
			source, digest := snapshotFixture(t)
			target := destination(t)
			switch mutation {
			case "target":
				if err := target.WriteFile("keep", []byte("unrelated"), 0600); err != nil {
					t.Fatal(err)
				}
			case "manifest":
				if err := source.WriteFile("manifest.json", []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "source":
				if err := source.WriteFile("tree/data/example", []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "digest":
				digest = hashBytes([]byte("another import"))
			}
			if err := Import(t.Context(), source, target, digest); !errors.Is(err, ErrSnapshot) {
				t.Fatal("unsafe import succeeded")
			}
			if r, err := readReceipt(target); err == nil && r.Phase == "complete" {
				t.Fatal("unsafe import completed")
			}
		})
	}
}

func TestImportRefusesModifiedPartialPublication(t *testing.T) {
	source, digest := snapshotFixture(t)
	target := destination(t)
	stop := errors.New("stop")
	if err := importSnapshot(t.Context(), source, target, digest, func(phase string) error {
		if phase == "published-data" {
			return stop
		}
		return nil
	}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if err := target.WriteFile("data/example", []byte("unexpected writer"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Import(t.Context(), source, target, digest); !errors.Is(err, ErrSnapshot) {
		t.Fatal("changed published store accepted")
	}
	if err := Import(t.Context(), source, target, hashBytes([]byte("different"))); !errors.Is(err, ErrSnapshot) {
		t.Fatal("different import took over partial copy")
	}
}

func TestImportRebuildsOnlyRecordedIncompleteStage(t *testing.T) {
	source, digest := snapshotFixture(t)
	target := destination(t)
	if err := writeReceipt(target, importReceipt{Version: 1, Digest: digest, Phase: "copying"}); err != nil {
		t.Fatal(err)
	}
	if err := target.Mkdir(importStage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := target.Mkdir(importStage+"/partial", 0700); err != nil {
		t.Fatal(err)
	}
	if err := target.WriteFile(importStage+"/partial/file", []byte("incomplete"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := target.Chmod(importStage+"/partial", 0500); err != nil {
		t.Fatal(err)
	}
	if err := Import(t.Context(), source, target, digest); err != nil {
		t.Fatal(err)
	}
}

func TestImportDoesNotFollowReplacedStage(t *testing.T) {
	source, digest := snapshotFixture(t)
	target := destination(t)
	if err := writeReceipt(target, importReceipt{Version: 1, Digest: digest, Phase: "copying"}); err != nil {
		t.Fatal(err)
	}
	if err := target.Symlink(t.TempDir(), importStage); err != nil {
		t.Fatal(err)
	}
	if err := Import(t.Context(), source, target, digest); !errors.Is(err, ErrSnapshot) {
		t.Fatal("stage link followed")
	}
}

func TestCompletedPublicationStillRequiresIntactStores(t *testing.T) {
	for _, mutation := range []string{"corrupt", "missing", "receipt-only", "extra"} {
		t.Run(mutation, func(t *testing.T) {
			source, digest := snapshotFixture(t)
			target := destination(t)
			if mutation == "receipt-only" {
				if err := writeReceipt(target, importReceipt{Version: 1, Digest: digest, Phase: "complete"}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := Import(t.Context(), source, target, digest); err != nil {
					t.Fatal(err)
				}
				switch mutation {
				case "corrupt":
					if err := target.WriteFile("data/example", []byte("changed after publication"), 0600); err != nil {
						t.Fatal(err)
					}
				case "missing":
					if err := target.RemoveAll("home"); err != nil {
						t.Fatal(err)
					}
				case "extra":
					if err := target.WriteFile("unrelated", []byte("unexpected"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := Import(t.Context(), source, target, digest); !errors.Is(err, ErrSnapshot) {
				t.Fatal("damaged complete publication accepted", err)
			}
		})
	}
}
