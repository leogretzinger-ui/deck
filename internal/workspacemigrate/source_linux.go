//go:build linux

package workspacemigrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

var ErrSource = errors.New("workspace migration: source must be local, stopped and exclusively owned")

// SourceConfig is private admin inventory, never browser input. The engine
// supplies the persistent paths; the full container ID pins the old workload.
type SourceConfig struct {
	EnginePath     string `json:"engine_path"`
	EngineSocket   string `json:"engine_socket,omitempty"`
	HomeDir        string `json:"home_dir"`
	RuntimeDir     string `json:"runtime_dir"`
	ContainerID    string `json:"container_id"`
	ComposeProject string `json:"compose_project"`
	OutputDir      string `json:"output_dir"`
	OwnerUID       int    `json:"owner_uid"`
	OwnerGID       int    `json:"owner_gid"`
}

type sourceMount struct {
	Type, Source, Destination string
	RW                        bool
}
type sourceContainer struct {
	ID                                                         string
	Running, Paused, Restarting                                bool
	PID                                                        int
	Status, Started, Finished, Restart, Project, PodmanProject string
	Mounts                                                     []sourceMount
}

const sourceTemplate = `{"ID":{{json .Id}},"Running":{{json .State.Running}},"Paused":{{json .State.Paused}},"Restarting":{{json .State.Restarting}},"PID":{{json .State.Pid}},"Status":{{json .State.Status}},"Started":{{json .State.StartedAt}},"Finished":{{json .State.FinishedAt}},"Restart":{{json .HostConfig.RestartPolicy.Name}},"Project":{{json (index .Config.Labels "com.docker.compose.project")}},"PodmanProject":{{json (index .Config.Labels "io.podman.compose.project")}},"Mounts":{{json .Mounts}}}`

var sourceID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var sourceProject = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type engineRead func(context.Context, ...string) ([]byte, error)

func (c SourceConfig) validate() error {
	if !sourceID.MatchString(c.ContainerID) || !sourceProject.MatchString(c.ComposeProject) || c.OwnerUID <= 0 || c.OwnerGID < 0 || (os.Geteuid() != 0 && c.OwnerUID != os.Geteuid()) {
		return ErrSource
	}
	if !Canonical(c.EnginePath) || !Canonical(c.HomeDir) || !Canonical(c.RuntimeDir) {
		return ErrSource
	}
	base := filepath.Base(c.EnginePath)
	if base != "docker" && base != "podman" {
		return ErrSource
	}
	if base == "docker" {
		if !filepath.IsAbs(c.EngineSocket) || filepath.Clean(c.EngineSocket) != c.EngineSocket || strings.ContainsAny(c.EngineSocket, "\x00\r\n") {
			return ErrSource
		}
	} else if c.EngineSocket != "" {
		return ErrSource
	}
	if !filepath.IsAbs(c.OutputDir) || filepath.Clean(c.OutputDir) != c.OutputDir || strings.ContainsAny(c.OutputDir, "\x00\r\n") || !Canonical(filepath.Dir(c.OutputDir)) {
		return ErrSource
	}
	if _, err := os.Lstat(c.OutputDir); !errors.Is(err, os.ErrNotExist) {
		return ErrSource
	}
	st, err := os.Stat(filepath.Dir(c.OutputDir))
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return ErrSource
	}
	var actual unix.Stat_t
	if unix.Lstat(filepath.Dir(c.OutputDir), &actual) != nil || int(actual.Uid) != c.OwnerUID {
		return ErrSource
	}

	return nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, ErrSource
	}
	return b.Buffer.Write(p)
}

func (c SourceConfig) engine(ctx context.Context, args ...string) ([]byte, error) {
	prefix := []string{"--remote=false"}
	if filepath.Base(c.EnginePath) == "docker" {
		prefix = []string{"--host", "unix://" + c.EngineSocket}
	}
	// #nosec G204 -- Admin-pinned executable and fixed inspect/list arguments.
	cmd := exec.CommandContext(ctx, c.EnginePath, append(prefix, args...)...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + c.HomeDir, "XDG_RUNTIME_DIR=" + c.RuntimeDir, "LANG=C.UTF-8"}
	var output limitedBuffer
	output.limit = 4 * 1024 * 1024
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return nil, ErrSource
	}
	return output.Bytes(), nil
}

func inspectSource(ctx context.Context, run engineRead, id string) (sourceContainer, error) {
	var v sourceContainer
	if !sourceID.MatchString(id) {
		return v, ErrSource
	}
	raw, err := run(ctx, "inspect", "--format", sourceTemplate, id)
	if err != nil || json.Unmarshal(raw, &v) != nil || v.ID != id {
		return v, ErrSource
	}
	return v, nil
}

func stopped(v sourceContainer) bool {
	return !v.Running && !v.Paused && !v.Restarting && v.PID == 0 && (v.Status == "exited" || v.Status == "created") && (v.Restart == "" || v.Restart == "no")
}
func project(v sourceContainer, want string) bool {
	return (v.Project == want || v.PodmanProject == want) && (v.Project == "" || v.Project == want) && (v.PodmanProject == "" || v.PodmanProject == want)
}
func inside(name, root string) bool { return name == root || strings.HasPrefix(name, root+"/") }

type sourceStore struct {
	destination, source string
	exclude             []string
}

func sourceStores(v sourceContainer) ([]sourceStore, error) {
	byDestination := map[string]string{}
	for _, m := range v.Mounts {
		if omittedAppMount(m) {
			continue
		}
		if !slices.Contains([]string{"/data", "/home/agent", "/workspace", "/home/agent/.config/gh", "/home/agent/.mempalace"}, m.Destination) || (m.Type != "bind" && m.Type != "volume") || byDestination[m.Destination] != "" || !Canonical(m.Source) {
			return nil, ErrSource
		}
		st, err := os.Stat(m.Source)
		if err != nil || !st.IsDir() {
			return nil, ErrSource
		}
		byDestination[m.Destination] = m.Source
	}
	for _, name := range []string{"/data", "/home/agent", "/workspace"} {
		if byDestination[name] == "" {
			return nil, ErrSource
		}
	}
	gh := byDestination["/home/agent/.config/gh"]
	if gh == "" {
		gh = filepath.Join(byDestination["/home/agent"], ".config", "gh")
		if !Canonical(gh) {
			return nil, ErrSource
		}
	}
	stores := []sourceStore{
		{"data", byDestination["/data"], []string{"gateway"}},
		{"home", byDestination["/home/agent"], []string{".config/gh"}},
		{"projects", byDestination["/workspace"], nil},
		{"gh", gh, nil},
	}
	if palace := byDestination["/home/agent/.mempalace"]; palace != "" {
		stores[1].exclude = append(stores[1].exclude, ".mempalace")
		stores = append(stores, sourceStore{"home/.mempalace", palace, nil})
	}
	return stores, nil
}

// The shipped app profiles use disk-backed /tmp as disposable scratch. This
// exception applies only to the app: a sidecar's persistent mount still needs
// copied-store coverage, regardless of its container path.
func omittedAppMount(m sourceMount) bool {
	if m.Type == "tmpfs" && (m.Destination == "/tmp" || m.Destination == "/run/noisedeck") {
		return true
	}
	if m.Destination == "/tmp" && (m.Type == "bind" || m.Type == "volume") && Canonical(m.Source) {
		info, err := os.Stat(m.Source)
		return err == nil && info.IsDir()
	}
	return !m.RW && (m.Destination == "/run/noisedeck-host-machine-id" || m.Destination == "/run/noisedeck/sealed-state")
}

// observeSource checks every container, including sidecars and other projects
// sharing a source mount. Host services and supervisors remain SRE's stop duty.
func observeSource(ctx context.Context, c SourceConfig, run engineRead) (sourceContainer, []sourceStore, error) {
	v, err := inspectSource(ctx, run, c.ContainerID)
	if err != nil || !stopped(v) || !project(v, c.ComposeProject) {
		return v, nil, ErrSource
	}
	stores, err := sourceStores(v)
	if err != nil {
		return v, nil, err
	}
	for _, store := range stores {
		if inside(c.OutputDir, store.source) || inside(store.source, c.OutputDir) {
			return v, nil, ErrSource
		}
	}
	raw, err := run(ctx, "ps", "--all", "--no-trunc", "--format", "{{.ID}}")
	if err != nil {
		return v, nil, ErrSource
	}
	found := false
	for _, id := range strings.Fields(string(raw)) {
		peer, err := inspectSource(ctx, run, id)
		if err != nil {
			return v, nil, ErrSource
		}
		if id == c.ContainerID {
			if !reflect.DeepEqual(peer, v) {
				return v, nil, ErrSource
			}
			found = true
		}
		if project(peer, c.ComposeProject) {
			if !stopped(peer) {
				return v, nil, ErrSource
			}
			if id != c.ContainerID {
				for _, mount := range peer.Mounts {
					if !accountedMount(mount, stores) {
						return v, nil, ErrSource
					}
				}
			}
		}
		if peer.Running || peer.Paused || peer.Restarting || peer.PID != 0 {
			for _, mount := range peer.Mounts {
				for _, store := range stores {
					if mount.Source != "" && (inside(mount.Source, store.source) || inside(store.source, mount.Source)) {
						return v, nil, ErrSource
					}
				}
			}
		}
	}
	if !found {
		return v, nil, ErrSource
	}
	return v, stores, nil
}

// A stopped sidecar can own data absent from the app's mount list. Only mounts
// whose bytes are fully included in the selected stores can rely on that
// copy; known ephemeral/system mounts carry no additional persistent data.
func accountedMount(mount sourceMount, stores []sourceStore) bool {
	if mount.Type == "tmpfs" && (mount.Destination == "/tmp" || mount.Destination == "/run/noisedeck") {
		return true
	}
	if !mount.RW && (mount.Destination == "/run/noisedeck-host-machine-id" || mount.Destination == "/run/noisedeck/sealed-state") {
		return true
	}
	if (mount.Type != "bind" && mount.Type != "volume") || !Canonical(mount.Source) {
		return false
	}
	for _, store := range stores {
		if !inside(mount.Source, store.source) {
			continue
		}
		covered := true
		for _, skip := range store.exclude {
			excluded := filepath.Join(store.source, skip)
			if inside(mount.Source, excluded) || inside(excluded, mount.Source) {
				covered = false
				break
			}
		}
		if covered {
			return true
		}
	}
	return false
}
