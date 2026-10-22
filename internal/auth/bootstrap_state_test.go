package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestEnsureBootstrapStateCreatesPrivateStateOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "auth-state.json")
	created, err := EnsureBootstrapState(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if !created.Created || len(created.State.PasswordHash) != 0 {
		t.Fatalf("create result = %+v", created)
	}
	if len(created.State.SessionSecret) != sessionSecretBytes {
		t.Fatalf("session secret length = %d", len(created.State.SessionSecret))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state mode = %04o, want 0600", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("setup#token=")) {
		t.Fatal("state retained a plaintext token")
	}

	loaded, err := EnsureBootstrapState(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Created {
		t.Fatalf("restart redisclosed bootstrap credential: %+v", loaded)
	}
	if !bytes.Equal(loaded.State.PasswordHash, created.State.PasswordHash) ||
		!bytes.Equal(loaded.State.SessionSecret, created.State.SessionSecret) {
		t.Fatal("restart did not preserve durable authentication state")
	}
}

func TestEnsureBootstrapStateAcceptsPinnedHashWithoutPlaintext(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("operator-selected"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "auth-state.json")
	result, err := EnsureBootstrapState(path, string(hash))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created {
		t.Fatalf("pinned result = %+v", result)
	}
	if !bytes.Equal(result.State.PasswordHash, hash) {
		t.Fatal("pinned hash changed")
	}
	if _, err := EnsureBootstrapState(path, string(hash)); err != nil {
		t.Fatalf("matching pinned hash on restart: %v", err)
	}
	other, err := bcrypt.GenerateFromPassword([]byte("different"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureBootstrapState(path, string(other)); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched pinned hash error = %v", err)
	}
}

func TestEnsureBootstrapStateRejectsInsecureOrMalformedExistingState(t *testing.T) {
	t.Run("permissions", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auth-state.json")
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureBootstrapState(path, ""); err == nil || !strings.Contains(err.Error(), "private file") {
			t.Fatalf("permission error = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "auth-state.json")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureBootstrapState(path, ""); err == nil || !strings.Contains(err.Error(), "regular private file") {
			t.Fatalf("symlink error = %v", err)
		}
	})
	t.Run("truncated", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auth-state.json")
		if err := os.WriteFile(path, []byte(`{"version":1`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureBootstrapState(path, ""); err == nil || !strings.Contains(err.Error(), "decode") {
			t.Fatalf("decode error = %v", err)
		}
	})
}

func TestEnsureBootstrapStateRefusesInvalidPinnedHashBeforeWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-state.json")
	if _, err := EnsureBootstrapState(path, "plaintext"); err == nil || !strings.Contains(err.Error(), "supplied password hash") {
		t.Fatalf("invalid hash error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid hash created state: %v", err)
	}
}
