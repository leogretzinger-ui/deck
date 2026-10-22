//go:build linux || darwin

package project

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func openDirectoryAtFlags(parent int, name string, flags int) (int, error) {
	for {
		fd, err := unix.Openat(parent, name, flags|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != syscall.EINTR {
			return fd, err
		}
	}
}

func openRootPath(root *os.Root, name string, directory bool) (*os.File, error) {
	flags := os.O_RDONLY | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	return root.OpenFile(name, flags, 0)
}

// openDirAt opens one folder beneath parent without following a link at
// name, so a walk or a delete can never be steered out of the folder it
// started in.
func openDirAt(parent *os.File, name string) (*os.File, error) {
	fd, err := openDirectoryAtFlags(int(parent.Fd()), name, unix.O_RDONLY)
	runtime.KeepAlive(parent)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if f == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open directory: invalid file descriptor")
	}
	return f, nil
}

// unlinkFileAt removes name from parent's folder. Without AT_REMOVEDIR the
// kernel refuses a folder, so this never removes one.
func unlinkFileAt(parent *os.File, name string) error {
	for {
		err := unix.Unlinkat(int(parent.Fd()), name, 0)
		runtime.KeepAlive(parent)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func createUploadStage(parent *os.File) (*os.File, string, error) {
	for range 100 {
		id, err := newID()
		if err != nil {
			return nil, "", err
		}
		name := ".upload-" + id
		fd, err := openUploadAt(int(parent.Fd()), name)
		runtime.KeepAlive(parent)
		if errors.Is(err, syscall.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		f := os.NewFile(uintptr(fd), name)
		if f == nil {
			unix.Close(fd)
			return nil, "", errors.New("stage upload: invalid file descriptor")
		}
		return f, name, nil
	}
	return nil, "", errors.New("stage upload: temporary name collisions")
}

func openUploadAt(parent int, name string) (int, error) {
	flags := unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW
	for {
		fd, err := unix.Openat(parent, name, flags, 0o600)
		if err != syscall.EINTR {
			return fd, err
		}
	}
}

func removeUploadStage(parent *os.File, name string) {
	for {
		err := unix.Unlinkat(int(parent.Fd()), name, 0)
		runtime.KeepAlive(parent)
		if err != syscall.EINTR {
			return
		}
	}
}

// commitUpload renames the staged file over the destination. On the
// no-consent path it is never reached: a name that holds anything refuses
// before this. On the consent path it is the final act after the version
// recheck, and the rename is what makes the swap atomic — a reader sees
// either the old bytes or the new ones, never a half-written file. The
// recheck-to-rename window is documented once, on WriteFile.
func commitUpload(parent *os.File, staged, leaf string) error {
	for {
		err := unix.Renameat(int(parent.Fd()), staged, int(parent.Fd()), leaf)
		runtime.KeepAlive(parent)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// placeUpload moves the staged file onto its destination name under the
// destination's own directory handle, so no step re-resolves a path.
//
// Without consent the commit is create-if-absent: it either lands on a free
// name or refuses, and can never displace anything. With consent the
// destination is re-examined after staging — only a regular file is
// replaceable, and the opaque version must still match the one the user
// approved. An approved target that vanished in between cannot be
// destroyed, so the upload lands on the now-free name instead.
func placeUpload(parent *os.File, staged, leaf string, consent *UploadConsent, hooks fileAccessHooks) error {
	if consent == nil {
		return placeUploadIfAbsent(parent, staged, leaf, false)
	}
	target, err := statUploadTarget(parent, leaf)
	if err != nil {
		return err
	}
	if target == nil {
		// The approved file is gone; nothing a replacement could destroy
		// remains, so the commit is an ordinary create-if-absent.
		if hooks.targetMissing != nil {
			hooks.targetMissing()
		}
		return placeUploadIfAbsent(parent, staged, leaf, true)
	}
	if target.Dir {
		return &UploadConflict{Leaf: leaf, Dir: true, Version: target.Version, Size: target.Size, MTime: target.MTime}
	}
	if !target.Regular {
		return &UploadConflict{Leaf: leaf, NotFile: true, Version: target.Version, Size: target.Size, MTime: target.MTime}
	}
	// The version is recomputed here, after staging: this is the check a
	// same-size, same-mtime replacement or an in-place rewrite fails.
	if target.Version != consent.Version {
		return &UploadConflict{Leaf: leaf, Changed: true, Version: target.Version, Size: target.Size, MTime: target.MTime}
	}
	if hooks.conflictVerified != nil {
		hooks.conflictVerified()
	}
	return commitUpload(parent, staged, leaf)
}

func placeUploadIfAbsent(parent *os.File, staged, leaf string, changed bool) error {
	if err := commitUploadIfAbsent(parent, staged, leaf); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return uploadConflictAt(parent, leaf, changed)
		}
		return err
	}
	return nil
}

// uploadConflictAt turns a standing refusal into the ask: what holds the
// name now, as the listing would disclose it. changed marks a refusal of
// presented consent rather than a plain collision.
func uploadConflictAt(parent *os.File, leaf string, changed bool) error {
	target, err := statUploadTarget(parent, leaf)
	if err != nil {
		return err
	}
	if target == nil {
		// The racer deleted the name between the commit's refusal and this
		// look. Nothing was destroyed; the raw refusal is the honest answer.
		return syscall.EEXIST
	}
	return &UploadConflict{
		Leaf:    leaf,
		Dir:     target.Dir,
		NotFile: !target.Dir && !target.Regular,
		Changed: changed,
		Version: target.Version,
		Size:    target.Size,
		MTime:   target.MTime,
	}
}

func statUploadTarget(parent *os.File, leaf string) (*uploadTargetStat, error) {
	var st unix.Stat_t
	for {
		err := unix.Fstatat(int(parent.Fd()), leaf, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.ENOENT) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		break
	}
	return &uploadTargetStat{
		Dir:     st.Mode&unix.S_IFMT == unix.S_IFDIR,
		Regular: st.Mode&unix.S_IFMT == unix.S_IFREG,
		Size:    st.Size,
		MTime:   time.Unix(st.Mtim.Sec, st.Mtim.Nsec),
		Version: uploadVersion(&st),
	}, nil
}

// uploadVersion folds the kernel's identity for a name into the opaque
// string consent carries: device and inode (which file), the type bits
// (what kind), the size, and modification and change times at nanosecond
// resolution (what last happened to it). A replacement under the same name
// gets a new inode; an in-place rewrite keeps the inode but moves ctime —
// including a rewrite that restores size and mtime exactly, which cannot
// restore ctime. atime is deliberately excluded: reading a file must not
// invalidate an approval taken for it. The string is server-private;
// clients return it verbatim and compare nothing.
func uploadVersion(st *unix.Stat_t) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"deck-upload-version-1|%d|%d|%d|%d|%d|%d|%d|%d",
		st.Dev, st.Ino, st.Mode&unix.S_IFMT, st.Size,
		st.Mtim.Sec, st.Mtim.Nsec, st.Ctim.Sec, st.Ctim.Nsec)))
	return "1-" + hex.EncodeToString(sum[:])
}

// commitUploadIfAbsent lands the staged file only when the destination name
// is free. renameat2(RENAME_NOREPLACE) makes the check and the move one
// kernel operation; where the filesystem refuses the flag, linkat is the
// atomic fallback — a hard link either takes the free name or fails with
// EEXIST, and the staged name is dropped after it lands.
func commitUploadIfAbsent(parent *os.File, staged, leaf string) error {
	pfd := int(parent.Fd())
	// A name that flips between taken and free races with any report built
	// on it, so a vanished racer is retried once rather than reported as a
	// standing conflict; the bound keeps a pathological flip-flop from
	// looping forever. The conservative answer on the second refusal is
	// the conflict itself.
	for vanishRetries := 0; ; {
		err := renameNoReplace(pfd, staged, leaf)
		runtime.KeepAlive(parent)
		if err == nil {
			return nil
		}
		if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.EOPNOTSUPP) {
			return linkUploadIntoPlace(parent, staged, leaf)
		}
		if errors.Is(err, syscall.EEXIST) && vanishRetries < 1 {
			target, statErr := statUploadTarget(parent, leaf)
			if statErr == nil && target == nil {
				vanishRetries++
				continue
			}
		}
		return err
	}
}

func linkUploadIntoPlace(parent *os.File, staged, leaf string) error {
	pfd := int(parent.Fd())
	for {
		err := unix.Linkat(pfd, staged, pfd, leaf, 0)
		runtime.KeepAlive(parent)
		if !errors.Is(err, syscall.EINTR) {
			if err != nil {
				return err
			}
			removeUploadStage(parent, staged)
			return nil
		}
	}
}
