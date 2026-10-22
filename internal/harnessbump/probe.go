package harnessbump

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ProbeCommand is one command-line syntax check from a contract's
// runtime_compatibility_probe.
type ProbeCommand struct {
	Args                []string `yaml:"args"`
	RequiredFlags       []string `yaml:"required_flags"`
	RequiredUsageTokens []string `yaml:"required_usage_tokens"`
	AcceptedStdin       string   `yaml:"accepted_stdin"`
}

// ProbeSpec is a contract's runtime_compatibility_probe block. A contract
// writes either one `command` or a list of `commands`.
type ProbeSpec struct {
	Command              []string       `yaml:"command"`
	RequiredFlags        []string       `yaml:"required_flags"`
	Commands             []ProbeCommand `yaml:"commands"`
	ExactVersionRequired string         `yaml:"exact_version_required"`
	Reason               string         `yaml:"reason"`
}

// runtimeCompatibility is the older `runtime_compatibility` block some
// contracts carry instead of, or beside, `runtime_compatibility_probe`. It
// comes in three shapes: one `command` with `required_flags`; `method:
// capability_probes` with a list of named probes; and `method:
// exact_tested_version`, which declares that no syntax check can stand in
// for the measurement, so the shipped version must be `version_tested`.
type runtimeCompatibility struct {
	Method           string    `yaml:"method"`
	Command          []string  `yaml:"command"`
	RequiredFlags    []string  `yaml:"required_flags"`
	CapabilityProbes yaml.Node `yaml:"capability_probes"`
	Reason           string    `yaml:"reason"`
}

type capabilityProbe struct {
	Name          string   `yaml:"name"`
	Args          []string `yaml:"args"`
	RequiredFlags []string `yaml:"required_flags"`
}

// ReceiveProbeSpec is a contract's native_receive_probe block: the
// isolated probe that exercises the harness's real a2a receive path
// inside the built image, against a synthetic mailbox and a loopback
// model. It is the gate that notices a release whose hooks, plugin or
// queue API stopped delivering chat, which no command-line syntax check
// can see.
type ReceiveProbeSpec struct {
	Script   string `yaml:"script"`
	Network  string `yaml:"network"`
	TimeoutS int    `yaml:"timeout_s"`
}

// LoadReceiveProbe reads the harness's contract and returns its
// native_receive_probe, or nil when the contract declares none. The shape
// is closed like every other probe declaration: a misspelled or empty
// block is an error, never a pass.
func LoadReceiveProbe(root, name string) (*ReceiveProbeSpec, error) {
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(root, "docs", "provider-contracts", name+".yaml")))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var shape struct {
		Receive map[string]yaml.Node `yaml:"native_receive_probe"`
	}
	if err := yaml.Unmarshal(raw, &shape); err != nil {
		return nil, fmt.Errorf("harnessbump: contract %s: %w", name, err)
	}
	// Absent means no receive gate; present-but-null is a contract that
	// tried to declare one and must never pass silently.
	if !bytes.Contains(raw, []byte("native_receive_probe")) {
		return nil, nil
	}
	if shape.Receive == nil {
		return nil, fmt.Errorf("harnessbump: contract %s: native_receive_probe is present but not a mapping", name)
	}
	for key := range shape.Receive {
		switch key {
		case "script", "network", "timeout_s":
		default:
			return nil, fmt.Errorf("harnessbump: contract %s: native_receive_probe key %q is not one the probe reads", name, key)
		}
	}
	var c struct {
		Receive *ReceiveProbeSpec `yaml:"native_receive_probe"`
	}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("harnessbump: contract %s: %w", name, err)
	}
	spec := c.Receive
	if spec == nil || strings.TrimSpace(spec.Script) == "" {
		return nil, fmt.Errorf("harnessbump: contract %s: native_receive_probe declares no script", name)
	}
	if strings.Contains(spec.Script, "/") || strings.Contains(spec.Script, "..") {
		return nil, fmt.Errorf("harnessbump: contract %s: native_receive_probe script %q must be a name in the probe directory, not a path", name, spec.Script)
	}
	switch spec.Network {
	case "", "none", "outbound":
	default:
		return nil, fmt.Errorf("harnessbump: contract %s: native_receive_probe network %q is not none or outbound", name, spec.Network)
	}
	if spec.TimeoutS == 0 {
		spec.TimeoutS = 420
	}
	if spec.TimeoutS < 60 || spec.TimeoutS > 1800 {
		return nil, fmt.Errorf("harnessbump: contract %s: native_receive_probe timeout_s %d is outside 60..1800", name, spec.TimeoutS)
	}
	return spec, nil
}

type contractFile struct {
	VersionTested string                `yaml:"version_tested"`
	Probe         *ProbeSpec            `yaml:"runtime_compatibility_probe"`
	Compatibility *runtimeCompatibility `yaml:"runtime_compatibility"`
}

// LoadProbe reads the harness's contract and returns everything it
// declares about runtime compatibility as one probe, or nil when the
// contract declares nothing. A contract that declares a method the probe
// cannot enforce is an error, never a pass.
func LoadProbe(root, name string) (*ProbeSpec, error) {
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(root, "docs", "provider-contracts", name+".yaml")))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c contractFile
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("harnessbump: contract %s: %w", name, err)
	}
	if c.Probe != nil {
		// The newer block is held to the same rule as the older: it must
		// spell something the probe enforces, with the keys the probe
		// reads, so a misspelled or empty block is an error, never a pass.
		var shape struct {
			Probe map[string]yaml.Node `yaml:"runtime_compatibility_probe"`
		}
		_ = yaml.Unmarshal(raw, &shape)
		for key := range shape.Probe {
			switch key {
			case "command", "required_flags", "commands", "exact_version_required", "reason", "scope", "does_not_remeasure", "side_effects":
			default:
				return nil, fmt.Errorf("harnessbump: contract %s: runtime_compatibility_probe key %q is not one the probe reads", name, key)
			}
		}
		for i, cmd := range c.Probe.Commands {
			if len(cmd.Args) == 0 {
				return nil, fmt.Errorf("harnessbump: contract %s: runtime_compatibility_probe command %d has no args", name, i)
			}
			if len(cmd.RequiredFlags) == 0 && len(cmd.RequiredUsageTokens) == 0 && cmd.AcceptedStdin == "" {
				return nil, fmt.Errorf("harnessbump: contract %s: runtime_compatibility_probe command %d checks nothing", name, i)
			}
		}
		if len(c.Probe.Command) > 0 && len(c.Probe.RequiredFlags) == 0 {
			return nil, fmt.Errorf("harnessbump: contract %s: runtime_compatibility_probe command requires no flag", name)
		}
		if len(c.Probe.commands()) == 0 && c.Probe.ExactVersionRequired == "" {
			return nil, fmt.Errorf("harnessbump: contract %s: runtime_compatibility_probe enforces nothing", name)
		}
	}
	if c.Compatibility == nil {
		return c.Probe, nil
	}
	spec := &ProbeSpec{}
	if c.Probe != nil {
		spec = c.Probe
	}
	rc := c.Compatibility
	// The older block is a closed shape too: only the keys the probe reads,
	// and every runnable check names a command and a flag it must find.
	var legacy struct {
		Compatibility map[string]yaml.Node `yaml:"runtime_compatibility"`
	}
	_ = yaml.Unmarshal(raw, &legacy)
	for key := range legacy.Compatibility {
		switch key {
		case "method", "command", "required_flags", "capability_probes", "probe_state_isolation", "reason":
		default:
			return nil, fmt.Errorf("harnessbump: contract %s: runtime_compatibility key %q is not one the probe reads", name, key)
		}
	}
	switch rc.Method {
	case "", "command":
		if len(rc.Command) == 0 {
			return nil, fmt.Errorf("harnessbump: contract %s: runtime_compatibility declares no command", name)
		}
		if len(rc.RequiredFlags) == 0 {
			return nil, fmt.Errorf("harnessbump: contract %s: runtime_compatibility command requires no flag", name)
		}
		spec.Commands = append(spec.Commands, ProbeCommand{Args: rc.Command, RequiredFlags: rc.RequiredFlags})
	case "capability_probes":
		var shapes []map[string]yaml.Node
		if err := rc.CapabilityProbes.Decode(&shapes); err != nil || len(shapes) == 0 {
			return nil, fmt.Errorf("harnessbump: contract %s: runtime_compatibility names no capability probes", name)
		}
		for i, shape := range shapes {
			for key := range shape {
				switch key {
				case "name", "args", "required_flags":
				default:
					return nil, fmt.Errorf("harnessbump: contract %s: capability probe %d key %q is not one the probe reads", name, i, key)
				}
			}
		}
		var probes []capabilityProbe
		if err := rc.CapabilityProbes.Decode(&probes); err != nil {
			return nil, fmt.Errorf("harnessbump: contract %s: capability probes are malformed", name)
		}
		for i, p := range probes {
			if len(p.Args) == 0 || len(p.RequiredFlags) == 0 {
				return nil, fmt.Errorf("harnessbump: contract %s: capability probe %d checks nothing", name, i)
			}
			spec.Commands = append(spec.Commands, ProbeCommand{Args: p.Args, RequiredFlags: p.RequiredFlags})
		}
	case "exact_tested_version":
		if c.VersionTested == "" {
			return nil, fmt.Errorf("harnessbump: contract %s: exact_tested_version without version_tested", name)
		}
		spec.ExactVersionRequired = c.VersionTested
		spec.Reason = strings.TrimSpace(rc.Reason)
	default:
		return nil, fmt.Errorf("harnessbump: contract %s: runtime_compatibility method %q is not one the probe enforces", name, rc.Method)
	}
	if len(spec.commands()) == 0 && spec.ExactVersionRequired == "" {
		return nil, fmt.Errorf("harnessbump: contract %s declares runtime compatibility the probe cannot enforce", name)
	}
	return spec, nil
}

func (p *ProbeSpec) commands() []ProbeCommand {
	if len(p.Command) > 0 {
		return append([]ProbeCommand{{Args: p.Command, RequiredFlags: p.RequiredFlags}}, p.Commands...)
	}
	return p.Commands
}

// Runner executes the harness executable with arguments and stdin and
// returns everything it wrote and its exit error.
type Runner func(ctx context.Context, args []string, stdin string) (output string, err error)

// tmpfsHome is the probe container's /tmp. Moby prepends noexec to every
// --tmpfs option list and only a later explicit exec cancels it, so
// "rw,mode=1777" alone mounts a /tmp that refuses to run anything: the
// claude, kimi and codex receive probes exec their a2a hook wrapper from
// the probe root there and fail with "Permission denied", which reads as a
// broken CLI release rather than a mount flag.
const tmpfsHome = "/tmp:rw,exec,mode=1777"

// DockerRunner runs the harness inside a built image: the image read-only,
// no network, and a tmpfs home because several CLIs write state from
// --help.
func DockerRunner(image, executable string) Runner {
	return func(ctx context.Context, args []string, stdin string) (string, error) {
		argv := append([]string{"run", "--rm", "--interactive", "--network", "none", "--read-only",
			"--tmpfs", tmpfsHome, "--env", "HOME=/tmp",
			"--env", "XDG_CONFIG_HOME=/tmp/config", "--env", "XDG_DATA_HOME=/tmp/data",
			"--env", "XDG_STATE_HOME=/tmp/state", "--env", "XDG_CACHE_HOME=/tmp/cache",
			"--workdir", "/tmp", image, executable}, args...)
		cmd := exec.CommandContext(ctx, "docker", argv...)
		cmd.Stdin = strings.NewReader(stdin)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		return out.String(), err
	}
}

// Probe runs the contract's compatibility checks against the executable at
// the version and returns one report line per check. Any failed check is
// an error carrying every line, so the report reads whole.
func Probe(ctx context.Context, spec *ProbeSpec, version string, run Runner) ([]string, error) {
	if spec == nil {
		return []string{"contract carries no runtime_compatibility_probe; the install layer's own probes are the syntax gate"}, nil
	}
	var report []string
	failed := false
	fail := func(format string, args ...any) {
		failed = true
		report = append(report, fmt.Sprintf("FAIL "+format, args...))
	}
	if spec.ExactVersionRequired != "" {
		if spec.ExactVersionRequired == version {
			report = append(report, fmt.Sprintf("ok   %s is the version the contract measured", version))
		} else {
			fail("the contract is measured on %s exactly and %s is not it: %s", spec.ExactVersionRequired, version, spec.Reason)
		}
	}
	for _, c := range spec.commands() {
		out, err := run(ctx, c.Args, c.AcceptedStdin)
		label := strings.Join(c.Args, " ")
		if err != nil {
			fail("%s exited: %v\n%s", label, err, strings.TrimSpace(out))
			continue
		}
		if c.AcceptedStdin != "" {
			report = append(report, fmt.Sprintf("ok   %s accepted the contract's stdin", label))
		}
		for _, flag := range c.RequiredFlags {
			if flagPresent(out, flag) {
				report = append(report, fmt.Sprintf("ok   %s names %s", label, flag))
			} else {
				fail("%s no longer names %s", label, flag)
			}
		}
		for _, token := range c.RequiredUsageTokens {
			if usageTokenPresent(out, token) {
				report = append(report, fmt.Sprintf("ok   %s shows %s", label, token))
			} else {
				fail("%s no longer shows %s", label, token)
			}
		}
	}
	if failed {
		return report, errors.New("harnessbump: the contract's compatibility probe failed")
	}
	return report, nil
}

// ReceiveDockerRunner runs a receive probe's script inside the built
// image. `outbound` is the one network a receive probe may need: opencode
// installs its plugin SDK at bootstrap. Everything else stays on loopback
// with the syntax probes.
func ReceiveDockerRunner(image, network string) Runner {
	return func(ctx context.Context, args []string, stdin string) (string, error) {
		argv := []string{"run", "--rm", "--interactive", "--read-only",
			"--tmpfs", tmpfsHome, "--env", "HOME=/tmp"}
		if network != "outbound" {
			argv = append(argv, "--network", "none")
		}
		argv = append(argv, "--workdir", "/tmp", image, "python3")
		cmd := exec.CommandContext(ctx, "docker", append(append([]string{}, argv...), args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		return out.String(), err
	}
}

// probeDir is where the image installs the native receive probe suite.
const probeDir = "/usr/local/lib/noisedeck/a2a-probe"

// ProbeReceive runs the contract's native receive probe and returns its
// report lines. A nonzero exit is a failed gate carrying the probe's own
// diagnostics.
func ProbeReceive(ctx context.Context, spec *ReceiveProbeSpec, run Runner) ([]string, error) {
	if spec == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(spec.TimeoutS)*time.Second)
	defer cancel()
	fields := strings.Fields(spec.Script)
	args := append([]string{probeDir + "/" + fields[0]}, fields[1:]...)
	out, err := run(ctx, args, "")
	label := "native receive: " + spec.Script
	if err != nil {
		return []string{fmt.Sprintf("FAIL %s exited: %v\n%s", label, err, strings.TrimSpace(out))},
			errors.New("harnessbump: the native receive probe failed")
	}
	return []string{fmt.Sprintf("ok   %s passed against the real CLI", label)}, nil
}

// usageTokenPresent finds a usage token as a whole word, wherever the CLI
// documents it: codex prints [SESSION_ID] on its Usage line, omp prints
// MESSAGES under an ARGUMENTS header, and a contract should not have to know
// which. The word boundary stops a longer identifier from standing in for the
// token — MESSAGES_FILE is not MESSAGES — and nothing more: help text that
// merely mentions the word still satisfies it. This is a syntax probe, so the
// question it answers is whether the CLI still names the form, not whether it
// still means it.
func usageTokenPresent(out, token string) bool {
	re := regexp.MustCompile(`(^|[^\w])` + regexp.QuoteMeta(token) + `($|[^\w])`)
	return re.MatchString(out)
}

// flagPresent finds the flag as a whole token: `-s` must not be satisfied
// by `--sandbox`.
func flagPresent(out, flag string) bool {
	re := regexp.MustCompile(`(^|[\s,\[|])` + regexp.QuoteMeta(flag) + `($|[\s,=\]|>])`)
	return re.MatchString(out)
}
