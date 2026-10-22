// Package projectpolicy defines the closed noisedeck.project-policy.v1
// authority value: the canonical policy a confined project binds for every run
// of one revision. Canonicalization validates shape, not
// provenance: only an authenticated user control surface may create or
// approve a revision, and that admission path is owned elsewhere.
//
// The policy commits numbers and closed identities only. The version-pinned
// confinement-profile digest commits the substrate semantics that make the
// numbers enforceable: how they map onto cgroup v2 controls, how I/O rates are
// applied per supervisor-resolved backing device, how devices coalesce, and
// which startup probes must pass. An unresolved backing device or a readback
// mismatch fails the confined launch closed.
package projectpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"net"
	"strings"
)

// ProjectPolicySchemaV1 is the only supported policy schema.
const ProjectPolicySchemaV1 = "noisedeck.project-policy.v1"

// Bounds on the closed collections. A policy may admit no destination (the
// first proof slice has no external network) and no input, but never more than
// these counts.
const (
	MaxDestinations = 32
	MaxInputs       = 64
)

// cgroup v2 cpu.max accepts periods of 1 ms to 1 s and a quota of at least
// 1 ms; a committed CPU cap outside those bounds could never be enforced by
// the selected substrate.
const (
	cpuPeriodMinUsec = 1000
	cpuPeriodMaxUsec = 1000000
	cpuQuotaMinUsec  = 1000
	cpuUsecPerSecond = 1000000
)

const (
	maxHostBytes       = 253
	maxLabelBytes      = 63
	maxRepositoryBytes = 255
)

var (
	ErrInvalidPolicy  = errors.New("invalid project policy")
	ErrPolicyDigest   = errors.New("project policy digest mismatch")
	ErrPolicyEncoding = errors.New("project policy is not canonical")
)

// RemotePermissionClass is the mapped remote's credential permission class.
// Administration is not a representable class: organization administration and
// your account-wide authority stay outside every project domain.
type RemotePermissionClass string

const (
	// RemotePermissionRead admits authenticated read access to the mapped
	// remote repository only.
	RemotePermissionRead RemotePermissionClass = "read"
	// RemotePermissionContribute is the minimum practical contributor
	// permission set the source-control platform offers for that one
	// repository.
	RemotePermissionContribute RemotePermissionClass = "contribute"
)

// MappedRemoteV1 is the one stable remote-repository identity its permission
// policy, never an issued token or expiry.
type MappedRemoteV1 struct {
	Repository      string                `json:"repository"`
	PermissionClass RemotePermissionClass `json:"permission_class"`
}

// DestinationV1 is one approved network destination. The bytes are canonical:
// Host is case-folded ASCII, an IP literal must already be in canonical text
// form, and legacy numeric spellings are rejected rather than mapped.
type DestinationV1 struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
}

// InputV1 is one admitted immutable input: a content-addressed snapshot bound
// to its exact digest and byte size, carrying no path, name, or mutable state.
type InputV1 struct {
	SHA256 string `json:"sha256"`
	Bytes  uint64 `json:"bytes"`
}

// CPULimitV1 is the hard CPU cap. QuotaPerSecUsec and PeriodUsec are committed
// as numbers; their mapping onto cgroup v2 cpu.max belongs to the confinement
// profile.
type CPULimitV1 struct {
	QuotaPerSecUsec uint64 `json:"quota_per_sec_usec"`
	PeriodUsec      uint64 `json:"period_usec"`
}

// IOLimitV1 is the per-device I/O cap. The supervisor resolves the backing
// device of each bounded store and applies these rates to it; an unresolved
// device or a readback mismatch fails the confined launch closed.
type IOLimitV1 struct {
	ReadBytesPerSec  uint64 `json:"read_bytes_per_sec"`
	WriteBytesPerSec uint64 `json:"write_bytes_per_sec"`
	ReadOpsPerSec    uint64 `json:"read_ops_per_sec"`
	WriteOpsPerSec   uint64 `json:"write_ops_per_sec"`
}

// ResourceLimitsV1 is the closed resource set. Every value is an explicit
// positive number: zero, an omitted default, and an unlimited sentinel are all
// invalid. OutputBytes is the cumulative content-blind PTY output byte budget
// for one run, and overage terminates the run for resource_limit.
// ExchangeBytes bounds the aggregate retained immutable exchange-store bytes
// for one security-domain revision; a Continuity route may only lower it.
type ResourceLimitsV1 struct {
	Processes     uint64     `json:"processes"`
	MemoryBytes   uint64     `json:"memory_bytes"`
	OutputBytes   uint64     `json:"output_bytes"`
	ProjectBytes  uint64     `json:"project_bytes"`
	ScratchBytes  uint64     `json:"scratch_bytes"`
	ProjectInodes uint64     `json:"project_inodes"`
	ScratchInodes uint64     `json:"scratch_inodes"`
	ExchangeBytes uint64     `json:"exchange_bytes"`
	CPU           CPULimitV1 `json:"cpu"`
	IO            IOLimitV1  `json:"io"`
}

// ProjectPolicyV1 is the complete closed policy shape. SecurityDomainID is the
// stable domain identity; BackingDigest binds the supervisor-attested managed
// backing; ConfinementProfileDigest binds the pinned sandbox substrate profile
// revision; ProviderProfileDigest binds the pinned provider manifest whose
// canonical digest is defined in the confined-linux-v1 profile.
type ProjectPolicyV1 struct {
	Schema                   string           `json:"schema"`
	SecurityDomainID         string           `json:"security_domain_id"`
	BackingDigest            string           `json:"backing_digest"`
	ConfinementProfileDigest string           `json:"confinement_profile_digest"`
	ProviderProfileDigest    string           `json:"provider_profile_digest"`
	MemoryScopeDigest        string           `json:"memory_scope_digest"`
	MappedRemote             MappedRemoteV1   `json:"mapped_remote"`
	Destinations             []DestinationV1  `json:"destinations"`
	Inputs                   []InputV1        `json:"inputs"`
	Resources                ResourceLimitsV1 `json:"resources"`
}

// ProjectPolicyRevision is the exact policy identity a run binds for its
// lifetime.
type ProjectPolicyRevision struct {
	Digest        string
	CanonicalJSON string
	Policy        ProjectPolicyV1
}

// CanonicalizeProjectPolicy validates one policy and digests its canonical
// form. The input value is never mutated: destination and repository hosts are
// case-folded into a copy while exact repository path bytes remain committed.
func CanonicalizeProjectPolicy(policy ProjectPolicyV1) (*ProjectPolicyRevision, error) {
	normalized, err := normalizeProjectPolicy(policy)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(*normalized)
	if err != nil {
		return nil, fmt.Errorf("%w: encode: %w", ErrInvalidPolicy, err)
	}
	digest := sha256.Sum256(raw)
	return &ProjectPolicyRevision{
		Digest:        hex.EncodeToString(digest[:]),
		CanonicalJSON: string(raw),
		Policy:        *normalized,
	}, nil
}

// VerifyProjectPolicy is the authority-bearing decode path for a retained or
// received policy. It rejects unknown, duplicate, reordered, or otherwise
// non-canonical JSON rather than normalizing it into a different identity.
func VerifyProjectPolicy(digest, canonicalJSON string) (*ProjectPolicyRevision, error) {
	if !isLowerHex(digest, 64) {
		return nil, fmt.Errorf("%w: malformed digest", ErrPolicyDigest)
	}
	var policy ProjectPolicyV1
	decoder := json.NewDecoder(strings.NewReader(canonicalJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return nil, fmt.Errorf("%w: decode: %w", ErrPolicyEncoding, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPolicyEncoding, err)
	}
	revision, err := CanonicalizeProjectPolicy(policy)
	if err != nil {
		return nil, err
	}
	if revision.CanonicalJSON != canonicalJSON {
		return nil, ErrPolicyEncoding
	}
	if revision.Digest != digest {
		return nil, ErrPolicyDigest
	}
	return revision, nil
}

// normalizeProjectPolicy validates shape and returns the canonical-form copy
// whose bytes are digested. Collections are made non-nil so the canonical form
// of an empty set is [].
func normalizeProjectPolicy(policy ProjectPolicyV1) (*ProjectPolicyV1, error) {
	invalid := func(field string) error {
		return fmt.Errorf("%w: %s", ErrInvalidPolicy, field)
	}
	if policy.Schema != ProjectPolicySchemaV1 {
		return nil, fmt.Errorf("%w: unsupported schema %q", ErrInvalidPolicy, policy.Schema)
	}
	if !isLowerHex(policy.SecurityDomainID, 32) {
		return nil, invalid("security domain id")
	}
	if !isLowerHex(policy.BackingDigest, 64) {
		return nil, invalid("backing digest")
	}
	if !isLowerHex(policy.ConfinementProfileDigest, 64) {
		return nil, invalid("confinement profile digest")
	}
	if !isLowerHex(policy.ProviderProfileDigest, 64) {
		return nil, invalid("provider profile digest")
	}
	if !isLowerHex(policy.MemoryScopeDigest, 64) {
		return nil, invalid("memory scope digest")
	}

	repository, ok := canonicalRepository(policy.MappedRemote.Repository)
	if !ok {
		return nil, invalid("mapped remote repository")
	}
	switch policy.MappedRemote.PermissionClass {
	case RemotePermissionRead, RemotePermissionContribute:
	default:
		return nil, invalid("mapped remote permission class")
	}

	if len(policy.Destinations) > MaxDestinations {
		return nil, invalid("destination count")
	}
	destinations := make([]DestinationV1, len(policy.Destinations))
	seen := make(map[DestinationV1]struct{}, len(policy.Destinations))
	for i, destination := range policy.Destinations {
		host, ok := canonicalHost(destination.Host)
		if !ok {
			return nil, fmt.Errorf("%w: destination %d host", ErrInvalidPolicy, i)
		}
		if destination.Port == 0 {
			return nil, fmt.Errorf("%w: destination %d port", ErrInvalidPolicy, i)
		}
		destinations[i] = DestinationV1{Host: host, Port: destination.Port}
		if _, duplicate := seen[destinations[i]]; duplicate {
			return nil, fmt.Errorf("%w: duplicate destination %d", ErrInvalidPolicy, i)
		}
		seen[destinations[i]] = struct{}{}
	}

	if len(policy.Inputs) > MaxInputs {
		return nil, invalid("input count")
	}
	inputs := make([]InputV1, len(policy.Inputs))
	digests := make(map[string]struct{}, len(policy.Inputs))
	for i, input := range policy.Inputs {
		if !isLowerHex(input.SHA256, 64) {
			return nil, fmt.Errorf("%w: input %d digest", ErrInvalidPolicy, i)
		}
		if !positiveBound(input.Bytes) {
			return nil, fmt.Errorf("%w: input %d bytes", ErrInvalidPolicy, i)
		}
		inputs[i] = input
		if _, duplicate := digests[input.SHA256]; duplicate {
			return nil, fmt.Errorf("%w: duplicate input %d", ErrInvalidPolicy, i)
		}
		digests[input.SHA256] = struct{}{}
	}

	resources := policy.Resources
	for field, value := range map[string]uint64{
		"processes":      resources.Processes,
		"memory bytes":   resources.MemoryBytes,
		"output bytes":   resources.OutputBytes,
		"project bytes":  resources.ProjectBytes,
		"scratch bytes":  resources.ScratchBytes,
		"project inodes": resources.ProjectInodes,
		"scratch inodes": resources.ScratchInodes,
		"exchange bytes": resources.ExchangeBytes,
		"io read bytes":  resources.IO.ReadBytesPerSec,
		"io write bytes": resources.IO.WriteBytesPerSec,
		"io read ops":    resources.IO.ReadOpsPerSec,
		"io write ops":   resources.IO.WriteOpsPerSec,
	} {
		if !positiveBound(value) {
			return nil, invalid(field)
		}
	}
	if !positiveBound(resources.CPU.QuotaPerSecUsec) {
		return nil, invalid("cpu quota")
	}
	if resources.CPU.PeriodUsec < cpuPeriodMinUsec || resources.CPU.PeriodUsec > cpuPeriodMaxUsec {
		return nil, invalid("cpu period")
	}
	if quota, ok := cpuQuotaForPeriod(resources.CPU); !ok || quota < cpuQuotaMinUsec {
		return nil, invalid("cpu quota and period")
	}

	normalized := policy
	normalized.MappedRemote = MappedRemoteV1{
		Repository:      repository,
		PermissionClass: policy.MappedRemote.PermissionClass,
	}
	normalized.Destinations = destinations
	normalized.Inputs = inputs
	return &normalized, nil
}

// positiveBound admits an explicit positive number. Zero means "unset" and
// MaxUint64 is the legacy unlimited sentinel (RLIM_INFINITY, cgroup "max");
// neither is a committed bound.
func positiveBound(value uint64) bool {
	return value != 0 && value != math.MaxUint64
}

// canonicalHost case-folds one host and reports whether the folded form is
// canonical. Case is presentation, not identity. An IP literal must be in its
// canonical text form already: non-canonical and legacy numeric spellings
// (leading zeros, shorthand quads, expanded IPv6) are rejected rather than
// mapped to the address they happen to denote. A dotted name whose labels are
// all numeric is address-shaped and rejected as a hostname.
func canonicalHost(host string) (string, bool) {
	if host == "" || len(host) > maxHostBytes {
		return "", false
	}
	for i := range host {
		if host[i] > 0x7f {
			return "", false
		}
	}
	folded := strings.ToLower(host)
	if ip := net.ParseIP(folded); ip != nil {
		return folded, ip.String() == folded
	}
	return folded, validDNSHost(folded)
}

func validDNSHost(folded string) bool {
	addressShaped := true
	for _, label := range strings.Split(folded, ".") {
		if len(label) == 0 || len(label) > maxLabelBytes {
			return false
		}
		for i := range label {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			case c == '-':
				if i == 0 || i == len(label)-1 {
					return false
				}
			default:
				return false
			}
		}
		if !legacyNumericComponent(label) {
			addressShaped = false
		}
	}
	return !addressShaped
}

func legacyNumericComponent(label string) bool {
	return allDigits(label) ||
		(len(label) > 2 && strings.HasPrefix(label, "0x") && isASCIIHex(label[2:]))
}

// cpuQuotaForPeriod maps the per-second allowance to cpu.max quota units by
// flooring quota_per_sec_usec*period_usec/1_000_000. Flooring is deterministic
// and never widens the committed hard cap.
func cpuQuotaForPeriod(cpu CPULimitV1) (uint64, bool) {
	hi, lo := bits.Mul64(cpu.QuotaPerSecUsec, cpu.PeriodUsec)
	if hi >= cpuUsecPerSecond {
		return 0, false
	}
	quota, _ := bits.Div64(hi, lo, cpuUsecPerSecond)
	return quota, true
}

// canonicalRepository case-folds only the host and requires host/path form with
// plain path segments: "github.com/owner/repo". Path bytes remain exact because
// not every remote service assigns them case-insensitive identity.
func canonicalRepository(repository string) (string, bool) {
	if repository == "" || len(repository) > maxRepositoryBytes {
		return "", false
	}
	host, path, found := strings.Cut(repository, "/")
	if !found {
		return "", false
	}
	canonicalHostPart, ok := canonicalHost(host)
	if !ok || !strings.Contains(canonicalHostPart, ".") {
		// A repository identity needs a qualified host: without one,
		// "owner/repo" would be indistinguishable from "host/repo".
		return "", false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", false
		}
		for i := range segment {
			c := segment[i]
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') &&
				c != '-' && c != '.' && c != '_' {
				return "", false
			}
		}
	}
	return canonicalHostPart + "/" + path, true
}

func allDigits(label string) bool {
	for i := range label {
		if label[i] < '0' || label[i] > '9' {
			return false
		}
	}
	return true
}

func isASCIIHex(value string) bool {
	for i := range value {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return value != ""
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for i := range value {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
