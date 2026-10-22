//go:build linux

package workspaceplain

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"golang.org/x/sys/unix"
)

type identity struct {
	Device uint64
	Inode  uint64
}

const marker = ".deck-plain-installation.json"

var stores = []string{"data", "home", "projects", "tmp", "gh", "data/gateway"}

func privateIdentity(path string, uid int, directory bool) (identity, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return identity{}, ErrStorage
	}
	var st unix.Stat_t
	if unix.Lstat(path, &st) != nil {
		return identity{}, ErrStorage
	}
	return checkedIdentity(st, uid, directory)
}

func checkedIdentity(st unix.Stat_t, uid int, directory bool) (identity, error) {
	want := uint32(unix.S_IFREG)
	if directory {
		want = unix.S_IFDIR
	}
	if int(st.Uid) != uid || st.Mode&0077 != 0 || st.Mode&unix.S_IFMT != want {
		return identity{}, ErrStorage
	}
	return identity{uint64(st.Dev), st.Ino}, nil
}

func (c *Controller) rootFD() (int, error) {
	id, err := privateIdentity(c.cfg.StateRoot, c.cfg.OwnerUID, true)
	if err != nil || id != c.rootIdentity {
		return -1, ErrStorage
	}
	fd, err := unix.Open(c.cfg.StateRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, ErrStorage
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || (identity{uint64(st.Dev), st.Ino}) != id {
		_ = unix.Close(fd)
		return -1, ErrStorage
	}
	return fd, nil
}

func (c *Controller) savedLayout() (map[string]identity, error) {
	var raw string
	if c.db.QueryRow(`SELECT layout FROM installation WHERE id=1`).Scan(&raw) != nil {
		return nil, ErrJournal
	}
	layout := map[string]identity{}
	if json.Unmarshal([]byte(raw), &layout) != nil || layout == nil {
		return nil, ErrJournal
	}
	for p := range layout {
		if !slices.Contains(stores, p) {
			return nil, ErrJournal
		}
	}
	return layout, nil
}

// layout validates partial provisioning as well as complete installations.
// An unrecorded directory is never adopted after an interrupted mkdir.
func (c *Controller) layout(requireComplete bool) (bool, error) {
	fd, err := c.rootFD()
	if err != nil {
		return false, err
	}
	defer func() { _ = unix.Close(fd) }()
	layout, err := c.savedLayout()
	if err != nil {
		return false, err
	}
	copyFD, err := unix.Dup(fd)
	if err != nil {
		return false, ErrStorage
	}
	f := os.NewFile(uintptr(copyFD), c.cfg.StateRoot)
	entries, err := f.Readdirnames(-1)
	_ = f.Close()
	if err != nil {
		return false, ErrStorage
	}
	marked := false
	for _, name := range entries {
		if name == marker {
			marked = true
			continue
		}
		if !slices.Contains(stores, name) {
			return false, ErrStorage
		}
		if _, ok := layout[name]; !ok {
			return false, ErrStorage
		}
	}
	if !marked {
		if len(entries) != 0 || len(layout) != 0 || requireComplete {
			return false, ErrStorage
		}
		return false, nil
	}
	markFD, err := unix.Openat(fd, marker, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, ErrStorage
	}
	markFile := os.NewFile(uintptr(markFD), marker)
	var st unix.Stat_t
	statErr := unix.Fstat(markFD, &st)
	body, readErr := io.ReadAll(io.LimitReader(markFile, 1025))
	_ = markFile.Close()
	if _, err := checkedIdentity(st, c.cfg.OwnerUID, false); err != nil || statErr != nil || readErr != nil || string(body) != c.fingerprint+"\n" {
		return false, ErrStorage
	}
	for name, want := range layout {
		got, err := c.storeIdentity(fd, name)
		if err != nil || got != want {
			return false, ErrStorage
		}
	}
	complete := len(layout) == len(stores)
	if requireComplete && !complete {
		return false, ErrStorage
	}
	return complete, nil
}

func (c *Controller) storeIdentity(rootFD int, name string) (identity, error) {
	parent := rootFD
	base := name
	if name == "data/gateway" {
		fd, err := unix.Openat(rootFD, "data", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return identity{}, ErrStorage
		}
		defer func() { _ = unix.Close(fd) }()
		parent, base = fd, "gateway"
	}
	var st unix.Stat_t
	if unix.Fstatat(parent, base, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return identity{}, ErrStorage
	}
	return checkedIdentity(st, c.cfg.OwnerUID, true)
}

func (c *Controller) prepare() error {
	if complete, err := c.layout(false); err != nil || complete {
		return err
	}
	fd, err := c.rootFD()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	layout, err := c.savedLayout()
	if err != nil {
		return err
	}
	markFD, err := unix.Openat(fd, marker, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err == nil {
		f := os.NewFile(uintptr(markFD), marker)
		_, writeErr := io.WriteString(f, c.fingerprint+"\n")
		syncErr := f.Sync()
		closeErr := f.Close()
		if errors.Join(writeErr, syncErr, closeErr) != nil || unix.Fsync(fd) != nil {
			return ErrStorage
		}
	} else if !errors.Is(err, unix.EEXIST) {
		return ErrStorage
	}
	for _, name := range stores {
		if _, ok := layout[name]; ok {
			continue
		}
		parent := fd
		base := name
		if name == "data/gateway" {
			parent, err = unix.Openat(fd, "data", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return ErrStorage
			}
			base = "gateway"
		}
		err = unix.Mkdirat(parent, base, 0700)
		if err == nil {
			err = unix.Fsync(parent)
		}
		if parent != fd {
			_ = unix.Close(parent)
		}
		if err != nil {
			return ErrStorage
		}
		id, err := c.storeIdentity(fd, name)
		if err != nil {
			return err
		}
		layout[name] = id
		raw, _ := json.Marshal(layout)
		if _, err = c.db.Exec(`UPDATE installation SET layout=? WHERE id=1`, string(raw)); err != nil {
			return ErrJournal
		}
	}
	_, err = c.layout(true)
	return err
}
