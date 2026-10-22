package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDirectoryThroughSymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	root := mkdir(t, filepath.Join(base, "real"))
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	for _, allowed := range []string{root, alias} {
		t.Run(filepath.Base(allowed), func(t *testing.T) {
			target := filepath.Join(alias, "new-"+filepath.Base(allowed))
			if err := EnsureDirectory(target, []string{allowed}); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(filepath.Join(root, filepath.Base(target))); err != nil || !info.IsDir() {
				t.Fatalf("directory not created: %v", err)
			}
		})
	}
}

func TestBrowseDirectoriesSymlinkToAnotherAllowedRoot(t *testing.T) {
	base := t.TempDir()
	root := mkdir(t, filepath.Join(base, "root"))
	other := mkdir(t, filepath.Join(base, "other"))
	child := mkdir(t, filepath.Join(other, "child"))
	if err := os.Symlink(child, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	listing, err := BrowseDirectories(root, []string{root, other})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := Canonicalize(child)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Directories) != 1 || listing.Directories[0].Path != canonical {
		t.Fatalf("unexpected listing: %+v", listing)
	}
}

func TestAllowedOperationsThroughSearchOnlyAncestor(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	ancestor := t.TempDir()
	root := mkdir(t, filepath.Join(ancestor, "root"))
	child := mkdir(t, filepath.Join(root, "child"))
	root, err := Canonicalize(root)
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := newTestRepo(t)
	if err := os.Chmod(ancestor, 0o311); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ancestor, 0o700) })
	roots := []string{root}
	ctx := context.Background()
	for name, operation := range map[string]func() error{
		"register": func() error {
			_, _, err := repo.Register(ctx, "child", child, roots, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
			return err
		},
		"browse": func() error {
			listing, err := BrowseDirectories(root, roots)
			if err == nil && (len(listing.Directories) != 1 || listing.Directories[0].Name != "child") {
				t.Errorf("unexpected listing: %+v", listing)
			}
			return err
		},
		"make": func() error {
			_, err := MakeDirectory(child, "made", roots)
			return err
		},
		"ensure":   func() error { return EnsureDirectory(filepath.Join(child, "ensured"), roots) },
		"writable": func() error { return repo.checkWritable(child, roots) },
		"inspect": func() error {
			inspection, err := Inspect(ctx, root, "", roots, nil)
			if err == nil && (len(inspection.Entries) != 1 || inspection.Entries[0] != "child") {
				t.Errorf("unexpected inspection: %+v", inspection)
			}
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAllowedOperationsRefuseCheckedPathReplacement(t *testing.T) {
	root := t.TempDir()
	checked := mkdir(t, filepath.Join(root, "checked"))
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "private"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonical, err := Canonicalize(checked)
	if err != nil {
		t.Fatal(err)
	}
	if !UnderRoot(canonical, root) {
		t.Fatal("fixture outside root")
	}
	if err := os.Rename(checked, checked+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, checked); err != nil {
		t.Fatal(err)
	}
	roots := []string{root}
	if _, err := allowedStat(canonical, roots); err == nil {
		t.Error("stat followed the replacement")
	}
	if entries, err := allowedReadDir(canonical, roots); err == nil {
		t.Errorf("listing followed the replacement: %v", entries)
	}
	if body, err := allowedReadFile(filepath.Join(canonical, "private"), roots); err == nil {
		t.Errorf("read followed the replacement: %q", body)
	}
	if dir, err := allowedDirectory(canonical, roots); err == nil {
		dir.Close()
		t.Error("directory acquisition followed the replacement")
	}
	if gitDirConfined(canonical, roots) {
		t.Error("Git discovery accepted a replaced ancestor")
	}
	in := inspector{roots: roots}
	if entries := in.entries(canonical); len(entries) != 0 {
		t.Errorf("inspection disclosed entries: %v", entries)
	}
	for _, path := range []string{outside, filepath.Join(root, "..", filepath.Base(outside))} {
		if _, _, err := openAllowedPath(path, roots); err == nil {
			t.Errorf("accepted outside path %q", path)
		}
	}
}

func TestDirectoryCreationPinsParentAcrossReplacement(t *testing.T) {
	for _, operation := range []string{"make", "ensure"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			parent := mkdir(t, filepath.Join(root, "parent"))
			saved := parent + "-saved"
			outside := t.TempDir()
			hooks := fileAccessHooks{parentOpened: func() {
				if err := os.Rename(parent, saved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, parent); err != nil {
					t.Fatal(err)
				}
			}}
			var err error
			if operation == "make" {
				_, err = makeDirectory(parent, "new", []string{root}, hooks)
			} else {
				err = ensureDirectory(filepath.Join(parent, "new"), []string{root}, hooks)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(saved, "new")); err != nil {
				t.Fatalf("did not create in the acquired parent: %v", err)
			}
			if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
				t.Fatalf("created outside: %v", err)
			}
		})
	}
}

func TestWritableProbePinsItsDirectoryBeforeGeneratingItsName(t *testing.T) {
	repo, _ := newTestRepo(t)
	root := t.TempDir()
	checked := mkdir(t, filepath.Join(root, "checked"))
	outside := t.TempDir()
	const nonce = "probe"
	sentinel := filepath.Join(outside, ".noisedeck-write-probe-"+nonce)
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo.newID = func() (string, error) {
		if err := os.Rename(checked, checked+"-saved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, checked); err != nil {
			t.Fatal(err)
		}
		return nonce, nil
	}
	if err := repo.checkWritable(checked, []string{root}); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(sentinel); err != nil || string(body) != "keep" {
		t.Fatalf("outside probe changed: %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(checked+"-saved", ".noisedeck-write-probe-"+nonce)); !os.IsNotExist(err) {
		t.Fatalf("probe left behind: %v", err)
	}
}

func TestGitMetadataRefusesEscapingFiles(t *testing.T) {
	for _, name := range []string{"gitdir", "commondir"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			repo := mkdir(t, filepath.Join(root, "repo"))
			git := mkdir(t, filepath.Join(repo, ".git"))
			outside := filepath.Join(t.TempDir(), "pointer")
			entry := git
			var body string
			if name == "commondir" {
				body = git
				if err := os.WriteFile(outside, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(git, "commondir")); err != nil {
					t.Fatal(err)
				}
				info, err := os.Lstat(entry)
				if err != nil {
					t.Fatal(err)
				}
				if gitEntryConfined(entry, info, []string{root}) {
					t.Fatal("accepted escaping commondir")
				}
			} else {
				entry = filepath.Join(root, ".git")
				if err := os.WriteFile(entry, []byte("gitdir: "+git), 0o600); err != nil {
					t.Fatal(err)
				}
				info, err := os.Lstat(entry)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(outside, []byte("gitdir: "+git), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(entry); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, entry); err != nil {
					t.Fatal(err)
				}
				if gitEntryConfined(entry, info, []string{root}) {
					t.Fatal("read a replaced gitdir file outside the root")
				}
			}
		})
	}
}

func TestStarterPathsRefuseReplacedProjectRoot(t *testing.T) {
	root := t.TempDir()
	project := mkdir(t, filepath.Join(root, "project"))
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "file"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonical, err := Canonicalize(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(project, project+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, project); err != nil {
		t.Fatal(err)
	}
	if paths, err := cleanStarterPaths(canonical, []string{"file"}); err == nil {
		t.Fatalf("accepted files through a replaced root: %v", paths)
	}
}

func TestEnsureDirectoryRefusesExistingOutsidePaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := EnsureDirectory(outside, []string{root}); err == nil {
		t.Fatal("queried an existing outside directory")
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDirectory(filepath.Join(alias, "new"), []string{root}); err == nil {
		t.Fatal("accepted an escaping parent")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
		t.Fatalf("created outside: %v", err)
	}
}

func TestInspectionConfinesDirectoryQueries(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if isDir(outside, []string{root}) {
		t.Fatal("directory query escaped")
	}
	path := outside
	if exists, err := resolveDir(&path, []string{root}); exists || err == nil {
		t.Fatalf("resolved an outside directory: %v %v", exists, err)
	}
}
