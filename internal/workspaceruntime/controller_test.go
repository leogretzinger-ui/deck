//go:build linux

package workspaceruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/template"

	"github.com/noisefloor-oss/deck/internal/sealedstate"
	"golang.org/x/sys/unix"
)

type invocation struct {
	path      string
	args, env []string
	input     []byte
	capture   bool
}
type fakeExit int

func (e fakeExit) Error() string { return "synthetic native failure" }
func (e fakeExit) ExitCode() int { return int(e) }

type fixture struct {
	t         *testing.T
	cfg       Config
	c         *Controller
	facts     kernelFacts
	calls     []invocation
	health    string
	started   string
	app       bool
	failMount error
	failStart error
	failGate  error
	initHook  func()
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("controller intentionally refuses a root owner")
	}
	root := t.TempDir()
	for _, name := range []string{"cipher", "plain", "control", "home", "run", "bin"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{t: t, health: "healthy", started: "2026-09-21T00:00:00Z"}
	f.cfg = Config{InstanceID: "test_instance", OwnerUID: os.Geteuid(), CipherDir: filepath.Join(root, "cipher"), MountPoint: filepath.Join(root, "plain"), ControlDir: filepath.Join(root, "control"), HomeDir: filepath.Join(root, "home"), RuntimeDir: filepath.Join(root, "run"), GocryptfsPath: filepath.Join(root, "bin", "gocryptfs"), FusermountPath: filepath.Join(root, "bin", "fusermount3"), PodmanPath: filepath.Join(root, "bin", "podman"), SealedComposePath: filepath.Join(root, "sealed-compose.sh"), ComposeEnvFile: filepath.Join(root, "compose.env"), ComposeProject: "test_instance", AppContainer: "test_instance-app-1"}
	for _, path := range []string{f.cfg.GocryptfsPath, f.cfg.FusermountPath, f.cfg.PodmanPath, f.cfg.SealedComposePath, f.cfg.ComposeEnvFile} {
		if err := os.WriteFile(path, []byte("synthetic installation file"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.facts = kernelFacts{BootID: "test-boot", MountInstance: 1, FuseDevice: true, FuseAllowSysAdminAccess: "N", ResumeDevice: "0:0", Mounts: []sealedstate.Mount{{ID: 1, Device: "1:1", MountPoint: "/", Root: "/", FSType: "ext4"}}}
	f.open()
	t.Cleanup(func() {
		if f.c != nil {
			f.c.Close()
		}
	})
	return f
}

func TestInstallationPathsRejectEncryptedAliases(t *testing.T) {
	fields := map[string]func(*Config) *string{
		"home":        func(c *Config) *string { return &c.HomeDir },
		"runtime":     func(c *Config) *string { return &c.RuntimeDir },
		"gocryptfs":   func(c *Config) *string { return &c.GocryptfsPath },
		"fusermount":  func(c *Config) *string { return &c.FusermountPath },
		"podman":      func(c *Config) *string { return &c.PodmanPath },
		"wrapper":     func(c *Config) *string { return &c.SealedComposePath },
		"environment": func(c *Config) *string { return &c.ComposeEnvFile },
	}
	for name, field := range fields {
		for _, rootName := range []string{"cipher", "mount", "external", "missing"} {
			for _, parentAlias := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/parent=%t", name, rootName, parentAlias), func(t *testing.T) {
					f := newFixture(t)
					if err := f.c.Close(); err != nil {
						t.Fatal(err)
					}
					f.c = nil
					// A new journal makes path validation, rather than an earlier
					// configuration fingerprint, decide whether Open may proceed.
					f.cfg.ControlDir = t.TempDir()
					if err := os.Chmod(f.cfg.ControlDir, 0700); err != nil {
						t.Fatal(err)
					}
					root := t.TempDir()
					if rootName == "cipher" {
						root = f.cfg.CipherDir
					}
					if rootName == "mount" {
						root = f.cfg.MountPoint
					}
					base := filepath.Base(*field(&f.cfg))
					target := filepath.Join(root, base)
					if rootName != "missing" {
						if name == "home" || name == "runtime" {
							if err := os.Mkdir(target, 0700); err != nil {
								t.Fatal(err)
							}
						} else if err := os.WriteFile(target, []byte("synthetic installation file"), 0700); err != nil {
							t.Fatal(err)
						}
					}
					alias := filepath.Join(t.TempDir(), base)
					path := alias
					if parentAlias {
						target = root
						path = filepath.Join(alias, base)
					}
					if err := os.Symlink(target, alias); err != nil {
						t.Fatal(err)
					}
					*field(&f.cfg) = path
					c, err := open(f.cfg, f, func() (kernelFacts, error) { return f.facts, nil })
					if c != nil {
						_ = c.Close()
					}
					if rootName == "external" {
						if err != nil {
							t.Fatalf("external installed alias refused: %v", err)
						}
					} else if !errors.Is(err, ErrConfiguration) {
						t.Fatalf("unsafe installation alias accepted: %v", err)
					}
					if len(f.calls) != 0 {
						t.Fatal("configuration validation dispatched a native command")
					}
				})
			}
		}
	}
}

func (f *fixture) open() {
	f.t.Helper()
	c, err := open(f.cfg, f, func() (kernelFacts, error) { return f.facts, nil })
	if err != nil {
		f.t.Fatal(err)
	}
	c.closedProcesses = func(context.Context) error { return nil }
	f.c = c
}

func (f *fixture) configured() {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.cfg.CipherDir, "gocryptfs.conf"), []byte("synthetic encrypted metadata"), 0600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) mounted() {
	f.t.Helper()
	var st unix.Stat_t
	if err := unix.Stat(f.cfg.MountPoint, &st); err != nil {
		f.t.Fatal(err)
	}
	f.facts.Mounts = append(f.facts.Mounts[:1], sealedstate.Mount{ID: 40, Device: fmt.Sprintf("%d:%d", unix.Major(uint64(st.Dev)), unix.Minor(uint64(st.Dev))), MountPoint: f.cfg.MountPoint, Root: "/", FSType: "fuse.gocryptfs", Source: f.cfg.CipherDir, SuperOpts: fmt.Sprintf("rw,user_id=%d,group_id=100", f.cfg.OwnerUID)})
}

func (f *fixture) run(_ context.Context, path string, args, env []string, input []byte, capture bool) ([]byte, error) {
	f.calls = append(f.calls, invocation{path: path, args: slices.Clone(args), env: slices.Clone(env), input: slices.Clone(input), capture: capture})
	if path == f.cfg.GocryptfsPath {
		if slices.Contains(args, "-init") {
			f.configured()
			if f.initHook != nil {
				f.initHook()
			}
			return nil, nil
		}
		if f.failMount != nil {
			return []byte("must not escape"), f.failMount
		}
		f.mounted()
		return nil, nil
	}
	if path == f.cfg.FusermountPath {
		f.facts.Mounts = f.facts.Mounts[:1]
		return nil, nil
	}
	if len(args) > 0 && args[0] == "ps" {
		if f.app {
			return []byte(strings.Repeat("a", 64)), nil
		}
		return nil, nil
	}
	if path == f.cfg.SealedComposePath {
		f.app = !slices.Contains(args, "down")
		return nil, f.failStart
	}
	if len(args) > 0 && args[0] == "info" {
		return []byte("true\n"), nil
	}
	if len(args) > 0 && args[0] == "container" {
		if f.app {
			return nil, nil
		}
		return nil, fakeExit(1)
	}
	if len(args) > 0 && args[0] == "inspect" {
		out, _ := json.Marshal(map[string]any{"id": strings.Repeat("a", 64), "running": true, "pid": 1234, "status": "running", "startedAt": f.started, "health": f.health, "project": f.cfg.ComposeProject, "podmanProject": ""})
		return out, nil
	}
	if len(args) > 0 && args[0] == "exec" && slices.Equal(args, []string{"exec", f.cfg.AppContainer, "/usr/local/bin/noise", "deck", "sealed-check"}) {
		return nil, f.failGate
	}
	f.t.Fatalf("unexpected native action %s %v", path, args)
	return nil, ErrCommand
}

func (f *fixture) mutations() []invocation {
	var result []invocation
	for _, call := range f.calls {
		if call.path == f.cfg.GocryptfsPath || call.path == f.cfg.SealedComposePath {
			result = append(result, call)
		}
	}
	return result
}

func TestInitializePrivatePipeAndStableObservation(t *testing.T) {
	f := newFixture(t)
	t.Setenv("NOISEDECK_SEALED_STATE_ROOT", "/an/ambient/replacement")
	t.Setenv("UNRELATED_PROVIDER_SECRET", "must-not-inherit")
	secret := bytes.Repeat([]byte{0x81}, 32)
	op, err := f.c.Initialize(context.Background(), "initialize_1", secret)
	if err != nil || op.Status != "succeeded" || op.State.Storage != "open" || op.State.Runtime != "stopped" {
		t.Fatalf("initialize: %+v %v", op, err)
	}
	want := base64.RawURLEncoding.EncodeToString(secret) + "\n"
	mutations := f.mutations()
	if len(mutations) != 2 || !slices.Contains(mutations[0].args, "-init") {
		t.Fatalf("native actions: %+v", mutations)
	}
	for _, call := range mutations {
		if string(call.input) != want || call.capture {
			t.Fatal("secret must use discarded-output command's private stdin")
		}
		if strings.Contains(strings.Join(append(slices.Clone(call.args), call.env...), " "), strings.TrimSpace(want)) {
			t.Fatal("secret escaped private pipe")
		}
		if strings.Contains(strings.Join(call.env, " "), "must-not-inherit") || !slices.Contains(call.env, "NOISEDECK_SEALED_STATE_ROOT="+f.cfg.MountPoint) {
			t.Fatal("ambient environment replaced fixed installation")
		}
	}
	if err := f.c.Close(); err != nil {
		t.Fatal(err)
	}
	f.open()
	again, err := f.c.Initialize(context.Background(), "initialize_1", secret)
	if err != nil || again.State.Epoch != op.State.Epoch || len(f.mutations()) != 2 {
		t.Fatalf("replayed native initialization: %+v %v", again, err)
	}
	if _, err := f.c.Start(context.Background(), "initialize_1"); !errors.Is(err, ErrConflict) {
		t.Fatal("operation ID changed its action", err)
	}
	for _, call := range f.calls {
		if slices.Contains(call.args, "down") || slices.Contains(call.args, "-u") {
			t.Fatal("observation stopped a native workload")
		}
	}
}

func TestInitializationRefusesExistingData(t *testing.T) {
	for _, where := range []string{"cipher", "plain", "native-config"} {
		t.Run(where, func(t *testing.T) {
			f := newFixture(t)
			if where == "native-config" {
				f.configured()
			} else {
				path := f.cfg.CipherDir
				if where == "plain" {
					path = f.cfg.MountPoint
				}
				if err := os.WriteFile(filepath.Join(path, "existing"), []byte("do not replace"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.c.Initialize(context.Background(), "init", make([]byte, 32))
			if !errors.Is(err, ErrPrecondition) || len(f.mutations()) != 0 {
				t.Fatalf("existing data was not refused: %v", err)
			}
		})
	}
}

func TestSharedHostGateRefusesUnsafeFacts(t *testing.T) {
	cases := map[string]func(*fixture){
		"swap": func(f *fixture) {
			f.c.cfg.SwapPolicy = sealedstate.SwapParanoid
			f.facts.SwapFacts.Swaps = []string{"/dev/zram0"}
		},
		"root-access":  func(f *fixture) { f.facts.FuseAllowSysAdminAccess = "Y" },
		"resume":       func(f *fixture) { f.facts.ResumeDevice = "8:1" },
		"missing-fuse": func(f *fixture) { f.facts.FuseDevice = false },
		"allow-other":  func(f *fixture) { f.configured(); f.mounted(); f.facts.Mounts[1].SuperOpts += ",allow_other" },
		"wrong-owner":  func(f *fixture) { f.configured(); f.mounted(); f.facts.Mounts[1].SuperOpts = "user_id=0" },
		"wrong-source": func(f *fixture) { f.configured(); f.mounted(); f.facts.Mounts[1].Source = "/different/cipher" },
	}
	for name, alter := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			alter(f)
			state, err := f.c.Observe(context.Background())
			if !errors.Is(err, ErrUnsafeStorage) || state.Epoch != "" {
				t.Fatalf("unsafe host: %+v %v", state, err)
			}
			if _, err = f.c.Initialize(context.Background(), "init", make([]byte, 32)); err == nil || len(f.mutations()) != 0 {
				t.Fatal("unsafe host dispatched native initialization")
			}
		})
	}
}

func TestUncertainMountIsNeverReplayedAndErrorsAreRedacted(t *testing.T) {
	f := newFixture(t)
	secret := bytes.Repeat([]byte{0x96}, 32)
	encoded := base64.RawURLEncoding.EncodeToString(secret)
	f.failMount = errors.New("native stderr printed " + encoded)
	op, err := f.c.Initialize(context.Background(), "init", secret)
	if !errors.Is(err, ErrCommand) || op.Status != "uncertain" || strings.Contains(err.Error(), encoded) {
		t.Fatalf("uncertain result: %+v %v", op, err)
	}
	f.c.Close()
	f.open()
	again, err := f.c.Initialize(context.Background(), "init", secret)
	if err != nil || again.Status != "uncertain" || len(f.mutations()) != 2 {
		t.Fatalf("uncertain effect replayed: %+v %v", again, err)
	}
	if _, err := f.c.Unlock(context.Background(), "another", secret); !errors.Is(err, ErrUncertain) {
		t.Fatalf("new ID bypassed unresolved effect: %v", err)
	}
	entries, _ := os.ReadDir(f.cfg.ControlDir)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(f.cfg.ControlDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, secret) || bytes.Contains(data, []byte(encoded)) {
			t.Fatal("native journal persisted secret")
		}
	}
	f.mounted()
	again, err = f.c.Operation(context.Background(), "init")
	if err != nil || again.Status != "succeeded" || len(f.mutations()) != 2 {
		t.Fatalf("observed effect not reconciled: %+v %v", again, err)
	}
}

func TestResumeOnlyProvenStages(t *testing.T) {
	for _, stage := range []string{"prepared", "initializing", "initialized", "mounting"} {
		t.Run(stage, func(t *testing.T) {
			f := newFixture(t)
			digest := ""
			if stage == "initialized" || stage == "mounting" {
				f.configured()
				digest = f.c.configDigest()
			}
			if _, err := f.c.db.Exec(`INSERT INTO operations(id,kind,status,stage,config_digest) VALUES('init','initialize','running',?,?)`, stage, digest); err != nil {
				t.Fatal(err)
			}
			f.c.Close()
			f.open()
			op, err := f.c.Initialize(context.Background(), "init", make([]byte, 32))
			if err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "prepared":
				if op.Status != "succeeded" || len(f.mutations()) != 2 {
					t.Fatalf("prepared stage did not proceed once: %+v", op)
				}
			case "initialized":
				if op.Status != "succeeded" || len(f.mutations()) != 1 || slices.Contains(f.mutations()[0].args, "-init") {
					t.Fatalf("initialized stage reinitialized backing: %+v", op)
				}
			default:
				if op.Status != "uncertain" || len(f.mutations()) != 0 {
					t.Fatalf("ambiguous stage repeated effect: %+v", op)
				}
			}
		})
	}
}

func TestChangedNativeMetadataCannotResume(t *testing.T) {
	f := newFixture(t)
	f.configured()
	digest := f.c.configDigest()
	f.c.db.Exec(`INSERT INTO operations(id,kind,status,stage,config_digest) VALUES('init','initialize','uncertain','initialized',?)`, digest)
	os.WriteFile(filepath.Join(f.cfg.CipherDir, "gocryptfs.conf"), []byte("another volume"), 0600)
	if _, err := f.c.Initialize(context.Background(), "init", make([]byte, 32)); !errors.Is(err, ErrUnsafeStorage) || len(f.mutations()) != 0 {
		t.Fatalf("replaced native metadata: %v", err)
	}
}

func TestStartUsesStorageGateAndObservedHealth(t *testing.T) {
	f := newFixture(t)
	f.configured()
	f.mounted()
	f.health = "starting"
	op, err := f.c.Start(context.Background(), "start")
	if err != nil || op.Status != "running" || op.State.Runtime != "starting" {
		t.Fatalf("startup was presented ready: %+v %v", op, err)
	}
	if got := f.mutations(); len(got) != 1 || got[0].path != f.cfg.SealedComposePath || !slices.Equal(got[0].args, []string{f.cfg.ComposeEnvFile, "up"}) || len(got[0].input) != 0 {
		t.Fatalf("runtime bypassed wrapper: %+v", got)
	}
	epoch := op.State.Epoch
	f.health = "healthy"
	op, err = f.c.Operation(context.Background(), "start")
	if err != nil || op.Status != "succeeded" || op.State.Epoch != epoch {
		t.Fatalf("ready observation: %+v %v", op, err)
	}
	f.started = "2026-09-21T00:02:00Z"
	state, err := f.c.Observe(context.Background())
	if err != nil || state.Epoch == epoch {
		t.Fatal("restarted native runtime retained epoch", err)
	}
}

func TestMountLostBeforeProvisioningDoesNotWritePlaintext(t *testing.T) {
	f := newFixture(t)
	f.configured()
	f.mounted()
	reads := 0
	f.c.kernel = func() (kernelFacts, error) {
		reads++
		if reads == 2 {
			f.facts.Mounts = f.facts.Mounts[:1]
		}
		return f.facts, nil
	}
	op, err := f.c.Start(context.Background(), "start")
	if !errors.Is(err, ErrCommand) || op.Status != "uncertain" || len(f.mutations()) != 0 {
		t.Fatalf("lost mount: %+v %v", op, err)
	}
	if !emptyDirectory(f.cfg.MountPoint) {
		t.Fatal("provisioning created plaintext stores below closed mount")
	}
}

func TestConfigurationAndOperationBounds(t *testing.T) {
	f := newFixture(t)
	if _, err := f.c.Initialize(context.Background(), strings.Repeat("a", 129), make([]byte, 32)); !errors.Is(err, ErrPrecondition) {
		t.Fatal(err)
	}
	if _, err := f.c.Initialize(context.Background(), "init", make([]byte, 31)); !errors.Is(err, ErrPrecondition) {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){func(c *Config) { c.OwnerUID = 0 }, func(c *Config) { c.ControlDir = c.MountPoint }, func(c *Config) { c.HomeDir = c.MountPoint }, func(c *Config) { c.RuntimeDir = c.CipherDir }, func(c *Config) { c.ComposeProject = "deck" }, func(c *Config) { c.AppContainer = "somebody; else's" }, func(c *Config) { c.MountPoint = "relative" }} {
		cfg := f.cfg
		change(&cfg)
		if c, err := open(cfg, f, func() (kernelFacts, error) { return f.facts, nil }); err == nil {
			c.Close()
			t.Fatal("invalid installation configuration accepted")
		}
	}
}

func TestReadinessRequiresContainerStorageGate(t *testing.T) {
	f := newFixture(t)
	f.configured()
	f.mounted()
	f.app = true
	f.failGate = errors.New("stale report")
	s, err := f.c.Observe(context.Background())
	if !errors.Is(err, ErrUnsafeStorage) || s.Runtime != "unknown" || s.Epoch != "" {
		t.Fatalf("healthy app bypassed storage gate: %+v %v", s, err)
	}
}

func TestWaitingObservationHonorsCancellation(t *testing.T) {
	f := newFixture(t)
	if err := f.c.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	state, err := f.c.Observe(ctx)
	f.c.release()
	if !errors.Is(err, context.Canceled) || state.Storage != "unknown" || len(f.calls) != 0 {
		t.Fatal("canceled observer waited for an in-flight operation")
	}
}

func TestInspectTemplateSelectsOnlyNativeFacts(t *testing.T) {
	marshal := func(v any) (string, error) { raw, err := json.Marshal(v); return string(raw), err }
	tmpl, err := template.New("inspect").Funcs(template.FuncMap{"json": marshal}).Parse(inspectFormat)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = tmpl.Execute(&out, map[string]any{"Id": strings.Repeat("b", 64), "State": map[string]any{"Running": true, "Pid": 12, "Status": "running", "StartedAt": "2026-09-21T00:00:00Z", "Healthcheck": map[string]any{"Status": "healthy", "Log": "private health output"}}, "Config": map[string]any{"Labels": map[string]string{"com.docker.compose.project": "test"}, "Env": []string{"PRIVATE=never copy"}}})
	var parsed map[string]any
	if err != nil || json.Unmarshal(out.Bytes(), &parsed) != nil || parsed["health"] != "healthy" || strings.Contains(out.String(), "private") || strings.Contains(out.String(), "PRIVATE") {
		t.Fatalf("inspection template did not select bounded native facts: %v", err)
	}
}
