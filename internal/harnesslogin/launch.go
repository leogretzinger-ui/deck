package harnesslogin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const OpenerPath = "/usr/local/libexec/noisedeck-login/xdg-open"

// shippedVersions follows image pins automatically; it identifies packaged
// executables, not a separate manual approval for each upstream release.
var shippedVersions = map[string]string{"codex": "codex-cli " + codexVersion, "pi": piVersion}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1024 {
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(p)
}

// LaunchEnvironment is called before spawning a CLI, never during a login.
// It reads only the native --version command, not provider state or output
// from a conversation. Custom browser/PATH configuration remains native.
func LaunchEnvironment(ctx context.Context, provider, executable, socket string, environment []string) []string {
	return launchEnvironment(ctx, provider, executable, socket, environment, OpenerPath)
}

func launchEnvironment(ctx context.Context, provider, executable, socket string, environment []string, opener string) []string {
	want, ok := shippedVersions[provider]
	if !ok || (provider == "codex" && ShippedCodexExecutable(ctx, executable) == "") || !filepath.IsAbs(executable) || os.Getenv("BROWSER") != "" || (provider == "pi" && !shippedPiModules(executable)) {
		return nil
	}
	for _, assignment := range environment {
		key, _, _ := strings.Cut(assignment, "=")
		if key == "PATH" || key == "BROWSER" {
			return nil
		}
	}
	info, err := os.Stat(opener)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "--version")
	cmd.WaitDelay = 200 * time.Millisecond
	var output boundedOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if cmd.Run() != nil || strings.TrimSpace(output.String()) != want {
		return nil
	}
	path := os.Getenv("PATH")
	original, _ := exec.LookPath("xdg-open")
	if original != "" && !filepath.IsAbs(original) {
		return nil
	}
	if originalInfo, err := os.Stat(original); err == nil && os.SameFile(info, originalInfo) {
		return nil
	}
	return []string{
		"PATH=" + filepath.Dir(opener) + string(os.PathListSeparator) + path,
		"BROWSER=" + opener,
		"NOISEDECK_LOGIN_SOCKET=" + socket,
		"NOISEDECK_LOGIN_PROVIDER=" + provider,
		"NOISEDECK_LOGIN_ORIGINAL_PATH=" + path,
		"NOISEDECK_LOGIN_ORIGINAL_OPENER=" + original,
	}
}

// Pi releases declare either the exact shipped pi-ai version or its caret range.
// Native probes qualify the packaged OAuth behavior under that bounded contract.
func shippedPiModules(executable string) bool {
	real, err := filepath.EvalSymlinks(executable)
	if err != nil || filepath.Base(real) != "cli.js" || filepath.Base(filepath.Dir(real)) != "bundle" || filepath.Base(filepath.Dir(filepath.Dir(real))) != "dist" {
		return false
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(real)))
	var cli struct {
		Name         string            `json:"name"`
		Version      string            `json:"version"`
		Dependencies map[string]string `json:"dependencies"`
	}
	if !readPackageMetadata(filepath.Join(root, "package.json"), &cli) ||
		cli.Name != "@earendil-works/pi-coding-agent" || cli.Version != piVersion {
		return false
	}
	declaration := cli.Dependencies["@earendil-works/pi-ai"]
	if declaration != piVersion && declaration != "^"+piVersion {
		return false
	}
	var ai struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if !readPackageMetadata(filepath.Join(root, "node_modules", "@earendil-works", "pi-ai", "package.json"), &ai) || ai.Name != "@earendil-works/pi-ai" {
		return false
	}
	if declaration == piVersion {
		return ai.Version == piVersion
	}
	return stableCaretVersion(ai.Version, piVersion)
}

func packageVersion(path, name, version string) bool {
	var metadata struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	return readPackageMetadata(path, &metadata) && metadata.Name == name && metadata.Version == version
}

func readPackageMetadata(path string, metadata any) bool {
	// Only fixed package-manifest suffixes derived from the configured CLI executable reach this helper.
	file, err := os.Open(path) // #nosec G304 -- packaged code metadata, never a request-supplied path.
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return false
	}
	return json.Unmarshal(data, metadata) == nil
}

// Only canonical stable X.Y.Z versions are admitted. Prerelease and build
// qualifiers require review rather than extending the shipped caret policy.
func stableCaretVersion(version, minimum string) bool {
	got, ok := stableVersion(version)
	if !ok {
		return false
	}
	want, ok := stableVersion(minimum)
	if !ok {
		return false
	}
	for i := range got {
		if got[i] < want[i] {
			return false
		}
		if got[i] > want[i] {
			break
		}
	}
	// A caret permits changes only below the first nonzero component.
	// Compare components directly so an upper bound cannot overflow.
	if want[0] != 0 {
		return got[0] == want[0]
	}
	if want[1] != 0 {
		return got[0] == 0 && got[1] == want[1]
	}
	return got == want
}

func stableVersion(version string) ([3]uint64, bool) {
	var result [3]uint64
	parts := strings.Split(version, ".")
	if len(parts) != len(result) {
		return result, false
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return result, false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return result, false
			}
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return result, false
		}
		result[i] = value
	}
	return result, true
}

// ShippedCodexExecutable resolves only the shipped npm layout or a directly
// configured native binary. Unknown launchers keep native authentication.
func ShippedCodexExecutable(ctx context.Context, executable string) string {
	real, err := filepath.EvalSymlinks(executable)
	if err != nil || !filepath.IsAbs(real) {
		return ""
	}
	candidate := real
	if filepath.Base(real) == "codex.js" && filepath.Base(filepath.Dir(real)) == "bin" {
		root := filepath.Dir(filepath.Dir(real))
		if !packageVersion(filepath.Join(root, "package.json"), "@openai/codex", codexVersion) {
			return ""
		}
		arch, triple := "", ""
		switch runtime.GOARCH {
		case "amd64":
			arch, triple = "x64", "x86_64-unknown-linux-musl"
		case "arm64":
			arch, triple = "arm64", "aarch64-unknown-linux-musl"
		default:
			return ""
		}
		module := filepath.Join(root, "node_modules", "@openai", "codex-linux-"+arch)
		// npm aliases retain the platform package's published name and version.
		if !packageVersion(filepath.Join(module, "package.json"), "@openai/codex", codexVersion+"-linux-"+arch) {
			return ""
		}
		candidate = filepath.Join(module, "vendor", triple, "bin", "codex")
	}
	file, err := os.Open(candidate) // #nosec G304 -- configured executable or fixed packaged binary suffix.
	if err != nil {
		return ""
	}
	var magic [4]byte
	_, readErr := io.ReadFull(file, magic[:])
	_ = file.Close()
	if readErr != nil || string(magic[:]) != "\x7fELF" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, candidate, "--version")
	cmd.WaitDelay = 200 * time.Millisecond
	var output boundedOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if cmd.Run() != nil || strings.TrimSpace(output.String()) != shippedVersions["codex"] {
		return ""
	}
	return candidate
}
