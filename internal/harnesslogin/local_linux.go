//go:build linux

package harnesslogin

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// LocalServer receives only native browser-launch registrations. It has no
// submission operation: local CLI processes cannot deliver a callback.
type LocalServer struct {
	listener *net.UnixListener
	workers  sync.WaitGroup
	slots    chan struct{}
}
type browserRequest struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
}

func StartLocal(path string, service *Service) (*LocalServer, error) {
	if old, err := os.Lstat(path); err == nil {
		stat, ok := old.Sys().(*syscall.Stat_t)
		if old.Mode()&os.ModeSocket == 0 || !ok || int64(stat.Uid) != int64(os.Geteuid()) {
			return nil, ErrUnavailable
		}
		if err = os.Remove(path); err != nil {
			return nil, ErrUnavailable
		}
	} else if !os.IsNotExist(err) {
		return nil, ErrUnavailable
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, ErrUnavailable
	}
	if err = os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return nil, ErrUnavailable
	}
	s := &LocalServer{listener: listener, slots: make(chan struct{}, 8)}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			select {
			case s.slots <- struct{}{}:
			default:
				_ = conn.Close()
				continue
			}
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				defer func() { <-s.slots }()
				defer func() { _ = conn.Close() }()
				s.register(conn, service)
			}()
		}
	}()
	return s, nil
}
func (s *LocalServer) Close() error {
	err := s.listener.Close()
	s.workers.Wait()
	return err
}
func (s *LocalServer) register(conn *net.UnixConn, service *Service) {
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	var creds *syscall.Ucred
	var peerErr error
	if err = raw.Control(func(fd uintptr) {
		creds, peerErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || peerErr != nil || creds == nil || int64(creds.Uid) != int64(os.Geteuid()) {
		return
	}
	var request browserRequest
	decoder := json.NewDecoder(io.LimitReader(conn, MaxURLBytes+1024))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	registered := service.Register(ctx, int(creds.Pid), request.Provider, request.URL) == nil
	request.URL = ""
	_ = json.NewEncoder(conn).Encode(registered)
}

// BrowserOpener receives the original authorization URL as a native browser
// launch argument. Callback URLs are accepted only by the authenticated POST,
// never here. Registration errors and native URLs are never printed.
func BrowserOpener(args []string) int {
	if len(args) != 1 {
		return 1
	}
	path := os.Getenv("NOISEDECK_LOGIN_SOCKET")
	var registered bool
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		err = json.NewEncoder(conn).Encode(browserRequest{Provider: os.Getenv("NOISEDECK_LOGIN_PROVIDER"), URL: args[0]})
		if err == nil {
			_ = json.NewDecoder(io.LimitReader(conn, 64)).Decode(&registered)
		}
		_ = conn.Close()
	}
	// Always preserve the original native opener, even for unsupported URLs.
	// The original executable was resolved before PATH was extended at launch.
	original := os.Getenv("NOISEDECK_LOGIN_ORIGINAL_OPENER")
	if original == "" || !filepath.IsAbs(original) {
		if registered {
			return 0
		}
		return 1
	}
	cmd := exec.Command(original, args[0])
	cmd.Env = withoutLoginEnvironment(os.Environ())
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return 1
	}
	return 0
}
func withoutLoginEnvironment(env []string) []string {
	result := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "NOISEDECK_LOGIN_") || key == "BROWSER" {
			continue
		}
		if key == "PATH" {
			result = append(result, "PATH="+os.Getenv("NOISEDECK_LOGIN_ORIGINAL_PATH"))
			continue
		}
		result = append(result, entry)
	}
	return result
}
