//go:build linux

package workspaceruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/noisefloor-oss/deck/internal/workspacemigrate"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This exercises real gocryptfs only when explicitly invoked on a prepared
// Linux host. It neither launches Deck nor certifies the ordinary-root-read
// boundary; those require the deployment qualification packet.
func TestNativeHostEncryptedRoundTrip(t *testing.T) {
	if os.Getenv("NOISEDECK_NATIVE_HOST_TEST") != "1" {
		t.Skip("native host probe is opt-in; run deploy/test-workspace-runtime-host.sh")
	}
	if os.Geteuid() == 0 {
		t.Fatal("native host probe must run as the dedicated non-root owner")
	}
	bins := map[string]string{}
	for _, name := range []string{"gocryptfs", "podman", "fusermount3"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("native host probe requires %s", name)
		}
		bins[name] = path
	}
	root := t.TempDir()
	for _, name := range []string{"cipher", "plain", "control"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		t.Fatal("XDG_RUNTIME_DIR must name the owner's runtime directory")
	}
	wrapper, err := filepath.Abs("../../deploy/sealed-compose.sh")
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	name := "deck-native-test-" + hex.EncodeToString(nonce)
	cfg := Config{InstanceID: name, OwnerUID: os.Geteuid(), CipherDir: filepath.Join(root, "cipher"), MountPoint: filepath.Join(root, "plain"), ControlDir: filepath.Join(root, "control"), HomeDir: home, RuntimeDir: runtimeDir, GocryptfsPath: bins["gocryptfs"], PodmanPath: bins["podman"], SealedComposePath: wrapper, ComposeEnvFile: filepath.Join(root, "unused-compose.env"), ComposeProject: name, AppContainer: name + "-app-1"}
	if err := os.WriteFile(cfg.ComposeEnvFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	const marker = "synthetic workspace round-trip data"
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0700); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(snapshot, "tree")
	if err := os.Mkdir(tree, 0700); err != nil {
		t.Fatal(err)
	}
	for _, store := range []string{"data", "home", "projects", "gh"} {
		if err := os.Mkdir(filepath.Join(tree, store), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tree, "data", "probe"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenRoot(tree)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := workspacemigrate.ManifestFor(t.Context(), source)
	_ = source.Close()
	if err != nil {
		t.Fatal(err)
	}
	digest, raw, err := workspacemigrate.Digest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Migration = &MigrationConfig{SnapshotDir: snapshot, ManifestSHA256: digest}
	c, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mounted := false
	t.Cleanup(func() {
		if mounted {
			if err := exec.Command(bins["fusermount3"], "-u", cfg.MountPoint).Run(); err != nil {
				t.Error("native test mount could not be closed")
			}
		}
		if c != nil {
			_ = c.Close()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	defer clear(secret)
	op, err := c.Initialize(ctx, "initialize", secret)
	if op.State.Storage == "open" {
		mounted = true
	}
	if err != nil || op.Status != "succeeded" {
		t.Fatalf("native initialize did not succeed: status=%s error=%v", op.Status, err)
	}
	if _, err := c.Start(ctx, "blocked-start"); !errors.Is(err, ErrMigrationRequired) {
		t.Fatal("native migration admitted start before verified import", err)
	}
	if err := c.Import(ctx); err != nil {
		t.Fatal("native encrypted import failed", err)
	}
	original, err := os.ReadFile(filepath.Join(tree, "data", "probe"))
	if err != nil || string(original) != marker {
		t.Fatal("native import changed source")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := c.Import(ctx); err != nil {
		t.Fatal("native receipt lost across controller restart", err)
	}
	before := op.State.Epoch
	if err := exec.Command(bins["fusermount3"], "-u", cfg.MountPoint).Run(); err != nil {
		t.Fatal("native nonlazy unmount failed")
	}
	mounted = false
	state, err := c.Observe(ctx)
	if err != nil || state.Storage != "closed" {
		t.Fatalf("native closed observation: %+v %v", state, err)
	}
	if err := filepath.WalkDir(cfg.CipherDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(marker)) {
			t.Fatal("synthetic plaintext appeared in ciphertext backing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	op, err = c.Unlock(ctx, "unlock", secret)
	if op.State.Storage == "open" {
		mounted = true
	}
	if err != nil || op.Status != "succeeded" || op.State.Epoch == before {
		t.Fatalf("native unlock did not establish a new mount: status=%s error=%v", op.Status, err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.MountPoint, "data", "probe"))
	if err != nil || string(data) != marker {
		t.Fatal("native encrypted round trip did not restore synthetic data")
	}
}
