package provider

// The identity hook assets and the paths provisioning installs them at.
// Deck never writes provider configuration: noisedeck-provision owns
// these files, and the descriptors here only name them.
//
// The hook formats are NOT interchangeable (doc.go): Codex nests
// event -> groups -> hook objects in JSON; Kimi uses flat TOML [[hooks]]
// tables; opencode, kilo, omp, and pi each load a provisioning-owned plugin
// file whose exact bytes are embedded below.

import (
	_ "embed"
	"fmt"
)

// OpencodeHookPlugin is the canonical identity plugin provisioning installs
// for opencode. Exported so noisedeck-provision writes exactly these bytes.
//
//go:embed assets/opencode-hook.js
var OpencodeHookPlugin []byte

// KiloHookPlugin is the canonical Kilo identity plugin provisioning installs.
//
//go:embed assets/kilo-hook.js
var KiloHookPlugin []byte

// OmpHookPlugin is the canonical identity capture hook provisioning installs
// for omp (Oh My Pi). omp loads it per launch via `--hook <path>` (Deck
// controls argv), so the file's location is Deck's choice.
//
//go:embed assets/omp-hook.ts
var OmpHookPlugin []byte

// PiHookPlugin is the canonical identity capture hook provisioning installs
// for pi. pi loads it per launch via `--extension <path>` (Deck controls
// argv); pi's extension discovery scans only <cwd>/.pi/extensions/ and
// ~/.pi/agent/extensions/, and the hook is installed at neither, so the flag
// is the only loader.
//
//go:embed assets/pi-hook.js
var PiHookPlugin []byte

// ErrHookNotProvisioned marks every failure whose remediation is
// provisioning's, not the user's in-app action: install or repair the
// documented hook entry in the named provider config file.
var ErrHookNotProvisioned = fmt.Errorf("provider: identity hook not provisioned")

// HookReceiverPath is the provisioned capture receiver every SessionStart
// hook invokes. A config whose SessionStart hooks point elsewhere is
// syntactically valid but can never deliver an identity to Deck.
const HookReceiverPath = "/usr/local/bin/noisedeck-provider-hook"
const CodexIdentityHookPath = "/usr/local/bin/noisedeck-codex-identity-hook"
