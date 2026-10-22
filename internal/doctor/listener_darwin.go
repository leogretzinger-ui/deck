//go:build darwin

package doctor

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

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
	args := []string{"-nP", "-t"}
	if cfg.Server.UnixSocket != "" {
		args = append(args, cfg.Server.UnixSocket)
	} else {
		_, port, err := net.SplitHostPort(cfg.Server.Bind)
		if err != nil {
			return false
		}
		args = []string{"-nP", "-iTCP:" + port, "-sTCP:LISTEN", "-Fpn"}
	}
	pids, err := lsof(args...)
	if err != nil {
		return false
	}
	owners := strings.Fields(pids)
	if cfg.Server.UnixSocket == "" {
		owners = lsofListenerPIDs(pids, cfg.Server.Bind)
	}
	for _, pid := range owners {
		if n, err := strconv.Atoi(pid); err != nil || n <= 0 {
			continue
		}
		text, err := lsof("-a", "-p", pid, "-d", "txt", "-Fn")
		if err != nil {
			continue
		}
		matches := false
		for _, line := range strings.Split(text, "\n") {
			if path, ok := strings.CutPrefix(line, "n"); ok {
				if info, err := os.Stat(path); err == nil && os.SameFile(exe, info) {
					matches = true
				}
			}
		}
		if !matches {
			continue
		}
		files, err := lsof("-a", "-p", pid, "-Ffl", cfg.Database.LockPath)
		if err == nil {
			for _, line := range strings.Split(files, "\n") {
				if line == "lW" {
					return true
				}
			}
		}
	}
	return false
}

func lsof(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/lsof", args...)
	cmd.WaitDelay = time.Second
	var out limitedOutput
	cmd.Stdout = &out
	err := cmd.Run()
	return string(out), err
}
