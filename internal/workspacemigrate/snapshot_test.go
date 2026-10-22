package workspacemigrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fixture(t *testing.T) (*os.Root, Manifest) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range stores {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range map[string]string{
		"data/example": "synthetic application data", "home/native-history": "opaque synthetic provider bytes",
		"projects/executable": "#!/bin/sh\nprintf preserved", "gh/hosts.yml": "synthetic configuration",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/workspace/executable", filepath.Join(dir, "home/project-link")); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	m, err := ManifestFor(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	return r, m
}

func destination(t *testing.T) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestCopyPreservesOpaqueDataAndAbsoluteLinks(t *testing.T) {
	source, manifest := fixture(t)
	target := destination(t)
	if err := Copy(t.Context(), source, target, manifest); err != nil {
		t.Fatal(err)
	}
	link, err := target.Readlink("home/project-link")
	if err != nil || link != "/workspace/executable" {
		t.Fatal("absolute container path was not preserved")
	}
	if err := Verify(t.Context(), source, manifest); err != nil {
		t.Fatal("source changed during copy")
	}
	if err := Copy(t.Context(), source, target, manifest); !errors.Is(err, ErrSnapshot) {
		t.Fatal("copy must not overwrite an existing destination")
	}
}

func TestVerifyRejectsChangedMissingAndExtraData(t *testing.T) {
	for _, mutation := range []string{"changed", "missing", "extra", "link"} {
		t.Run(mutation, func(t *testing.T) {
			source, manifest := fixture(t)
			target := destination(t)
			if err := Copy(t.Context(), source, target, manifest); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mutation {
			case "changed":
				err = target.WriteFile("home/native-history", []byte("changed"), 0700)
			case "missing":
				err = target.Remove("data/example")
			case "extra":
				err = target.WriteFile("projects/extra", []byte("extra"), 0600)
			case "link":
				err = target.Remove("home/project-link")
				if err == nil {
					err = target.Symlink("/another/project", "home/project-link")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := Verify(t.Context(), target, manifest); !errors.Is(err, ErrSnapshot) {
				t.Fatal("changed tree verified successfully")
			}
		})
	}
}

func TestManifestRejectsTraversalAndSymlinkParents(t *testing.T) {
	_, m := fixture(t)
	for _, name := range []string{"../outside", "/outside", "data/../outside", "home/project-link/outside", "data/\nprivate"} {
		bad := Manifest{Version: m.Version, Entries: append([]Entry(nil), m.Entries...)}
		bad.Entries[len(bad.Entries)-1].Path = name
		slices.SortFunc(bad.Entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
		if validManifest(bad) {
			t.Errorf("unsafe entry accepted: %q", name)
		}
	}
}

func TestManifestRejectsNonUTF8SymlinkTarget(t *testing.T) {
	source, _ := fixture(t)
	if err := source.Symlink("bad\xffname", "home/dangling"); err != nil {
		t.Fatal(err)
	}
	if _, err := ManifestFor(t.Context(), source); !errors.Is(err, ErrSnapshot) {
		t.Fatalf("non-UTF-8 symlink target accepted: %v", err)
	}
}

func TestCopyRejectsSourceChangesAndCancellation(t *testing.T) {
	source, manifest := fixture(t)
	if err := source.WriteFile("data/example", []byte("changed after manifest"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Copy(t.Context(), source, destination(t), manifest); !errors.Is(err, ErrSnapshot) {
		t.Fatal("changed source accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Copy(ctx, source, destination(t), manifest); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled copy continued")
	}
}
