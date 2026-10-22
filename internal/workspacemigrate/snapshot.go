// Package workspacemigrate transfers offline workspace files without reading
// provider formats. The admin owns source quiescence and final cutover.
package workspacemigrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrSnapshot = errors.New("workspace migration: snapshot verification failed")

var stores = []string{"data", "home", "projects", "gh"}

// Entry describes file bytes and portable metadata, never provider identity.
// Manifests contain private names and must remain inside owner-only storage.
type Entry struct {
	Path   string      `json:"path"`
	Kind   string      `json:"kind"`
	Mode   fs.FileMode `json:"mode"`
	Size   int64       `json:"size,omitempty"`
	SHA256 string      `json:"sha256,omitempty"`
	Target string      `json:"target,omitempty"`
	Mtime  int64       `json:"mtime"`
}

type Manifest struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func fileHash(ctx context.Context, r *os.Root, name string) (string, int64, error) {
	f, err := r.Open(name)
	if err != nil {
		return "", 0, ErrSnapshot
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", 0, ErrSnapshot
	}
	h := sha256.New()
	n, err := io.Copy(h, contextReader{ctx, f})
	if err != nil {
		return "", 0, errors.Join(ErrSnapshot, ctx.Err())
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func scan(ctx context.Context, root *os.Root) ([]Entry, error) {
	return scanFiltered(ctx, root, nil)
}

func scanFiltered(ctx context.Context, root *os.Root, skip func(string, fs.FileInfo) bool) ([]Entry, error) {
	var entries []Entry
	err := fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return ErrSnapshot
		}
		if name == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Mode()&(fs.ModeSetuid|fs.ModeSetgid) != 0 {
			return ErrSnapshot
		}
		if skip != nil && skip(name, info) {
			if info.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if err := portableMetadata(root, name, info); err != nil {
			return err
		}
		e := Entry{Path: name, Mode: info.Mode().Perm(), Mtime: info.ModTime().UnixNano()}
		switch {
		case info.IsDir():
			e.Kind = "directory"
		case info.Mode().IsRegular():
			e.Kind = "file"
			e.SHA256, e.Size, err = fileHash(ctx, root, name)
		case info.Mode()&fs.ModeSymlink != 0:
			e.Kind = "symlink"
			e.Target, err = root.Readlink(name)
		default:
			return ErrSnapshot
		}
		if err != nil {
			return ErrSnapshot
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, errors.Join(ErrSnapshot, ctx.Err())
	}
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	return entries, nil
}

func validManifest(m Manifest) bool {
	if m.Version != 1 || len(m.Entries) == 0 {
		return false
	}
	names := make(map[string]string, len(m.Entries))
	previous := ""
	for _, e := range m.Entries {
		if !fs.ValidPath(e.Path) || e.Path == "." || e.Path <= previous || e.Mode&^0777 != 0 ||
			strings.ContainsAny(e.Path, "\x00\r\n") || !slices.Contains(stores, strings.Split(e.Path, "/")[0]) {
			return false
		}
		if parent := path.Dir(e.Path); parent != "." && names[parent] != "directory" {
			return false
		}
		switch e.Kind {
		case "directory":
			if e.Target != "" || e.SHA256 != "" || e.Size != 0 {
				return false
			}
		case "file":
			hash, err := hex.DecodeString(e.SHA256)
			if err != nil || len(hash) != sha256.Size || e.SHA256 != strings.ToLower(e.SHA256) || e.Size < 0 || e.Target != "" {
				return false
			}
		case "symlink":
			if e.Target == "" || !utf8.ValidString(e.Target) || strings.ContainsRune(e.Target, 0) || e.Size != 0 || e.SHA256 != "" {
				return false
			}
		default:
			return false
		}
		if isStore(e.Path) && e.Mode != 0700 {
			return false
		}
		names[e.Path], previous = e.Kind, e.Path
	}
	for _, store := range stores {
		if names[store] != "directory" {
			return false
		}
	}
	return true
}

// Copy copies only into a new empty directory, without following source links.
// Callers keep both roots open until verification and durable publication.
func Copy(ctx context.Context, source, target *os.Root, m Manifest) error {
	if !validManifest(m) {
		return ErrSnapshot
	}
	for _, e := range m.Entries {
		if ctx.Err() != nil {
			return errors.Join(ErrSnapshot, ctx.Err())
		}
		var err error
		switch e.Kind {
		case "directory":
			err = target.Mkdir(e.Path, 0700)
		case "symlink":
			err = target.Symlink(e.Target, e.Path)
		case "file":
			err = copyFile(ctx, source, target, e)
		}
		if err != nil {
			return errors.Join(ErrSnapshot, ctx.Err())
		}
	}
	// Set directory metadata after children, while directories remain writable.
	for i := len(m.Entries) - 1; i >= 0; i-- {
		e := m.Entries[i]
		if e.Kind == "symlink" {
			continue
		}
		if target.Chmod(e.Path, e.Mode) != nil || target.Chtimes(e.Path, time.Unix(0, e.Mtime), time.Unix(0, e.Mtime)) != nil {
			return ErrSnapshot
		}
	}
	for i := len(m.Entries) - 1; i >= 0; i-- {
		e := m.Entries[i]
		if e.Kind == "directory" && syncDir(target, e.Path) != nil {
			return ErrSnapshot
		}
	}
	if syncDir(target, ".") != nil {
		return ErrSnapshot
	}
	return Verify(ctx, target, m)
}

func copyFile(ctx context.Context, source, target *os.Root, e Entry) error {
	return copyFileTo(ctx, source, target, e, e.Path)
}
func copyFileTo(ctx context.Context, source, target *os.Root, e Entry, destination string) error {
	in, err := source.Open(e.Path)
	if err != nil {
		return ErrSnapshot
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ErrSnapshot
	}
	out, err := target.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrSnapshot
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), contextReader{ctx, in})
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || n != e.Size || hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
		return ErrSnapshot
	}
	return nil
}

// Verify checks the full tree, including unexpected names and link targets.
func Verify(ctx context.Context, root *os.Root, m Manifest) error {
	if !validManifest(m) {
		return ErrSnapshot
	}
	actual, err := scan(ctx, root)
	if err != nil {
		return ErrSnapshot
	}
	return compare(actual, m.Entries)
}

func compare(actual, wanted []Entry) error {
	if len(actual) != len(wanted) {
		return ErrSnapshot
	}
	for i, want := range wanted {
		got := actual[i]
		// Symlink timestamps are not portable; the link itself is never followed.
		if got.Path != want.Path || got.Kind != want.Kind || got.Mode != want.Mode || got.Size != want.Size ||
			got.SHA256 != want.SHA256 || got.Target != want.Target || (got.Kind != "symlink" && got.Mtime != want.Mtime) {
			return ErrSnapshot
		}
	}
	return nil
}

// ManifestFor scans a prepared private layout. Source engine checks belong to
// snapshot creation; this function alone never attests source quiescence.
func ManifestFor(ctx context.Context, root *os.Root) (Manifest, error) {
	entries, err := scan(ctx, root)
	m := Manifest{Version: 1, Entries: entries}
	if err != nil || !validManifest(m) {
		return Manifest{}, ErrSnapshot
	}
	return m, nil
}

func Digest(m Manifest) (string, []byte, error) {
	if !validManifest(m) {
		return "", nil, ErrSnapshot
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw) > 256*1024*1024 {
		return "", nil, ErrSnapshot
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), raw, nil
}

// Canonical requires an installation path rather than a relative or aliased
// directory. Callers additionally verify owner, mode and path separation.
func Canonical(name string) bool {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name || strings.ContainsAny(name, "\x00\r\n") {
		return false
	}
	resolved, err := filepath.EvalSymlinks(name)
	return err == nil && resolved == name
}

func validDigest(digest string) bool {
	value, err := hex.DecodeString(digest)
	return err == nil && len(value) == sha256.Size && digest == strings.ToLower(digest)
}
func hashBytes(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func isStore(name string) bool    { return slices.Contains(stores, name) }
