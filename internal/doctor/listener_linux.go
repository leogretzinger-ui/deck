//go:build linux

package doctor

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/noisefloor-oss/deck/internal/config"
)

func ownedListener(cfg *config.Config) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	exe, err := os.Stat(self)
	if err != nil {
		return false
	}
	lock, err := os.Stat(cfg.Database.LockPath)
	if err != nil {
		return false
	}
	inodes := listenerInodes(cfg)
	if len(inodes) == 0 {
		return false
	}
	processes, _ := os.ReadDir("/proc")
	for _, process := range processes {
		if _, err := strconv.ParseUint(process.Name(), 10, 64); err != nil {
			continue
		}
		root := filepath.Join("/proc", process.Name())
		other, err := os.Stat(filepath.Join(root, "exe"))
		if err != nil || !os.SameFile(exe, other) {
			continue
		}
		hasListener, hasLock := false, false
		fds, _ := os.ReadDir(filepath.Join(root, "fd"))
		for _, fd := range fds {
			if _, err := strconv.ParseUint(fd.Name(), 10, 64); err != nil {
				continue
			}
			path := filepath.Join(root, "fd", fd.Name())
			link, _ := os.Readlink(path)
			if inodes[link] {
				hasListener = true
			}
			if info, err := os.Stat(path); err == nil && os.SameFile(lock, info) {
				fdinfo, _ := os.ReadFile(filepath.Join(root, "fdinfo", fd.Name())) //nolint:gosec // PID and file descriptor names contain only decimal digits.
				for _, line := range strings.Split(string(fdinfo), "\n") {
					fields := strings.Fields(line)
					if len(fields) >= 6 && fields[0] == "lock:" && fields[2] == "FLOCK" && fields[4] == "WRITE" && fields[5] == process.Name() {
						hasLock = true
					}
				}
			}
		}
		if hasListener && hasLock {
			return true
		}
	}
	return false
}

func listenerInodes(cfg *config.Config) map[string]bool {
	out := make(map[string]bool)
	if cfg.Server.UnixSocket != "" {
		raw, _ := os.ReadFile("/proc/net/unix")
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 8 && fields[3] == "00010000" && fields[7] == cfg.Server.UnixSocket {
				out["socket:["+fields[6]+"]"] = true
			}
		}
		return out
	}
	host, port, err := net.SplitHostPort(cfg.Server.Bind)
	if err != nil {
		return out
	}
	wantPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return out
	}
	wantIP := net.ParseIP(host)
	for _, table := range []string{"tcp", "tcp6"} {
		raw, _ := os.ReadFile(filepath.Join("/proc", "net", table)) //nolint:gosec // table is limited to tcp and tcp6 under /proc/net.
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			ipHex, portHex, ok := strings.Cut(fields[1], ":")
			gotPort, err := strconv.ParseUint(portHex, 16, 16)
			if !ok || err != nil || gotPort != wantPort {
				continue
			}
			ip := make(net.IP, len(ipHex)/2)
			valid := len(ip) == net.IPv4len || len(ip) == net.IPv6len
			for i := 0; valid && i < len(ip); i += 4 {
				word, err := strconv.ParseUint(ipHex[2*i:2*i+8], 16, 32)
				if err != nil {
					valid = false
					break
				}
				// procfs renders each native-endian 32-bit address word.
				binary.NativeEndian.PutUint32(ip[i:i+4], uint32(word))
			}
			if valid && (ip.Equal(wantIP) || ip.IsUnspecified()) {
				out["socket:["+fields[9]+"]"] = true
			}
		}
	}
	return out
}
