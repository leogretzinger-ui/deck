package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const installationModeVersion = 1

type installationModeFile struct {
	Version int    `json:"version"`
	Mode    string `json:"mode"`
}

// InstallationModePath keeps the auth-mode receipt beside the control
// database, in the same durable installation-owned data root.
func InstallationModePath(databasePath string) string {
	return filepath.Join(filepath.Dir(databasePath), "auth-mode.json")
}

// EnsureInstallationMode establishes the configured mode for a fresh
// installation or verifies it against the durable receipt. An existing
// pre-receipt installation must be adopted explicitly while offline.
func EnsureInstallationMode(path, configuredMode string, existingInstallation bool) error {
	stored, err := ReadInstallationMode(path)
	switch {
	case err == nil:
		if stored != configuredMode {
			return fmt.Errorf("auth: configured mode %q does not match installation receipt %q; stop Deck and run `noise deck auth-mode transition --from %s --to %s`", configuredMode, stored, stored, configuredMode)
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return err
	case existingInstallation:
		return fmt.Errorf("auth: existing installation has no auth-mode receipt; keep Deck stopped and run `noise deck auth-mode adopt --mode %s` before startup", configuredMode)
	default:
		return writeInstallationModeOnce(path, configuredMode)
	}
}

// AdoptInstallationMode writes the one-time receipt for an installation made
// by a pre-receipt release. It never changes an existing receipt.
func AdoptInstallationMode(path, mode string, existingInstallation bool) error {
	if !existingInstallation {
		return errors.New("auth: cannot adopt an auth mode for a fresh installation; start Deck to create its receipt")
	}
	if _, err := ReadInstallationMode(path); err == nil {
		return errors.New("auth: installation already has an auth-mode receipt; use `noise deck auth-mode transition` to change it")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeInstallationModeOnce(path, mode)
}

// TransitionInstallationMode atomically replaces an existing receipt. The
// caller must hold the backend lock so no service can observe the transition.
func TransitionInstallationMode(path, from, to string) error {
	stored, err := ReadInstallationMode(path)
	if err != nil {
		return err
	}
	if stored != from {
		return fmt.Errorf("auth: installation receipt is %q, not requested source mode %q", stored, from)
	}
	if from == to {
		return errors.New("auth: source and target auth modes are identical")
	}
	return replaceInstallationMode(path, to)
}

// ReadInstallationMode validates that a receipt is an owner-private regular
// file containing one canonical mode and no unrecognized data.
func ReadInstallationMode(path string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0) // #nosec G304 -- derived from the validated user-owned database path.
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = unix.Close(fd)
		return "", errors.New("auth: open installation mode receipt")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("auth: inspect installation mode receipt: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("auth: installation mode receipt %q must be a regular private file (mode 0600)", path)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return "", fmt.Errorf("auth: inspect installation mode receipt owner: %w", err)
	}
	if int64(stat.Uid) != int64(os.Geteuid()) {
		return "", fmt.Errorf("auth: installation mode receipt %q must be owned by the runtime user", path)
	}
	dec := json.NewDecoder(io.LimitReader(f, 4<<10))
	dec.DisallowUnknownFields()
	var stored installationModeFile
	if err := dec.Decode(&stored); err != nil {
		return "", fmt.Errorf("auth: decode installation mode receipt: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errors.New("auth: installation mode receipt contains trailing data")
	}
	if stored.Version != installationModeVersion {
		return "", fmt.Errorf("auth: unsupported installation mode receipt version %d", stored.Version)
	}
	if !canonicalAuthMode(stored.Mode) {
		return "", fmt.Errorf("auth: installation mode receipt contains invalid mode %q", stored.Mode)
	}
	return stored.Mode, nil
}

func canonicalAuthMode(mode string) bool {
	switch mode {
	case "none", "password", "oidc", "trusted_proxy":
		return true
	default:
		return false
	}
}

func installationModeBody(mode string) ([]byte, error) {
	if !canonicalAuthMode(mode) {
		return nil, fmt.Errorf("auth: mode %q is not canonical", mode)
	}
	body, err := json.Marshal(installationModeFile{Version: installationModeVersion, Mode: mode})
	if err != nil {
		return nil, fmt.Errorf("auth: encode installation mode receipt: %w", err)
	}
	return append(body, '\n'), nil
}

func writeInstallationModeOnce(path, mode string) error {
	body, err := installationModeBody(mode)
	if err != nil {
		return err
	}
	return writePrivateOnce(path, body, "installation mode receipt", ".auth-mode-*")
}

func replaceInstallationMode(path, mode string) error {
	body, err := installationModeBody(mode)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".auth-mode-*")
	if err != nil {
		return fmt.Errorf("auth: create installation mode temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("auth: protect installation mode receipt: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("auth: write installation mode receipt: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("auth: sync installation mode receipt: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("auth: close installation mode receipt: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("auth: replace installation mode receipt: %w", err)
	}
	d, err := os.Open(dir) // #nosec G304 -- derived from the validated user-owned database path.
	if err != nil {
		return fmt.Errorf("auth: open installation mode directory: %w", err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("auth: sync installation mode directory: %w", err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("auth: close installation mode directory: %w", err)
	}
	return nil
}
