//go:build linux

// Command runner serializes the real Codex probe and reaps any exact process
// identities left by a prior timeout or interrupt before and after the test.
package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type processIdentity struct {
	PID        int    `json:"pid"`
	StartTicks int64  `json:"start_ticks"`
	BootID     string `json:"boot_id"`
}

type runMetadata struct {
	BootID        string `json:"boot_id"`
	EphemeralHome string `json:"ephemeral_home"`
}

var canonicalUUID = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

const maxPrivateMetadataBytes = 1 << 20

func main() {
	root, err := repositoryRoot()
	if err != nil {
		fatal(err)
	}
	bootID, err := currentBootID()
	if err != nil {
		fatal(err)
	}
	reviewRoot := filepath.Join(root, ".review")
	if err := ensurePrivateDir(reviewRoot); err != nil {
		fatal(fmt.Errorf("secure review scratch root: %w", err))
	}
	base := filepath.Join(reviewRoot, "ps")
	if err := ensurePrivateDir(base); err != nil {
		fatal(fmt.Errorf("secure private probe root: %w", err))
	}

	baseDir, err := openPrivateDir(base)
	if err != nil {
		fatal(fmt.Errorf("open private probe root: %w", err))
	}
	defer func() { _ = baseDir.Close() }()
	lock, err := openPrivateLockAt(baseDir, "run.lock")
	if err != nil {
		fatal(fmt.Errorf("open probe lock: %w", err))
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		fatal(fmt.Errorf("lock probe namespace: %w", err))
	}

	if err := cleanupRuns(base, bootID); err != nil {
		fatal(fmt.Errorf("pre-run cleanup failed: %w", err))
	}

	cmd := exec.Command("go", "test", "-v", "-p=1", "./scripts/provider-spike/conformance",
		"-run", "^TestCodex(ProviderContract|HookTrustPersistence)$", "-count=1", "-timeout", "20m")
	cmd.Dir = root
	cmd.Env = withEnv(os.Environ(), "NOISEDECK_REAL_CODEX", "1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The go command remains alive while its test binary runs. Inheriting this
	// open file description keeps the flock held even if the runner itself is
	// SIGKILLed, so another invocation cannot overlap the orphaned probe.
	cmd.ExtraFiles = []*os.File{lock}
	if err := cmd.Start(); err != nil {
		fatal(fmt.Errorf("start real Codex probe: %w", err))
	}

	done := make(chan struct{})
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-signals:
			if unixSignal, ok := sig.(syscall.Signal); ok {
				_ = syscall.Kill(-cmd.Process.Pid, unixSignal)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
		}
	}()

	waitErr := cmd.Wait()
	close(done)
	signal.Stop(signals)
	cleanupErr := cleanupRuns(base, bootID)

	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	if cleanupErr != nil {
		fmt.Fprintf(os.Stderr, "post-run cleanup failed: %v\n", cleanupErr)
		exitCode = 1
	}
	os.Exit(exitCode)
}

func cleanupRuns(base, bootID string) error {
	entries, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "c-") {
			continue
		}
		dir := filepath.Join(base, entry.Name())
		if err := cleanupRun(dir, bootID); err != nil {
			return fmt.Errorf("clean one private run: %w", err)
		}
	}
	return nil
}

func cleanupRun(dir, bootID string) error {
	if err := validatePrivateDir(dir); err != nil {
		return err
	}
	metadata, err := loadRunMetadata(dir)
	if err != nil {
		return err
	}
	if metadata.BootID != bootID {
		return removePriorBootRun(dir, metadata)
	}
	identities, err := loadIdentities(dir, metadata.BootID)
	certified := cleanupCertified(dir, metadata)
	if certified && err != nil {
		return errors.New("cleanup-certified run has invalid process identities")
	}
	for _, identity := range identities {
		if certified && hasIdentity(identity, bootID) {
			return errors.New("cleanup certification disagrees with a live process identity")
		}
		if !certified {
			terminate(identity, bootID, 2*time.Second)
		}
	}
	serverErr := stopPrivateServer(filepath.Join(dir, "tmux", "ac.sock"))
	identitySurvived := false
	for _, identity := range identities {
		if !certified {
			terminate(identity, bootID, 2*time.Second)
		}
		identitySurvived = identitySurvived || hasIdentity(identity, bootID)
	}
	homeErr := removeEphemeralHome(metadata.EphemeralHome, dir)
	if certified {
		if serverErr != nil || identitySurvived || homeErr != nil {
			return errors.New("cleanup certification could not be revalidated")
		}
		return os.RemoveAll(dir)
	}
	// Without a harness-published certification there may have been a hard
	// kill between process creation and exact-identity publication. Best-effort
	// cleanup is safe, but deleting the run would destroy the evidence needed
	// to resolve an untracked escaped descendant.
	return errors.New("uncertified current-boot run retained after best-effort cleanup")
}

func loadRunMetadata(dir string) (runMetadata, error) {
	nameBootID, ok := runNameBootID(filepath.Base(dir))
	if !ok {
		return runMetadata{}, errors.New("private run directory has no boot identity")
	}
	data, err := readPrivateFile(dir, "run.json")
	if errors.Is(err, os.ErrNotExist) {
		return runMetadata{BootID: nameBootID, EphemeralHome: ephemeralHomeForRun(dir)}, nil
	}
	if err != nil {
		return runMetadata{}, errors.New("private run metadata is unavailable")
	}
	var metadata runMetadata
	if err := json.Unmarshal(data, &metadata); err != nil ||
		!canonicalUUID.MatchString(metadata.BootID) || metadata.BootID != nameBootID ||
		metadata.EphemeralHome != ephemeralHomeForRun(dir) {
		return runMetadata{}, errors.New("private run metadata is invalid")
	}
	return metadata, nil
}

func cleanupCertified(dir string, metadata runMetadata) bool {
	data, err := readPrivateFile(dir, "cleanup-certified.json")
	if err != nil {
		return false
	}
	var certified runMetadata
	return json.Unmarshal(data, &certified) == nil && certified == metadata
}

func runNameBootID(name string) (string, bool) {
	const prefix = "c-"
	const encodedLength = 22
	if !strings.HasPrefix(name, prefix) || len(name) <= len(prefix)+encodedLength ||
		name[len(prefix)+encodedLength] != '-' {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(name[len(prefix) : len(prefix)+encodedLength])
	if err != nil || len(raw) != 16 {
		return "", false
	}
	hexID := hex.EncodeToString(raw)
	bootID := hexID[:8] + "-" + hexID[8:12] + "-" + hexID[12:16] + "-" +
		hexID[16:20] + "-" + hexID[20:]
	return bootID, canonicalUUID.MatchString(bootID)
}

func ephemeralHomeForRun(dir string) string {
	return filepath.Join("/dev/shm", "noisedeck-"+filepath.Base(dir))
}

func removeEphemeralHome(path, runDir string) error {
	if err := requireTmpfs("/dev/shm"); err != nil {
		return err
	}
	want := ephemeralHomeForRun(runDir)
	if path != want || !strings.HasPrefix(path, "/dev/shm/noisedeck-c-") {
		return errors.New("refusing unsafe in-memory provider-home cleanup")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return os.Remove(path)
	}
	return os.RemoveAll(path)
}

func requireTmpfs(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("in-memory scratch path is not a real directory")
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil || stat.Type != unix.TMPFS_MAGIC {
		return errors.New("in-memory scratch path is not tmpfs")
	}
	return nil
}

func ensurePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
		return validatePrivateDir(path)
	}
	if err != nil {
		return err
	}
	return validatePrivateDirInfo(path, info)
}

func validatePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return validatePrivateDirInfo(path, info)
}

func validatePrivateDirInfo(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("private scratch path is not a real directory")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("open private scratch directory without following links")
	}
	defer func() { _ = unix.Close(fd) }()
	var stat unix.Stat_t
	euid := os.Geteuid()
	if euid < 0 || uint64(euid) > uint64(^uint32(0)) {
		return errors.New("effective user id is outside Linux uid_t range")
	}
	ownerUID := uint32(euid) // #nosec G115 -- bounds checked immediately above.
	if err := unix.Fstat(fd, &stat); err != nil || stat.Uid != ownerUID ||
		stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("private scratch directory owner or type is unsafe")
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		return errors.New("secure private scratch directory permissions")
	}
	return nil
}

func openPrivateDir(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open private directory", Path: path, Err: err}
	}
	directory := os.NewFile(uintptr(fd), path)
	if directory == nil {
		_ = unix.Close(fd)
		return nil, errors.New("wrap private scratch directory")
	}
	if err := validatePrivateDirFile(directory); err != nil {
		_ = directory.Close()
		return nil, err
	}
	return directory, nil
}

func validatePrivateDirFile(directory *os.File) error {
	var stat unix.Stat_t
	ownerUID, err := effectiveUID()
	if err != nil {
		return err
	}
	if err := unix.Fstat(int(directory.Fd()), &stat); err != nil ||
		stat.Uid != ownerUID || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("private scratch directory owner or type is unsafe")
	}
	if err := unix.Fchmod(int(directory.Fd()), 0o700); err != nil {
		return errors.New("secure private scratch directory permissions")
	}
	return nil
}

func openPrivateLockAt(directory *os.File, name string) (*os.File, error) {
	if err := validatePrivateName(name); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(directory.Fd()), name,
		unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, &os.PathError{Op: "open private lock", Path: name, Err: err}
	}
	lock := os.NewFile(uintptr(fd), name)
	if lock == nil {
		_ = unix.Close(fd)
		return nil, errors.New("wrap private lock file")
	}
	if err := validatePrivateFile(lock, name, false); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

func readPrivateFile(dir, name string) ([]byte, error) {
	directory, err := openPrivateDir(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	return readPrivateFileAt(directory, name)
}

func readPrivateFileAt(directory *os.File, name string) ([]byte, error) {
	if err := validatePrivateName(name); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open private metadata", Path: name, Err: err}
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("wrap private metadata file")
	}
	defer func() { _ = file.Close() }()
	if err := validatePrivateFile(file, name, true); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPrivateMetadataBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read private metadata %s: %w", name, err)
	}
	if len(data) > maxPrivateMetadataBytes {
		return nil, errors.New("private metadata exceeds size limit")
	}
	return data, nil
}

func validatePrivateFile(file *os.File, name string, checkSize bool) error {
	var stat unix.Stat_t
	ownerUID, err := effectiveUID()
	if err != nil {
		return err
	}
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return fmt.Errorf("inspect private metadata %s: %w", name, err)
	}
	if stat.Uid != ownerUID || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return errors.New("private metadata owner, type, or link count is unsafe")
	}
	if stat.Mode&0o7777 != 0o600 {
		return errors.New("private metadata permissions are unsafe")
	}
	if checkSize && (stat.Size < 0 || stat.Size > maxPrivateMetadataBytes) {
		return errors.New("private metadata exceeds size limit")
	}
	return nil
}

func validatePrivateName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return errors.New("private metadata name is unsafe")
	}
	return nil
}

func effectiveUID() (uint32, error) {
	euid := os.Geteuid()
	if euid < 0 || uint64(euid) > uint64(^uint32(0)) {
		return 0, errors.New("effective user id is outside Linux uid_t range")
	}
	return uint32(euid), nil // #nosec G115 -- bounds checked immediately above.
}

func removePriorBootRun(dir string, metadata runMetadata) error {
	if err := removeEphemeralHome(metadata.EphemeralHome, dir); err != nil {
		return err
	}
	socketPath := filepath.Join(dir, "tmux", "ac.sock")
	if err := removePrivateSocketIfStale(socketPath); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func stopPrivateServer(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("private tmux path is not a socket")
	}
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return errors.New("tmux unavailable while a private server socket exists")
	}
	if err := exec.Command(tmuxPath, "-S", socketPath, "kill-server").Run(); err != nil {
		return removePrivateSocketIfStale(socketPath)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(socketPath); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return removePrivateSocketIfStale(socketPath)
}

func removePrivateSocketIfStale(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return errors.New("private tmux path is not a socket")
	}
	conn, dialErr := net.DialTimeout("unix", socketPath, 250*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		return errors.New("private tmux socket still has a live listener")
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, os.ErrNotExist) {
		return errors.New("cannot prove private tmux socket is stale")
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func loadIdentities(dir, bootID string) ([]processIdentity, error) {
	directory, err := openPrivateDir(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()

	var identities []processIdentity
	var identityErrs []error
	if data, err := readPrivateFileAt(directory, "processes.json"); err == nil {
		var manifest []processIdentity
		if err := json.Unmarshal(data, &manifest); err != nil {
			identityErrs = append(identityErrs, errors.New("sanitized process manifest is invalid"))
		} else {
			for _, identity := range manifest {
				if validProcessIdentity(identity, bootID) {
					identities = appendIdentity(identities, identity)
				} else {
					identityErrs = append(identityErrs,
						errors.New("sanitized process identity does not match private run metadata"))
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		identityErrs = append(identityErrs, err)
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return identities, errors.Join(append(identityErrs, err)...)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".identity.json") {
			continue
		}
		data, err := readPrivateFileAt(directory, entry.Name())
		if err != nil {
			identityErrs = append(identityErrs, err)
			continue
		}
		var identity processIdentity
		if err := json.Unmarshal(data, &identity); err != nil {
			identityErrs = append(identityErrs, errors.New("test-owned tool identity is invalid"))
			continue
		}
		if !validProcessIdentity(identity, bootID) {
			identityErrs = append(identityErrs,
				errors.New("sanitized process identity does not match private run metadata"))
			continue
		}
		identities = appendIdentity(identities, identity)
	}
	return identities, errors.Join(identityErrs...)
}

func validProcessIdentity(identity processIdentity, bootID string) bool {
	return identity.PID > 1 && identity.StartTicks > 0 && identity.BootID == bootID
}

func appendIdentity(items []processIdentity, identity processIdentity) []processIdentity {
	for _, item := range items {
		if item == identity {
			return items
		}
	}
	return append(items, identity)
}

func terminate(identity processIdentity, bootID string, timeout time.Duration) {
	if identity.PID <= 1 || identity.StartTicks <= 0 || !hasIdentity(identity, bootID) {
		return
	}
	_ = syscall.Kill(identity.PID, syscall.SIGTERM)
	deadline := time.Now().Add(timeout)
	for hasIdentity(identity, bootID) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if hasIdentity(identity, bootID) {
		_ = syscall.Kill(identity.PID, syscall.SIGKILL)
		deadline = time.Now().Add(time.Second)
		for hasIdentity(identity, bootID) && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
	}
}

func hasIdentity(identity processIdentity, bootID string) bool {
	if identity.BootID == "" || identity.BootID != bootID {
		return false
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(identity.PID), "stat"))
	if err != nil {
		return false
	}
	right := strings.LastIndexByte(string(data), ')')
	if right < 0 {
		return false
	}
	fields := strings.Fields(string(data)[right+1:])
	if len(fields) <= 19 {
		return false
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	return err == nil && ticks == identity.StartTicks
}

func currentBootID() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", errors.New("read Linux boot identity for exact process cleanup")
	}
	bootID := strings.TrimSpace(string(raw))
	if !canonicalUUID.MatchString(bootID) {
		return "", errors.New("linux boot identity is not a canonical UUID")
	}
	return bootID, nil
}

func repositoryRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(out))
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return "", errors.New("unsafe repository root")
	}
	return root, nil
}

func withEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
