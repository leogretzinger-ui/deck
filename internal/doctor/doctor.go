// Package doctor checks Deck's configured tools without starting agent sessions.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/provider"
	"github.com/noisefloor-oss/deck/internal/taskengine"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

type Check struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	OK       bool   `json:"ok"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

type Report struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// Run uses the already loaded runtime config. listenOwned is true only when
// the caller is serving this Deck's configured listener.
func Run(ctx context.Context, cfg *config.Config, listenOwned bool) Report {
	report := Report{OK: true, Checks: []Check{}}
	add := func(check Check) {
		if check.OK {
			check.Fix = ""
		}
		report.Checks = append(report.Checks, check)
		if check.Required && !check.OK {
			report.OK = false
		}
	}
	check := executable("tmux", "tmux", "Install tmux "+tmux.MinimumVersion+" or newer (macOS: brew install tmux) and add it to Deck's PATH.")
	if check.OK {
		out, err := probe(ctx, "tmux", []string{"-V"}, false)
		if err != nil || !tmux.SupportedVersion(strings.TrimSpace(out)) {
			check.OK = false
			check.Message = "tmux must report version " + tmux.MinimumVersion + " or newer."
		} else {
			check.Message = strings.TrimSpace(out)
		}
	}
	add(check)
	add(executable("git", "git", "Install Git (macOS: brew install git) and add it to Deck's PATH."))
	add(executable("gh", "gh", "Install GitHub CLI (macOS: brew install gh) and add it to Deck's PATH before starting Deck."))
	check = executable("tasks", cfg.Taskmaster.Executable, "Install noise with Tasks "+taskengine.Version+" and set taskmaster.executable to its absolute path in your config.")
	if check.OK {
		out, err := probe(ctx, cfg.Taskmaster.Executable, []string{"tasks", "version"}, false)
		if err != nil || strings.TrimSpace(out) != "noise-tasks "+taskengine.Version {
			check.OK = false
			check.Message = "Tasks must report noise-tasks " + taskengine.Version + "."
		} else {
			check.Message = strings.TrimSpace(out)
		}
	}
	add(check)
	add(executable("chats", cfg.Chats.Executable, "Install a2a and set chats.executable to its absolute path in your config."))
	entries := cfg.Providers.Entries()
	checks := make([]Check, len(entries))
	var probes sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i, entry := range entries {
		slots <- struct{}{}
		probes.Go(func() {
			defer func() { <-slots }()
			checks[i] = providerCheck(ctx, entry)
		})
	}
	probes.Wait()
	for _, check := range checks {
		add(check)
	}
	dataDir := filepath.Dir(cfg.Database.Path)
	check = Check{Name: "data-directory", Required: true, OK: true, Message: dataDir + " is writable."}
	if err := writable(dataDir); err != nil {
		check.OK = false
		check.Message = dataDir + " is not writable."
		check.Fix = "Give your Deck service user write access to this directory or set database.path to a writable location in your config."
	}
	add(check)
	add(listener(cfg, listenOwned))
	return report
}

func providerCheck(ctx context.Context, entry config.ProviderEntry) Check {
	name := "provider:" + entry.Name
	if entry.Config.Executable == "" {
		return Check{Name: name, OK: true, Message: "Not configured; optional."}
	}
	key := strings.ReplaceAll(entry.Name, "-", "_")
	check := executable(name, entry.Config.Executable, "Install "+entry.Binary+" or set providers."+key+".executable to a working absolute path in your config.")
	if args := provider.VersionProbeArgs(provider.Name(entry.Name)); check.OK && len(args) > 0 {
		if _, err := probe(ctx, entry.Config.Executable, args, true); errors.Is(err, context.DeadlineExceeded) {
			check.OK = false
			check.Required = false
			check.Message = entry.Binary + " is slow to answer its version probe."
			check.Fix = "Run doctor again when your machine is less busy."
		} else if err != nil {
			check.OK = false
			check.Message = entry.Binary + " could not run its version probe."
		} else {
			check.Message = entry.Config.Executable + " is runnable."
		}
	}
	return check
}

func executable(name, binary, fix string) Check {
	check := Check{Name: name, Required: true, OK: true, Message: binary + " found.", Fix: fix}
	if _, err := exec.LookPath(binary); err != nil {
		check.OK = false
		check.Message = binary + " not found or executable on Deck's PATH."
	}
	return check
}

func probe(ctx context.Context, binary string, args []string, private bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.WaitDelay = time.Second
	if private {
		home, err := os.MkdirTemp("", "noisedeck-doctor-")
		if err != nil {
			return "", err
		}
		defer func() { _ = os.RemoveAll(home) }()
		cmd.Dir = home
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + home, "XDG_DATA_HOME=" + home, "XDG_CACHE_HOME=" + home, "TMPDIR=" + home}
	}
	var out limitedOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState.Success() && len(out) > 0 {
		err = nil
	}
	var exitErr *exec.ExitError
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && errors.As(err, &exitErr) && !exitErr.Exited() {
		err = context.DeadlineExceeded
	}
	return string(out), err
}

type limitedOutput []byte

func (out *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 4096 - len(*out); remaining > 0 {
		*out = append(*out, p[:min(len(p), remaining)]...)
	}
	return n, nil
}

func writable(dir string) error {
	// A fresh install may not have its data directory yet. Probe the nearest
	// existing parent without leaving directories behind.
	for {
		_, err := os.Stat(dir)
		if os.IsNotExist(err) {
			parent := filepath.Dir(dir)
			if parent == dir {
				return err
			}
			dir = parent
			continue
		}
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(dir, ".noisedeck-doctor-*")
		if err != nil {
			return err
		}
		name := f.Name()
		_, writeErr := f.Write([]byte("doctor"))
		closeErr := f.Close()
		removeErr := os.Remove(name)
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		return removeErr
	}
}

func listener(cfg *config.Config, owned bool) Check {
	address, network := cfg.Server.Bind, "tcp"
	if cfg.Server.UnixSocket != "" {
		address, network = cfg.Server.UnixSocket, "unix"
	}
	check := Check{Name: "listen-address", Required: true, OK: true, Message: address + " is owned by this Deck."}
	if owned {
		return check
	}
	if network == "unix" {
		// Never unlink or replace an existing private socket during a check.
		if _, err := os.Lstat(address); !os.IsNotExist(err) {
			check.OK = false
		} else if err := writable(filepath.Dir(address)); err != nil {
			check.OK = false
		}
	} else {
		ln, err := net.Listen(network, address)
		if err != nil {
			check.OK = false
		} else {
			_ = ln.Close()
		}
	}
	if !check.OK && ownedListener(cfg) {
		check.OK = true
		return check
	}
	if check.OK {
		check.Message = address + " is free."
	} else {
		check.Message = address + " is unavailable or its owner could not be verified."
		check.Fix = "Choose a free server.bind (or server.unix_socket) in your config. If this Deck is already running, use its authenticated GET /api/doctor report."
	}
	return check
}

func (report Report) WriteText(w io.Writer) error {
	for _, check := range report.Checks {
		status := "ok"
		if !check.OK {
			status = "error"
			if !check.Required {
				status = "warning"
			}
		}
		if _, err := fmt.Fprintf(w, "%s: %s: %s", check.Name, status, strings.Join(strings.Fields(check.Message), " ")); err != nil {
			return err
		}
		if !check.OK && check.Fix != "" {
			if _, err := fmt.Fprintf(w, " %s", strings.Join(strings.Fields(check.Fix), " ")); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	return nil
}
