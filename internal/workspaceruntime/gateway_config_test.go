//go:build linux

package workspaceruntime

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestGatewayOptInIsPinnedAndDoesNotInheritAmbientConfiguration(t *testing.T) {
	f := newFixture(t)
	t.Setenv("NOISEDECK_WORKSPACE_GATEWAY", "true")
	if slices.Contains(f.c.environment(), "NOISEDECK_WORKSPACE_GATEWAY=true") {
		t.Fatal("ambient configuration enabled the workspace gateway")
	}
	f.c.cfg.WorkspaceGateway = true
	if !slices.Contains(f.c.environment(), "NOISEDECK_WORKSPACE_GATEWAY=true") {
		t.Fatal("explicit gateway opt-in was not passed to the fixed wrapper")
	}
	if err := f.c.Close(); err != nil {
		t.Fatal(err)
	}
	f.c = nil
	f.cfg.WorkspaceGateway = true
	if c, err := open(f.cfg, f, func() (kernelFacts, error) { return f.facts, nil }); !errors.Is(err, ErrConfiguration) {
		if c != nil {
			c.Close()
		}
		t.Fatal("gateway opt-in changed outside the pinned installation", err)
	}
}

func TestGatewaySocketDirectoryRequiresPrivateEncryptedParent(t *testing.T) {
	for _, state := range []string{"open", "disabled", "closed", "symlink", "public"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t)
			f.c.cfg.WorkspaceGateway = state != "disabled"
			f.configured()
			if state != "closed" {
				f.mounted()
			}
			gateway := filepath.Join(f.cfg.MountPoint, "data", "gateway")
			if state == "symlink" || state == "public" {
				if err := os.Mkdir(filepath.Dir(gateway), 0700); err != nil {
					t.Fatal(err)
				}
				if state == "symlink" {
					if err := os.Symlink(t.TempDir(), gateway); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(gateway, 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(gateway, 0755); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := f.c.prepareStores()
			if state == "open" {
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(gateway)
				if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !privateDirectory(gateway, f.cfg.OwnerUID) {
					t.Fatal("gateway directory is not private to the mapped host owner", err)
				}
			} else if state == "disabled" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(gateway); !os.IsNotExist(err) {
					t.Fatal("disabled gateway created a socket directory")
				}
			} else if err == nil {
				t.Fatal("unsafe gateway directory was accepted")
			}
			if state == "closed" {
				if _, err := os.Lstat(filepath.Dir(gateway)); !os.IsNotExist(err) {
					t.Fatal("locked preparation wrote a plaintext fallback")
				}
			}
		})
	}
}
