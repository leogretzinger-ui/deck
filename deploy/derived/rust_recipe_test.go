package derived

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The rust recipe is a derived image, not a stage in Deck's own Dockerfile: a
// running container cannot be handed a layer, so an optional toolchain is
// either a derived image, a persistent workspace install, or a host mount.
// These pin the properties that make the derived image the auditable one.
func TestRustRecipeIsDerivedFromTheLocalImage(t *testing.T) {
	raw, err := os.ReadFile("rust.Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	recipe := string(raw)

	for _, required := range []string{
		"ARG DECK_IMAGE=deck:local",
		"FROM ${DECK_IMAGE}",
		// Copied from a pinned image rather than fetched by a network installer,
		// so a rebuild resolves to the same toolchain bytes.
		"FROM rust:1.98.0-bookworm@sha256:",
		"COPY --from=rust-toolchain /usr/local/rustup /usr/local/rustup",
		"COPY --from=rust-toolchain /usr/local/cargo /usr/local/cargo",
		// Tauri's published Debian prerequisites. A missing one fails at a
		// project's build rather than at image build, where nobody is watching.
		"libwebkit2gtk-4.1-dev",
		"libxdo-dev",
		"libayatana-appindicator3-dev",
		"librsvg2-dev",
		"pkg-config --exists webkit2gtk-4.1",
	} {
		if !strings.Contains(recipe, required) {
			t.Errorf("rust recipe is missing %q", required)
		}
	}

	if strings.Contains(recipe, "rustup-init") || strings.Contains(recipe, "sh.rustup.rs") {
		t.Error("the rust recipe installs Rust over the network instead of copying a pinned toolchain")
	}
	if strings.Contains(recipe, "deck:slim") {
		t.Error("the rust recipe defaults to slim, which carries no compiler for a Rust build to link against")
	}
	// The copied toolchain must not stay writable by the session user. The
	// upstream rust image chmods both trees a+w so anyone can rustup-update
	// in place, and COPY preserves that, so an image built from it leaves the
	// compiler writable by every session unless the modes are normalised.
	for _, required := range []string{
		"chown -R root:root /usr/local/rustup /usr/local/cargo",
		"chmod -R go-w /usr/local/rustup /usr/local/cargo",
		"test ! -w /usr/local/cargo/bin/cargo",
		"test ! -w /usr/local/rustup",
		// The compiler, not the rustup proxy symlink in cargo/bin that
		// `command -v rustc` resolves to: writability of the proxy says
		// nothing about the toolchain binary it dispatches to.
		`rustc_real="$(readlink -f "$(rustup which rustc)")"`,
		`test ! -w "$rustc_real"`,
		// Cargo keeps its locks at the CARGO_HOME root. Without them it
		// warns the home is read-only and proceeds with no file locking,
		// so parallel builds mutate the shared caches unsynchronised.
		// Match the creation, not just any mention: asserting on the lock
		// path alone is satisfied by the assertion line itself, so removing
		// the install would leave the guard green.
		// The trailing continuation makes the two distinguishable: without
		// it, ".package-cache" is a prefix of ".package-cache-mutate" and
		// the second line satisfies the check for the first.
		`/dev/null /usr/local/cargo/.package-cache \`,
		`/dev/null /usr/local/cargo/.package-cache-mutate \`,
		// The root itself stays non-writable: directory write permission
		// governs creating and removing entries, so a writable CARGO_HOME
		// would let a session replace bin/ wholesale.
		"test ! -w /usr/local/cargo",
		// A real build exercises the locking contract rather than asserting
		// the file modes and hoping.
		"cargo build --offline",
	} {
		if !strings.Contains(recipe, required) {
			t.Errorf("rust recipe does not make the toolchain read-only to the session user: missing %q", required)
		}
	}

	// It must end as the session user: a derived image that leaves root as the
	// final user would silently change how every session in it runs.
	// Instruction names are case-insensitive and may be indented, so the
	// comparison is on the normalised token rather than a literal prefix.
	last := ""
	for _, line := range strings.Split(recipe, "\n") {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "USER") {
			last = strings.Join(fields[1:], " ")
		}
	}
	if last != "${NOISEDECK_RUNTIME_UID}:${NOISEDECK_RUNTIME_GID}" {
		t.Fatalf("rust recipe's final USER = %q, want the runtime user", last)
	}
}

func TestRustProfileRestoresCargoWithoutDuplicatingPath(t *testing.T) {
	command := `PATH=/home/agent/.local/bin:/usr/local/bin:/usr/bin:/bin; ` +
		`. "$1"; . "$1"; printf '%s' "$PATH"`
	out, err := exec.Command("/bin/bash", "-c", command, "test", "rust-profile.sh").Output()
	if err != nil {
		t.Fatalf("source rust profile: %v", err)
	}
	if got := string(out); got != "/usr/local/cargo/bin:/home/agent/.local/bin:/usr/local/bin:/usr/bin:/bin" {
		t.Fatalf("PATH after sourcing twice = %q", got)
	}
}

// Deck's own Dockerfile must not grow an optional toolchain stage: that is the
// shape this recipe replaced.
func TestDeckImageShipsNoRustToolchain(t *testing.T) {
	raw, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(raw)
	// Guard the constructs that would reintroduce a toolchain stage, not the
	// bare words: a comment or an unrelated command mentioning cargo is not a
	// shipped toolchain, and a test that fires on one is a test people learn
	// to ignore.
	for _, forbidden := range []string{
		"AS tauri",
		"AS rust-toolchain",
		"COPY --from=rust-toolchain",
		"/usr/local/cargo",
		"/usr/local/rustup",
		"rustup-init",
	} {
		if strings.Contains(dockerfile, forbidden) {
			t.Errorf("Deck's Dockerfile contains %q; optional toolchains are derived images", forbidden)
		}
	}
}
