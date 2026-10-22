//go:build linux

package workspaceproxy

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func dialPrivate(ctx context.Context, socket string, uid int) (net.Conn, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || uid < 0 || len(socket) > 103 {
		return nil, errors.New("invalid private backend")
	}
	dir := filepath.Dir(socket)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return nil, errors.New("private backend directory unavailable")
	}
	info, err := os.Lstat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return nil, errors.New("private backend socket unavailable")
	}
	connection, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = connection.Close()
		}
	}()
	local, ok := connection.(*net.UnixConn)
	if !ok {
		return nil, errors.New("private backend transport unavailable")
	}
	raw, err := local.SyscallConn()
	if err != nil {
		return nil, err
	}
	var credential *unix.Ucred
	var inspectErr error
	if err := raw.Control(func(fd uintptr) {
		credential, inspectErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || inspectErr != nil || credential == nil || int64(credential.Uid) != int64(uid) {
		return nil, errors.New("private backend has the wrong owner")
	}
	accepted = true
	return connection, nil
}
