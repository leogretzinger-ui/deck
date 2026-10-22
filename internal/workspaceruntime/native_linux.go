//go:build linux

package workspaceruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/noisefloor-oss/deck/internal/sealedstate"
	"golang.org/x/sys/unix"
)

type kernelFacts struct {
	Mounts                  []sealedstate.Mount
	MountInstance           uint64
	BootID                  string
	FuseDevice              bool
	FuseAllowSysAdminAccess string
	SwapFacts               sealedstate.Facts
	ResumeDevice            string
}

func readKernelFor(path string) (kernelFacts, error) {
	f, err := readKernel()
	if err != nil {
		return f, err
	}
	m, ok := sealedstate.MountFor(f.Mounts, path)
	if !ok || m.ID <= 0 {
		return f, ErrUnavailable
	}
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return f, ErrUnavailable
	}
	defer func() { _ = unix.Close(fd) }()
	var ordinary, unique unix.Statx_t
	if unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID, &ordinary) != nil ||
		ordinary.Mask&unix.STATX_MNT_ID == 0 || ordinary.Mnt_id != uint64(m.ID) {
		return f, ErrUnavailable
	}
	// /proc mount IDs and device numbers may be recycled immediately after
	// unmount. The kernel's unique mount ID distinguishes those lifetimes
	// without changing identity across a gateway restart.
	if unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID_UNIQUE, &unique) != nil ||
		unique.Mask&unix.STATX_MNT_ID_UNIQUE == 0 || unique.Mnt_id == 0 {
		return f, ErrUnavailable
	}
	f.MountInstance = unique.Mnt_id
	return f, nil
}

func readKernel() (kernelFacts, error) {
	var f kernelFacts
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return f, ErrUnavailable
	}
	defer func() { _ = file.Close() }()
	f.Mounts, err = sealedstate.ParseMountInfo(file)
	if err != nil {
		return f, ErrUnavailable
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return f, ErrUnavailable
	}
	f.BootID = strings.TrimSpace(string(boot))
	if info, err := os.Stat("/dev/fuse"); err == nil {
		f.FuseDevice = info.Mode()&os.ModeCharDevice != 0
	}
	allow, _ := os.ReadFile("/sys/module/fuse/parameters/allow_sys_admin_access")
	f.FuseAllowSysAdminAccess = strings.TrimSpace(string(allow))
	f.SwapFacts, err = sealedstate.ReadSwapFacts()
	if err != nil {
		return f, ErrUnavailable
	}
	resume, err := os.ReadFile("/sys/power/resume")
	if err != nil && !os.IsNotExist(err) {
		return f, ErrUnavailable
	}
	f.ResumeDevice = strings.TrimSpace(string(resume))
	return f, nil
}

func privateDirectory(path string, uid int) bool { return privatePath(path, uid, true) }
func privateFile(path string, uid int) bool      { return privatePath(path, uid, false) }

func privatePath(path string, uid int, directory bool) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return false
	}
	var st unix.Stat_t
	if unix.Lstat(path, &st) != nil || int(st.Uid) != uid || st.Mode&0077 != 0 {
		return false
	}
	if directory {
		return st.Mode&unix.S_IFMT == unix.S_IFDIR
	}
	return st.Mode&unix.S_IFMT == unix.S_IFREG
}

func (c *Controller) volumeMount(f kernelFacts) (sealedstate.Mount, bool, error) {
	if !f.FuseDevice || f.FuseAllowSysAdminAccess != "N" || len(sealedstate.SwapFailures(f.SwapFacts, c.cfg.SwapPolicy)) > 0 ||
		(f.ResumeDevice != "" && f.ResumeDevice != "0:0") || f.BootID == "" {
		return sealedstate.Mount{}, false, ErrUnsafeStorage
	}
	m, ok := sealedstate.MountFor(f.Mounts, c.cfg.MountPoint)
	if !ok {
		return m, false, ErrUnavailable
	}
	if m.MountPoint != c.cfg.MountPoint {
		for _, nested := range f.Mounts {
			if within(nested.MountPoint, c.cfg.MountPoint) {
				return m, false, ErrUnsafeStorage
			}
		}
		return m, false, nil
	}
	opts := map[string]string{}
	for _, s := range strings.Split(m.SuperOpts, ",") {
		k, v, _ := strings.Cut(s, "=")
		opts[k] = v
	}
	_, other := opts["allow_other"]
	_, root := opts["allow_root"]
	if m.FSType != "fuse.gocryptfs" || m.Source != c.cfg.CipherDir || m.Root != "/" || other || root || opts["user_id"] != strconv.Itoa(c.cfg.OwnerUID) {
		return m, false, ErrUnsafeStorage
	}
	return m, true, nil
}

func (c *Controller) observe(ctx context.Context) (State, error) {
	s := State{Storage: "unknown", Runtime: "unknown"}
	f, err := c.kernel()
	if err != nil || f.MountInstance == 0 {
		return s, ErrUnavailable
	}
	m, open, err := c.volumeMount(f)
	if err != nil {
		return s, err
	}
	if open {
		s.Storage = "open"
		file, err := os.Open(c.cfg.MountPoint)
		if err != nil {
			s.Storage = "unknown"
			return s, ErrUnavailable
		}
		_, err = file.Readdirnames(1)
		_ = file.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			s.Storage = "unknown"
			return s, ErrUnavailable
		}
	} else {
		s.Storage = "closed"
	}
	config := filepath.Join(c.cfg.CipherDir, "gocryptfs.conf")
	if _, err := os.Lstat(config); err == nil {
		if !privateFile(config, c.cfg.OwnerUID) {
			return s, ErrUnsafeStorage
		}
		s.Configured = true
	} else if !os.IsNotExist(err) {
		return s, ErrUnavailable
	}
	if open && !s.Configured {
		s.Storage = "unknown"
		return s, ErrUnsafeStorage
	}
	runtime, identity, err := c.inspectRuntime(ctx)
	s.Runtime = runtime
	if err != nil {
		return s, err
	}
	if !open && runtime != "stopped" {
		s.Storage = "unknown"
		return s, ErrUnsafeStorage
	}
	if !open && s.Configured {
		if err := c.projectGone(ctx); err != nil {
			return s, err
		}
		if err := c.noAliases(f.Mounts); err != nil {
			s.Storage = "unknown"
			return s, err
		}
		if err := c.closedProcesses(ctx); err != nil {
			s.Storage = "unknown"
			return s, err
		}
		s.Locked = true
	}
	s.Epoch = epoch(f.BootID, m.ID, f.MountInstance, m.Device, identity)
	return s, nil
}

const inspectFormat = `{"id":{{json .Id}},"running":{{.State.Running}},"pid":{{.State.Pid}},"status":{{json .State.Status}},"startedAt":{{json .State.StartedAt}},"health":{{json .State.Healthcheck.Status}},"project":{{json (index .Config.Labels "com.docker.compose.project")}},"podmanProject":{{json (index .Config.Labels "io.podman.compose.project")}}}`

var containerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (c *Controller) inspectRuntime(ctx context.Context) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rootless, err := c.command(ctx, c.cfg.PodmanPath, []string{"info", "--format", "{{.Host.Security.Rootless}}"}, nil, true)
	if err != nil || strings.TrimSpace(string(rootless)) != "true" {
		return "unknown", "", ErrUnavailable
	}
	_, err = c.run.run(ctx, c.cfg.PodmanPath, []string{"container", "exists", c.cfg.AppContainer}, c.environment(), nil, true)
	if exitCode(err) == 1 {
		return "stopped", "absent", nil
	}
	if err != nil {
		return "unknown", "", ErrUnavailable
	}
	out, err := c.command(ctx, c.cfg.PodmanPath, []string{"inspect", "--format", inspectFormat, c.cfg.AppContainer}, nil, true)
	if err != nil {
		return "unknown", "", ErrUnavailable
	}
	var v struct {
		ID            string
		Running       bool
		PID           int
		Status        string
		StartedAt     string
		Health        string
		Project       string
		PodmanProject string
	}
	if json.Unmarshal(out, &v) != nil || !containerID.MatchString(v.ID) ||
		(v.Project != c.cfg.ComposeProject && v.PodmanProject != c.cfg.ComposeProject) ||
		(v.Project != "" && v.Project != c.cfg.ComposeProject) || (v.PodmanProject != "" && v.PodmanProject != c.cfg.ComposeProject) {
		return "unknown", "", ErrUnavailable
	}
	identity := fmt.Sprintf("%s/%d/%s", v.ID, v.PID, v.StartedAt)
	if !v.Running && v.PID == 0 && (v.Status == "exited" || v.Status == "stopped" || v.Status == "created" || v.Status == "configured") {
		return "stopped", identity, nil
	}
	started, err := time.Parse(time.RFC3339Nano, v.StartedAt)
	if !v.Running || v.Status != "running" || v.PID <= 0 || err != nil || started.IsZero() {
		return "unknown", "", ErrUnavailable
	}
	if v.Health == "healthy" {
		// A healthy process is insufficient after a wrapper interruption:
		// cross-check its current mounts against the launch-bound host report.
		if _, err := c.command(ctx, c.cfg.PodmanPath, []string{"exec", c.cfg.AppContainer, "/usr/local/bin/noise", "deck", "sealed-check"}, nil, false); err != nil {
			return "unknown", "", ErrUnsafeStorage
		}
		if c.cfg.WorkspaceGateway {
			if err := c.verifyGatewayIngress(ctx); err != nil {
				return "unknown", "", ErrUnsafeStorage
			}
		}
		return "running", identity, nil
	}
	if v.Health == "starting" {
		return "starting", identity, nil
	}
	return "unknown", "", ErrUnavailable
}

func (c *Controller) prepareStores() error {
	fd, err := unix.Open(c.cfg.MountPoint, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrUnsafeStorage
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil {
		return ErrUnsafeStorage
	}
	f, err := c.kernel()
	if err != nil {
		return ErrUnsafeStorage
	}
	m, open, err := c.volumeMount(f)
	device := fmt.Sprintf("%d:%d", unix.Major(uint64(st.Dev)), unix.Minor(uint64(st.Dev)))
	if err != nil || !open || m.Device != device {
		return ErrUnsafeStorage
	}
	mountDevice := st.Dev
	// Keep the opened mount alive while creating stores. If it disappears
	// from the path, mkdirat cannot fall through into the plaintext directory.
	for _, name := range []string{"data", "home", "projects", "tmp", "gh"} {
		if err := unix.Mkdirat(fd, name, 0700); err != nil && err != unix.EEXIST {
			return ErrUnsafeStorage
		}
		child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return ErrUnsafeStorage
		}
		err = unix.Fstat(child, &st)
		if err != nil || int(st.Uid) != c.cfg.OwnerUID || st.Mode&0077 != 0 || st.Dev != mountDevice {
			_ = unix.Close(child)
			return ErrUnsafeStorage
		}
		if name == "data" && c.cfg.WorkspaceGateway {
			err = c.prepareGateway(child, mountDevice)
		}
		_ = unix.Close(child)
		if err != nil {
			return ErrUnsafeStorage
		}
	}
	return nil
}

func (c *Controller) prepareGateway(dataFD int, device uint64) error {
	// Rootless keep-id maps this host owner to the runtime's container UID.
	// Creating through the open FUSE descriptors never targets host UID 1026
	// or falls through into plaintext if the mountpoint is detached.
	if err := unix.Mkdirat(dataFD, "gateway", 0700); err != nil && err != unix.EEXIST {
		return ErrUnsafeStorage
	}
	fd, err := unix.Openat(dataFD, "gateway", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrUnsafeStorage
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || int(st.Uid) != c.cfg.OwnerUID || st.Dev != device || st.Mode&0077 != 0 {
		return ErrUnsafeStorage
	}
	return nil
}
