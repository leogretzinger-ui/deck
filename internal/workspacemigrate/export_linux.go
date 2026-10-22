//go:build linux

package workspacemigrate

import (
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Snapshot creates a new private plaintext export. The source is never opened
// for writing. An incomplete output is retained for inspection, never reused.
func Snapshot(ctx context.Context, c SourceConfig) (string, error) { return snapshot(ctx, c, c.engine) }

func snapshot(ctx context.Context, c SourceConfig, run engineRead) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	before, stores, err := observeSource(ctx, c, run)
	if err != nil {
		return "", err
	}
	if os.Mkdir(c.OutputDir, 0700) != nil {
		return "", ErrSnapshot
	}
	output, err := os.OpenRoot(c.OutputDir)
	if err != nil {
		return "", ErrSnapshot
	}
	defer func() { _ = output.Close() }()
	if output.Mkdir("tree", 0700) != nil {
		return "", ErrSnapshot
	}
	tree, err := output.OpenRoot("tree")
	if err != nil {
		return "", ErrSnapshot
	}
	defer func() { _ = tree.Close() }()
	var checks []sourceCheck
	for _, store := range stores {
		check, err := exportStore(ctx, tree, store)
		if err != nil {
			return "", err
		}
		checks = append(checks, check)
	}
	// Re-read all original files before accepting this offline snapshot. Engine
	// state is checked again afterward; no database row stands in for shutdown.
	for _, check := range checks {
		if check.verify(ctx) != nil {
			return "", ErrSnapshot
		}
	}
	after, _, err := observeSource(ctx, c, run)
	if err != nil || !reflect.DeepEqual(before, after) {
		return "", ErrSource
	}
	// Only Deck's copied control DB changes. This is still protected plaintext
	// staging, so SQLite cannot reopen a detached encrypted mount by pathname.
	if err := prepareDatabase(ctx, filepath.Join(c.OutputDir, "tree", "data", "noisedeck.db")); err != nil {
		return "", err
	}
	manifest, err := ManifestFor(ctx, tree)
	if err != nil {
		return "", err
	}
	digest, raw, err := Digest(manifest)
	if err != nil {
		return "", err
	}
	if output.WriteFile("manifest.json", raw, 0600) != nil {
		return "", ErrSnapshot
	}
	// The private source report records omissions and normalization, but is not
	// imported into the encrypted workload or copied into a control/API journal.
	report := struct {
		Version       int      `json:"version"`
		Exclusions    []string `json:"exclusions"`
		OmittedMounts []string `json:"omitted_mounts"`
		Normalization string   `json:"normalization"`
	}{Version: 1, Normalization: "deck migrations, integrity checks, intentional session stops"}
	for _, mount := range before.Mounts {
		if omittedAppMount(mount) {
			report.OmittedMounts = append(report.OmittedMounts, mount.Destination)
		}
	}
	slices.Sort(report.OmittedMounts)
	for _, check := range checks {
		for _, skip := range check.skipped {
			report.Exclusions = append(report.Exclusions, check.store.destination+"/"+skip)
		}
	}
	info, err := json.Marshal(report)
	if err != nil || output.WriteFile("source-report.json", info, 0600) != nil {
		return "", ErrSnapshot
	}
	if err := assignOwner(output, c.OwnerUID, c.OwnerGID); err != nil {
		return "", err
	}
	if err := syncTree(output); err != nil {
		return "", err
	}
	parent, err := os.OpenRoot(filepath.Dir(c.OutputDir))
	if err != nil {
		return "", ErrSnapshot
	}
	defer func() { _ = parent.Close() }()
	if syncDir(parent, ".") != nil {
		return "", ErrSnapshot
	}
	return digest, nil
}

type sourceCheck struct {
	store   sourceStore
	entries []Entry
	skipped []string
}

func scanStore(ctx context.Context, root *os.Root, store sourceStore) ([]Entry, []string, error) {
	var skipped []string
	entries, err := scanFiltered(ctx, root, func(name string, info fs.FileInfo) bool {
		if slices.Contains(store.exclude, name) || info.Mode()&fs.ModeSocket != 0 {
			skipped = append(skipped, name)
			return true
		}
		return false
	})
	return entries, skipped, err
}

func exportStore(ctx context.Context, target *os.Root, store sourceStore) (sourceCheck, error) {
	result := sourceCheck{store: store}
	source, err := os.OpenRoot(store.source)
	if err != nil {
		return result, ErrSnapshot
	}
	defer func() { _ = source.Close() }()
	result.entries, result.skipped, err = scanStore(ctx, source, store)
	if err != nil {
		return result, err
	}
	if target.Mkdir(store.destination, 0700) != nil {
		return result, ErrSnapshot
	}
	for _, e := range result.entries {
		to := e
		to.Path = store.destination + "/" + e.Path
		switch e.Kind {
		case "directory":
			err = target.Mkdir(to.Path, 0700)
		case "symlink":
			err = target.Symlink(e.Target, to.Path)
		case "file":
			err = copyFileTo(ctx, source, target, e, to.Path)
		}
		if err != nil {
			return result, ErrSnapshot
		}
	}
	for i := len(result.entries) - 1; i >= 0; i-- {
		e := result.entries[i]
		if e.Kind == "symlink" {
			continue
		}
		name := store.destination + "/" + e.Path
		if target.Chmod(name, e.Mode) != nil || target.Chtimes(name, time.Unix(0, e.Mtime), time.Unix(0, e.Mtime)) != nil {
			return result, ErrSnapshot
		}
	}
	// The separate GH/MemPalace overlays are copied later. Verify the portable
	// contents now; only the new private store root has intentionally new mode.
	copied, err := target.OpenRoot(store.destination)
	if err != nil {
		return result, ErrSnapshot
	}
	actual, err := scan(ctx, copied)
	_ = copied.Close()
	if err != nil || compare(actual, result.entries) != nil {
		return result, ErrSnapshot
	}
	return result, nil
}

func (c sourceCheck) verify(ctx context.Context) error {
	source, err := os.OpenRoot(c.store.source)
	if err != nil {
		return ErrSnapshot
	}
	defer func() { _ = source.Close() }()
	entries, skipped, err := scanStore(ctx, source, c.store)
	if err != nil || !slices.Equal(skipped, c.skipped) {
		return ErrSnapshot
	}
	return compare(entries, c.entries)
}

func assignOwner(root *os.Root, uid, gid int) error {
	return fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return ErrSnapshot
		}
		if root.Lchown(name, uid, gid) != nil {
			return ErrSnapshot
		}
		return nil
	})
}
func syncTree(root *os.Root) error {
	var dirs []string
	err := fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return ErrSnapshot
		}
		if d.IsDir() {
			dirs = append(dirs, name)
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		f, err := root.Open(name)
		if err != nil {
			return ErrSnapshot
		}
		syncErr := f.Sync()
		closeErr := f.Close()
		if syncErr != nil || closeErr != nil {
			return ErrSnapshot
		}
		return nil
	})
	if err != nil {
		return ErrSnapshot
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if syncDir(root, dirs[i]) != nil {
			return ErrSnapshot
		}
	}
	return nil
}

// LoadSourceConfig reads only private admin inventory. It never consumes a
// shell environment file or evaluates an engine command supplied by a browser.
func LoadSourceConfig(path string) (SourceConfig, error) {
	var cfg SourceConfig
	if !Canonical(path) {
		return cfg, ErrSource
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 64*1024 {
		return cfg, ErrSource
	}
	var owner unix.Stat_t
	if unix.Lstat(path, &owner) != nil || int(owner.Uid) != os.Geteuid() {
		return cfg, ErrSource
	}
	// #nosec G304 -- Canonical caller-owned private inventory file, checked above.
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, ErrSource
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if dec.Decode(&cfg) != nil {
		return cfg, ErrSource
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return cfg, ErrSource
	}
	return cfg, nil
}
