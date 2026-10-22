//go:build linux

package workspacemigrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/noisefloor-oss/deck/internal/db"
	"gopkg.in/yaml.v3"
)

type sourceFixture struct {
	cfg   SourceConfig
	app   sourceContainer
	peers []sourceContainer
	root  string
}

func newSourceFixture(t *testing.T) *sourceFixture {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	f := &sourceFixture{root: base, cfg: SourceConfig{EnginePath: filepath.Join(base, "docker"), EngineSocket: "/var/run/docker.sock", HomeDir: base, RuntimeDir: base, ContainerID: strings.Repeat("a", 64), ComposeProject: "old-workspace", OutputDir: filepath.Join(base, "snapshot"), OwnerUID: os.Geteuid(), OwnerGID: os.Getegid()}}
	if f.cfg.OwnerUID == 0 {
		f.cfg.OwnerUID = 1000
		f.cfg.OwnerGID = 1000
		if err := os.Chown(base, 1000, 1000); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(f.cfg.EnginePath, []byte("synthetic engine path"), 0700); err != nil {
		t.Fatal(err)
	}
	f.app = sourceContainer{ID: f.cfg.ContainerID, Status: "exited", Restart: "no", Project: f.cfg.ComposeProject, Started: "previous-start", Finished: "observed-stop"}
	for name, dest := range map[string]string{"data": "/data", "home": "/home/agent", "projects": "/workspace", "gh": "/home/agent/.config/gh", "palace": "/home/agent/.mempalace"} {
		path := filepath.Join(base, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "opaque"), []byte("synthetic-"+name), 0600); err != nil {
			t.Fatal(err)
		}
		f.app.Mounts = append(f.app.Mounts, sourceMount{Type: "bind", Source: path, Destination: dest, RW: true})
	}
	d, err := db.Open(t.Context(), db.Config{Path: filepath.Join(base, "data", "noisedeck.db")})
	if err != nil {
		t.Fatal(err)
	}
	err = d.WithTx(t.Context(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id,name,canonical_path,created_at,last_used_at) VALUES('p','preserved','/workspace/project','now','now');
INSERT INTO sessions(id,project_id,provider,tmux_socket_path,tmux_session_name,status,native_session_id,created_at,updated_at) VALUES('s','p','claude','/run/noisedeck/tmux.sock','synthetic','interrupted','preserved-native-id','now','now');`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *sourceFixture) run(_ context.Context, args ...string) ([]byte, error) {
	if args[0] == "ps" {
		ids := []string{f.app.ID}
		for _, p := range f.peers {
			ids = append(ids, p.ID)
		}
		return []byte(strings.Join(ids, "\n")), nil
	}
	if args[0] == "inspect" {
		for _, p := range append([]sourceContainer{f.app}, f.peers...) {
			if p.ID == args[len(args)-1] {
				return json.Marshal(p)
			}
		}
	}
	return nil, ErrSource
}

func TestSnapshotRoundTripPreservesDataAndStopsOldGenerations(t *testing.T) {
	f := newSourceFixture(t)
	sourceDB := filepath.Join(f.root, "data", "noisedeck.db")
	original, err := os.ReadFile(sourceDB)
	if err != nil {
		t.Fatal(err)
	}
	socket, err := net.Listen("unix", filepath.Join(f.root, "home", "old.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	digest, err := snapshot(t.Context(), f.cfg, f.run)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.ReadFile(sourceDB)
	if err != nil || hashBytes(unchanged) != hashBytes(original) {
		t.Fatal("source database changed")
	}
	source, err := os.OpenRoot(f.cfg.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target := destination(t)
	if err := Import(t.Context(), source, target, digest); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"data/opaque": "synthetic-data", "home/opaque": "synthetic-home", "projects/opaque": "synthetic-projects", "gh/opaque": "synthetic-gh", "home/.mempalace/opaque": "synthetic-palace"} {
		got, err := target.ReadFile(name)
		if err != nil || string(got) != want {
			t.Fatalf("lost %s", name)
		}
	}
	if _, err := target.Lstat("home/old.sock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old socket was migrated")
	}
	d, err := db.Open(t.Context(), db.Config{Path: filepath.Join(target.Name(), "data", "noisedeck.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var generation, stopped int
	var identity, path string
	if err := d.Reader().QueryRowContext(t.Context(), `SELECT generation,workspace_stop_generation,native_session_id FROM sessions WHERE id='s'`).Scan(&generation, &stopped, &identity); err != nil {
		t.Fatal(err)
	}
	if stopped != generation || stopped == 0 || identity != "preserved-native-id" {
		t.Fatal("session normalization lost identity or allowed automatic restart")
	}
	if err := d.Reader().QueryRowContext(t.Context(), `SELECT canonical_path FROM projects WHERE id='p'`).Scan(&path); err != nil || path != "/workspace/project" {
		t.Fatal("project path changed")
	}
}

func TestSnapshotRefusesLiveWrongAndSharedSources(t *testing.T) {
	for _, mutation := range []string{"running", "restart", "wrong-project", "unknown-mount", "sidecar", "shared"} {
		t.Run(mutation, func(t *testing.T) {
			f := newSourceFixture(t)
			switch mutation {
			case "running":
				f.app.Running = true
				f.app.PID = 100
			case "restart":
				f.app.Restart = "always"
			case "wrong-project":
				f.app.Project = "someone-else"
			case "unknown-mount":
				f.app.Mounts = append(f.app.Mounts, sourceMount{Type: "bind", Source: f.root, Destination: "/unaccounted", RW: true})
			case "sidecar":
				f.peers = append(f.peers, sourceContainer{ID: strings.Repeat("b", 64), Project: f.cfg.ComposeProject, Running: true, PID: 200})
			case "shared":
				f.peers = append(f.peers, sourceContainer{ID: strings.Repeat("b", 64), Project: "other", Running: true, PID: 200, Mounts: []sourceMount{{Source: filepath.Join(f.root, "home"), Destination: "/shared", RW: true}}})
			}
			if _, err := snapshot(t.Context(), f.cfg, f.run); !errors.Is(err, ErrSource) {
				t.Fatal("unsafe source accepted", err)
			}
			if _, err := os.Lstat(f.cfg.OutputDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refusal already created output")
			}
		})
	}
}

func TestSnapshotRejectsSourceRestartDuringCopy(t *testing.T) {
	f := newSourceFixture(t)
	observations := 0
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			observations++
			if observations == 3 {
				f.app.Finished = "another-stop"
			}
		}
		return f.run(ctx, args...)
	}
	if _, err := snapshot(t.Context(), f.cfg, run); !errors.Is(err, ErrSource) {
		t.Fatal("restarted source accepted", err)
	}
	if _, err := os.Lstat(filepath.Join(f.cfg.OutputDir, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restarted source produced an importable snapshot")
	}
}

func TestSnapshotRefusesActiveConfinedAuthority(t *testing.T) {
	f := newSourceFixture(t)
	d, err := db.Open(t.Context(), db.Config{Path: filepath.Join(f.root, "data", "noisedeck.db")})
	if err != nil {
		t.Fatal(err)
	}
	err = d.WithTx(t.Context(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO project_security_domains(security_domain_id,display_label,created_at,created_by) VALUES(?,?,?,?)`, strings.Repeat("a", 32), "synthetic", "now", "test")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot(t.Context(), f.cfg, f.run); !errors.Is(err, ErrDatabase) {
		t.Fatal("copied confined authority into another installation", err)
	}
	if _, err := os.Lstat(filepath.Join(f.cfg.OutputDir, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsupported authority received a publishable manifest")
	}
}

func TestStoppedSidecarPersistentMountsMustBeFullyAccountedFor(t *testing.T) {
	for _, kind := range []string{"unrecognized", "excluded", "covered", "disk-tmp"} {
		t.Run(kind, func(t *testing.T) {
			f := newSourceFixture(t)
			source := filepath.Join(f.root, "projects")
			if kind == "unrecognized" || kind == "disk-tmp" {
				source = filepath.Join(f.root, "sidecar-data")
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "excluded" {
				source = filepath.Join(f.root, "home")
			}
			f.peers = []sourceContainer{{ID: strings.Repeat("b", 64), Project: f.cfg.ComposeProject, Status: "exited", Restart: "no", Mounts: []sourceMount{{Type: "volume", Source: source, Destination: "/sidecar-data", RW: true}}}}
			if kind == "disk-tmp" {
				f.peers[0].Mounts[0].Destination = "/tmp"
			}
			_, _, err := observeSource(t.Context(), f.cfg, f.run)
			if kind == "covered" {
				if err != nil {
					t.Fatal("fully copied sidecar store refused", err)
				}
			} else if !errors.Is(err, ErrSource) {
				t.Fatal("stopped sidecar data silently omitted", err)
			}
		})
	}
}

func TestSnapshotShippedMountLayouts(t *testing.T) {
	for profile, files := range map[string][]string{
		"plain":      {"compose.workspace-plain.yaml"},
		"production": {"compose.yaml", "compose.mempalace.yaml", "compose.production.yaml"},
	} {
		t.Run(profile, func(t *testing.T) {
			f := newSourceFixture(t)
			paths := map[string]string{}
			for _, mount := range f.app.Mounts {
				paths[mount.Destination] = mount.Source
			}
			paths["/tmp"] = filepath.Join(f.root, "tmp")
			paths["/run/noisedeck-host-machine-id"] = filepath.Join(f.root, "machine-id")
			if err := os.Mkdir(paths["/tmp"], 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(paths["/tmp"], "discarded"), []byte("temporary scratch"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths["/run/noisedeck-host-machine-id"], []byte("synthetic machine identity"), 0600); err != nil {
				t.Fatal(err)
			}
			// Read the shipped app mount declarations, substituting only synthetic
			// local sources. Later overlays replace mounts at the same destination.
			mounts := map[string]sourceMount{}
			var tmpfs []string
			for _, file := range files {
				raw, err := os.ReadFile(filepath.Join("..", "..", file))
				if err != nil {
					t.Fatal(err)
				}
				var doc struct {
					Services map[string]struct {
						Volumes []yaml.Node `yaml:"volumes"`
						Tmpfs   []string    `yaml:"tmpfs"`
					} `yaml:"services"`
				}
				if err := yaml.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				app := doc.Services["app"]
				if app.Tmpfs != nil {
					tmpfs = app.Tmpfs
				}
				for _, node := range app.Volumes {
					var declaration struct {
						Type, Target string
						ReadOnly     bool `yaml:"read_only"`
					}
					if node.Kind == yaml.ScalarNode {
						parts := strings.Split(node.Value, ":")
						if len(parts) != 2 {
							t.Fatal("unsupported mount declaration", file)
						}
						declaration.Type, declaration.Target = "volume", parts[1]
					} else if err := node.Decode(&declaration); err != nil {
						t.Fatal(err)
					}
					path, ok := paths[declaration.Target]
					if !ok {
						t.Fatal("new shipped mount needs migration coverage", declaration.Target)
					}
					mounts[declaration.Target] = sourceMount{Type: declaration.Type, Source: path, Destination: declaration.Target, RW: !declaration.ReadOnly}
				}
			}
			for _, spec := range tmpfs {
				target := strings.SplitN(spec, ":", 2)[0]
				mounts[target] = sourceMount{Type: "tmpfs", Destination: target, RW: true}
			}
			if mounts["/tmp"].Type != "bind" {
				t.Fatal("test must exercise shipped disk-backed temporary storage")
			}
			f.app.Mounts = nil
			for _, mount := range mounts {
				f.app.Mounts = append(f.app.Mounts, mount)
			}
			if _, err := snapshot(t.Context(), f.cfg, f.run); err != nil {
				t.Fatal("shipped layout refused", err)
			}
			raw, err := os.ReadFile(filepath.Join(f.cfg.OutputDir, "source-report.json"))
			if err != nil {
				t.Fatal(err)
			}
			var report struct {
				OmittedMounts []string `json:"omitted_mounts"`
			}
			if err := json.Unmarshal(raw, &report); err != nil || !slices.Contains(report.OmittedMounts, "/tmp") {
				t.Fatal("temporary mount omission not recorded", err)
			}
			if _, err := os.Stat(filepath.Join(f.cfg.OutputDir, "tree", "tmp")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("temporary storage copied", err)
			}
			if _, err := os.Stat(filepath.Join(paths["/tmp"], "discarded")); err != nil {
				t.Fatal("source scratch removed", err)
			}
		})
	}
}
