//go:build unix

package apppreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// MetadataServer exposes only nonsecret launch assignments to the shared
// unconfined Unix user. It is never mounted on either HTTP listener.
type MetadataServer struct {
	listener *net.UnixListener
	path     string
	identity os.FileInfo
	registry *Registry
	workers  sync.WaitGroup
	once     sync.Once
}

func privateMetadataDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0077 != 0 {
		return errors.New("preview socket directory must be a private directory owned by the Deck user")
	}
	return nil
}

func StartMetadata(socket string, registry *Registry) (*MetadataServer, error) {
	if registry == nil || !filepath.IsAbs(socket) {
		return nil, errors.New("preview metadata needs a registry and absolute socket path")
	}
	if err := privateMetadataDir(filepath.Dir(socket)); err != nil {
		return nil, err
	}
	if old, err := os.Lstat(socket); err == nil {
		stat, ok := old.Sys().(*syscall.Stat_t)
		if old.Mode()&os.ModeSocket == 0 || !ok || int(stat.Uid) != os.Geteuid() {
			return nil, errors.New("preview socket path is occupied by another resource")
		}
		conn, dialErr := net.DialTimeout("unix", socket, metadataTimeout)
		if dialErr == nil {
			_ = conn.Close()
			return nil, errors.New("preview socket is already serving; configure a distinct metadata_socket for this installation")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("cannot establish that preview socket is stale: %w", dialErr)
		}
		// This catches a replacement already visible at the check. Unlink is
		// a separate operation; other same-UID processes must not concurrently
		// rewrite this directory in the unconfined workspace.
		current, err := os.Lstat(socket)
		if err != nil || !os.SameFile(old, current) {
			return nil, errors.New("preview socket changed while checking it")
		}
		if err := os.Remove(socket); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	identity, err := os.Lstat(socket)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	s := &MetadataServer{listener: listener, path: socket, identity: identity, registry: registry}
	if err := os.Chmod(socket, 0600); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.workers.Add(1)
	go s.accept()
	return s, nil
}

func (s *MetadataServer) accept() {
	defer s.workers.Done()
	slots := make(chan struct{}, 16)
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			defer func() { <-slots }()
			defer func() { _ = conn.Close() }()
			s.serve(conn)
		}()
	}
}

func (s *MetadataServer) serve(conn *net.UnixConn) {
	ctx, cancel := context.WithTimeout(context.Background(), metadataTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return
	}
	response := metadataResponse{Version: metadataVersion, Error: "bad_request"}
	raw, err := io.ReadAll(io.LimitReader(conn, 1025))
	var request metadataRequest
	if err == nil && len(raw) <= 1024 && decodeMetadata(raw, &request) == nil && request.Version == metadataVersion && validPreviewProjectID(request.ProjectID) {
		reg, err := s.registry.Get(ctx, request.ProjectID)
		switch {
		case errors.Is(err, ErrPreviewProjectNotFound):
			response.Error = "not_found"
		case err != nil:
			response.Error = "unavailable"
		case reg == nil || !reg.Enabled:
			response.Error = "preview_disabled"
		default:
			host, port, _ := net.SplitHostPort(reg.Upstream)
			n, _ := strconv.Atoi(port)
			assignment := &Assignment{ProjectID: reg.ProjectID, Host: host, Port: n, Origin: reg.Origin}
			if assignment.validate(request.ProjectID) != nil {
				response.Error = "unavailable"
			} else {
				response.Error, response.Assignment = "", assignment
			}
		}
	}
	_ = json.NewEncoder(conn).Encode(response)
}

func (s *MetadataServer) Close() error {
	var err error
	s.once.Do(func() {
		err = s.listener.Close()
		s.workers.Wait()
		// Skip a replacement observed before cleanup. The check and unlink
		// are not atomic protection against another process sharing this UID.
		if current, statErr := os.Lstat(s.path); statErr == nil && os.SameFile(s.identity, current) {
			err = errors.Join(err, os.Remove(s.path))
		}
	})
	return err
}
