//go:build linux

package server

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type privateListener struct {
	*net.UnixListener
	uid int
}

func listenPrivate(path string, uid int) (net.Listener, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 103 || uid < 0 || uint64(uid) > uint64(^uint32(0)) || uid != os.Geteuid() {
		return nil, errors.New("server: private socket requires a canonical path and the runtime owner UID")
	}
	dir := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return nil, errors.New("server: private socket directory must exist without symlinks")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("server: private socket directory must be owner-only")
	}
	var stat unix.Stat_t
	if err := unix.Stat(dir, &stat); err != nil || int(stat.Uid) != uid {
		return nil, errors.New("server: private socket directory has the wrong owner")
	}
	if existing, err := os.Lstat(path); err == nil {
		if existing.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("server: private socket path contains another file")
		}
		if err := unix.Lstat(path, &stat); err != nil || int(stat.Uid) != uid {
			return nil, errors.New("server: private socket has the wrong owner")
		}
		connection, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond)
		if connection != nil {
			_ = connection.Close()
		}
		if !errors.Is(dialErr, unix.ECONNREFUSED) {
			return nil, errors.New("server: existing private socket is live or uncertain")
		}
		current, err := os.Lstat(path)
		if err != nil || !os.SameFile(existing, current) {
			return nil, errors.New("server: private socket changed during inspection")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return &privateListener{UnixListener: listener, uid: uid}, nil
}

func (l *privateListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		raw, err := connection.SyscallConn()
		var credential *unix.Ucred
		if err == nil {
			var inspectErr error
			err = raw.Control(func(fd uintptr) {
				credential, inspectErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
			})
			if err == nil {
				err = inspectErr
			}
		}
		if err == nil && credential != nil && int64(credential.Uid) == int64(l.uid) {
			return connection, nil
		}
		_ = connection.Close()
	}
}
