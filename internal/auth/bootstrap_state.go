package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sys/unix"
)

const (
	bootstrapStateVersion = 1
	setupTokenBytes       = 32
	SetupTokenTTL         = 30 * time.Minute
	sessionSecretBytes    = 32
	passwordHashCost      = 12
)

// BootstrapState is the runtime authentication material consumed by the
// password handler. It contains no plaintext credential.
type BootstrapState struct {
	PasswordHash  []byte
	SessionSecret []byte
	SetupHash     []byte
	SetupExpires  time.Time
}

// BootstrapResult reports whether this boot created durable authentication state.
type BootstrapResult struct {
	State   BootstrapState
	Created bool
}

type bootstrapStateFile struct {
	Version       int       `json:"version"`
	PasswordHash  string    `json:"password_hash"`
	SessionSecret string    `json:"session_secret"`
	SetupHash     []byte    `json:"setup_hash,omitempty"`
	SetupExpires  time.Time `json:"setup_expires,omitempty"`
}

// EnsureBootstrapState loads or atomically creates password-mode state. The
// caller must hold the process-wide backend lock, which serializes creation.
func EnsureBootstrapState(path, suppliedHash string) (BootstrapResult, error) {
	if path == "" {
		return BootstrapResult{}, errors.New("auth: bootstrap state path is empty")
	}
	if suppliedHash != "" {
		if _, err := bcrypt.Cost([]byte(suppliedHash)); err != nil {
			return BootstrapResult{}, fmt.Errorf("auth: supplied password hash: %w", err)
		}
	}
	if _, err := os.Lstat(path); err == nil {
		result, readErr := readBootstrapState(path)
		if readErr != nil {
			return BootstrapResult{}, readErr
		}
		if suppliedHash != "" && suppliedHash != string(result.State.PasswordHash) {
			return BootstrapResult{}, errors.New("auth: supplied password hash does not match durable bootstrap state")
		}
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return BootstrapResult{}, fmt.Errorf("auth: inspect bootstrap state: %w", err)
	}

	secret := make([]byte, sessionSecretBytes)
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		return BootstrapResult{}, fmt.Errorf("auth: generate session secret: %w", err)
	}
	state := BootstrapState{PasswordHash: []byte(suppliedHash), SessionSecret: secret}
	if err := saveBootstrapState(path, state, false); err != nil {
		return BootstrapResult{}, err
	}
	return BootstrapResult{State: state, Created: true}, nil
}

// EnsureSetupURL preserves an unexpired local reset link until you can open it.
func EnsureSetupURL(path, origin string, now time.Time) (string, error) {
	result, err := readBootstrapState(path)
	if err != nil {
		return "", err
	}
	if len(result.State.PasswordHash) != 0 {
		if err := os.Remove(SetupURLPath(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		return "", nil
	}
	f, err := openPrivateFile(SetupURLPath(path))
	if err == nil {
		raw, readErr := io.ReadAll(io.LimitReader(f, 2048))
		_ = f.Close()
		if readErr != nil {
			return "", readErr
		}
		link := strings.TrimSpace(string(raw))
		token, ok := strings.CutPrefix(link, origin+"/setup#token=")
		if ok && validSetupToken(result.State, token, now) {
			return link, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return IssueSetupToken(path, origin, now)
}

// IssueSetupToken replaces any previous link. The caller holds the backend lock.
func IssueSetupToken(path, origin string, now time.Time) (string, error) {
	result, err := readBootstrapState(path)
	if err != nil {
		return "", err
	}
	if len(result.State.PasswordHash) != 0 {
		return "", errors.New("auth: a password is already set")
	}
	token, err := randomText(setupTokenBytes)
	if err != nil {
		return "", fmt.Errorf("auth: generate setup token: %w", err)
	}
	sum := sha256.Sum256([]byte(token))
	result.State.SetupHash = sum[:]
	result.State.SetupExpires = now.Add(SetupTokenTTL)
	if err := saveBootstrapState(path, result.State, true); err != nil {
		return "", err
	}
	link := origin + "/setup#token=" + token
	if err := writePrivate(SetupURLPath(path), []byte(link+"\n"), "setup link", ".setup-url-*", true); err != nil {
		return "", err
	}
	return link, nil
}

func SetupURLPath(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "setup-url")
}

// ResetPassword invalidates existing sessions. Deck must be stopped.
func ResetPassword(path, origin string, now time.Time) (string, error) {
	if _, err := readBootstrapState(path); err != nil {
		return "", err
	}
	secret := make([]byte, sessionSecretBytes)
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		return "", err
	}
	if err := saveBootstrapState(path, BootstrapState{SessionSecret: secret}, true); err != nil {
		return "", err
	}
	return IssueSetupToken(path, origin, now)
}

func validSetupToken(state BootstrapState, token string, now time.Time) bool {
	sum := sha256.Sum256([]byte(token))
	match := subtle.ConstantTimeCompare(sum[:], state.SetupHash) == 1
	return match && token != "" && now.Before(state.SetupExpires) && len(state.PasswordHash) == 0
}

func saveBootstrapState(path string, state BootstrapState, replace bool) error {
	body, err := json.Marshal(bootstrapStateFile{Version: bootstrapStateVersion,
		PasswordHash: string(state.PasswordHash), SessionSecret: base64.RawURLEncoding.EncodeToString(state.SessionSecret),
		SetupHash: state.SetupHash, SetupExpires: state.SetupExpires})
	if err != nil {
		return err
	}
	return writePrivate(path, append(body, '\n'), "bootstrap state", ".auth-state-*", replace)
}

func randomText(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func readBootstrapState(path string) (BootstrapResult, error) {
	f, err := openPrivateFile(path)
	if err != nil {
		return BootstrapResult{}, err
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(io.LimitReader(f, 16<<10))
	dec.DisallowUnknownFields()
	var stored bootstrapStateFile
	if err := dec.Decode(&stored); err != nil {
		return BootstrapResult{}, fmt.Errorf("auth: decode bootstrap state: %w", err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return BootstrapResult{}, errors.New("auth: bootstrap state contains trailing data")
	}
	if len(stored.SetupHash) != 0 && len(stored.SetupHash) != sha256.Size {
		return BootstrapResult{}, errors.New("auth: invalid setup hash")
	}
	if stored.Version != bootstrapStateVersion {
		return BootstrapResult{}, fmt.Errorf("auth: unsupported bootstrap state version %d", stored.Version)
	}
	if _, err := bcrypt.Cost([]byte(stored.PasswordHash)); stored.PasswordHash != "" && err != nil {
		return BootstrapResult{}, fmt.Errorf("auth: stored password hash: %w", err)
	}
	secret, err := base64.RawURLEncoding.DecodeString(stored.SessionSecret)
	if err != nil || len(secret) != sessionSecretBytes {
		return BootstrapResult{}, errors.New("auth: stored session secret is invalid")
	}
	return BootstrapResult{State: BootstrapState{
		PasswordHash: []byte(stored.PasswordHash), SessionSecret: secret, SetupHash: stored.SetupHash, SetupExpires: stored.SetupExpires,
	}}, nil
}

func openPrivateFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("auth: open regular private file: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = f.Close()
		return nil, errors.New("auth: authentication state must be a regular private file (mode 0600)")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = f.Close()
		return nil, err
	}
	if int64(stat.Uid) != int64(os.Geteuid()) {
		_ = f.Close()
		return nil, errors.New("auth: run as the service user that owns the authentication state")
	}
	return f, nil
}

func writePrivateOnce(path string, body []byte, label, tempPattern string) error {
	return writePrivate(path, body, label, tempPattern, false)
}

func writePrivate(path string, body []byte, label, tempPattern string, replace bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("auth: create %s directory: %w", label, err)
	}
	tmp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return fmt.Errorf("auth: create %s temporary file: %w", label, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	failed := true
	defer func() {
		if failed {
			_ = tmp.Close()
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("auth: protect %s: %w", label, err)
	}
	if _, err := tmp.Write(body); err != nil {
		return fmt.Errorf("auth: write %s: %w", label, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("auth: sync %s: %w", label, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("auth: close %s: %w", label, err)
	}
	publish := os.Link
	if replace {
		publish = os.Rename
	}
	if err := publish(tmpName, path); err != nil {
		return fmt.Errorf("auth: publish %s: %w", label, err)
	}
	d, err := os.Open(dir) // #nosec G304 -- derived from the user-owned runtime database path.
	if err != nil {
		return fmt.Errorf("auth: open %s directory: %w", label, err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("auth: sync %s directory: %w", label, err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("auth: close %s directory: %w", label, err)
	}
	failed = false
	return nil
}
