package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"time"
)

const (
	AttestationSchemaV1           = "noisedeck.supervisor-attestation.v1"
	ConfinedLinuxProfileV1        = "confined-linux-v1"
	PinnedBubblewrapArchiveSHA256 = "69abc30005d2186baf7737feacd8da35633b93cf5af38838ecff17c5f8e924f6"
	RequiredCgroupV2Controllers   = "cpu,io,memory,pids"
)

var (
	ErrInvalidAttestation  = errors.New("invalid supervisor attestation")
	ErrAttestationDigest   = errors.New("supervisor attestation digest mismatch")
	ErrAttestationEncoding = errors.New("supervisor attestation is not canonical")
	ErrAttestationBinding  = errors.New("supervisor attestation does not match run binding")
)

// ConfinementProfile is the closed sandbox profile described by an
// attestation. Supporting another profile requires a new implementation and
// conformance record rather than caller-selected data.
type ConfinementProfile string

const ProfileConfinedLinuxV1 ConfinementProfile = ConfinedLinuxProfileV1

// ProbeResult is the closed result of one effective-state probe.
type ProbeResult string

const (
	ProbeEffective   ProbeResult = "effective"
	ProbeUnavailable ProbeResult = "unavailable"
)

// CgroupV2Hierarchy identifies the only cgroup hierarchy admitted by the
// profile. The required controllers are represented separately.
type CgroupV2Hierarchy string

const (
	CgroupV2Unified     CgroupV2Hierarchy = "unified"
	CgroupV2Unavailable CgroupV2Hierarchy = "unavailable"
)

// CgroupV2Controllers is a closed, canonical controller set. It cannot carry
// an arbitrary host-derived controller list.
type CgroupV2Controllers string

const (
	CgroupV2RequiredControllers    CgroupV2Controllers = RequiredCgroupV2Controllers
	CgroupV2ControllersUnavailable CgroupV2Controllers = "unavailable"
)

// CredentialClass is the closed class of credential exposed to the sandbox.
type CredentialClass string

const (
	CredentialNone                 CredentialClass = "none"
	CredentialProviderSubscription CredentialClass = "provider-subscription"
)

// UnavailableReason is a closed content-free explanation for evidence that
// cannot support a confined launch or reconciliation.
type UnavailableReason string

const (
	AttestationReasonNone             UnavailableReason = ""
	AttestationReasonHostPrimitive    UnavailableReason = "host_primitive"
	AttestationReasonRevisionMismatch UnavailableReason = "revision_mismatch"
	AttestationReasonVerificationFail UnavailableReason = "verification_failed"
	AttestationReasonUnverifiedState  UnavailableReason = "unverified_state"
)

// AttestationV1 is the closed canonical effective-state evidence shape for
// confined-linux-v1. It describes a protected supervisor run and cannot carry
// terminal content, a host path, or a free-text reason. Defining this record
// does not probe a host, launch a workload, or prove confinement.
type AttestationV1 struct {
	Schema                       string              `json:"schema"`
	ProfileName                  ConfinementProfile  `json:"profile_name"`
	ProfileRevision              string              `json:"profile_revision"`
	ApplicationSessionID         string              `json:"application_session_id"`
	ApplicationSessionGeneration int64               `json:"application_session_generation"`
	SupervisorRunID              string              `json:"supervisor_run_id"`
	SandboxGeneration            string              `json:"sandbox_generation"`
	BootID                       string              `json:"boot_id"`
	PTYBindingDigest             string              `json:"pty_binding_digest"`
	ProjectPolicyRevision        string              `json:"project_policy_revision"`
	ProviderProfileRevision      string              `json:"provider_profile_revision"`
	SupervisorBuildDigest        string              `json:"supervisor_build_digest"`
	KernelRelease                string              `json:"kernel_release"`
	NamespaceResult              ProbeResult         `json:"namespace_result"`
	SeccompResult                ProbeResult         `json:"seccomp_result"`
	BubblewrapArchiveDigest      string              `json:"bubblewrap_archive_digest"`
	BubblewrapExecutableDigest   string              `json:"bubblewrap_executable_digest"`
	CgroupV2Hierarchy            CgroupV2Hierarchy   `json:"cgroup_v2_hierarchy"`
	CgroupV2Controllers          CgroupV2Controllers `json:"cgroup_v2_controllers"`
	NetworkPolicyRevision        string              `json:"network_policy_revision"`
	CredentialClass              CredentialClass     `json:"credential_class"`
	LifecycleState               Phase               `json:"lifecycle_state"`
	UnavailableReason            UnavailableReason   `json:"unavailable_reason"`
	LaunchTime                   string              `json:"launch_time"`
	LastReconciliationTime       string              `json:"last_reconciliation_time"`
}

// AttestationRevision is the exact identity retained for an attestation.
type AttestationRevision struct {
	Digest        string
	CanonicalJSON string
	Attestation   AttestationV1
}

// AttestationBinding contains the protected identities the supervisor must
// compare with its run record. Copies supplied in evidence grant no authority.
type AttestationBinding struct {
	ProfileRevision              string
	ApplicationSessionID         string
	ApplicationSessionGeneration int64
	SupervisorRunID              string
	SandboxGeneration            string
	BootID                       string
	PTYBindingDigest             string
	ProjectPolicyRevision        string
	ProviderProfileRevision      string
	NetworkPolicyRevision        string
}

// CanonicalizeAttestation validates and digests one attestation.
func CanonicalizeAttestation(attestation AttestationV1) (*AttestationRevision, error) {
	if !validateAttestation(attestation) {
		return nil, ErrInvalidAttestation
	}
	raw, err := json.Marshal(attestation)
	if err != nil {
		return nil, ErrInvalidAttestation
	}
	digest := sha256.Sum256(raw)
	return &AttestationRevision{
		Digest:        hex.EncodeToString(digest[:]),
		CanonicalJSON: string(raw),
		Attestation:   attestation,
	}, nil
}

// VerifyAttestation rejects noncanonical input and compares every protected
// identity with the authority-bearing run binding.
func VerifyAttestation(digest, canonicalJSON string, binding AttestationBinding) (*AttestationRevision, error) {
	if !isLowerSHA256(digest) {
		return nil, ErrAttestationDigest
	}
	var attestation AttestationV1
	decoder := json.NewDecoder(strings.NewReader(canonicalJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&attestation); err != nil {
		return nil, ErrAttestationEncoding
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, ErrAttestationEncoding
	}
	revision, err := CanonicalizeAttestation(attestation)
	if err != nil {
		return nil, err
	}
	if revision.CanonicalJSON != canonicalJSON {
		return nil, ErrAttestationEncoding
	}
	if revision.Digest != digest {
		return nil, ErrAttestationDigest
	}
	if !attestationMatchesBinding(revision.Attestation, binding) {
		return nil, ErrAttestationBinding
	}
	return revision, nil
}

func validateAttestation(attestation AttestationV1) bool {
	// The observed executable digest must equal the compiled pin for this
	// target; the release archive digest can never substitute for it.
	if err := VerifyBubblewrapExecutableSHA256(
		runtime.GOOS, runtime.GOARCH, attestation.BubblewrapExecutableDigest); err != nil {
		return false
	}
	if attestation.Schema != AttestationSchemaV1 ||
		attestation.ProfileName != ProfileConfinedLinuxV1 ||
		!isLowerSHA256(attestation.ProfileRevision) ||
		!isCanonicalUUIDv4(attestation.ApplicationSessionID) ||
		attestation.ApplicationSessionGeneration < 1 ||
		!isCanonicalUUIDv4(attestation.SupervisorRunID) ||
		!isCanonicalUUIDv4(attestation.SandboxGeneration) ||
		!isCanonicalBootID(attestation.BootID) ||
		!isLowerSHA256(attestation.PTYBindingDigest) ||
		!isLowerSHA256(attestation.ProjectPolicyRevision) ||
		!isLowerSHA256(attestation.ProviderProfileRevision) ||
		!isLowerSHA256(attestation.SupervisorBuildDigest) ||
		!isASCIIAtom(attestation.KernelRelease) ||
		!validProbeResult(attestation.NamespaceResult) ||
		!validProbeResult(attestation.SeccompResult) ||
		attestation.BubblewrapArchiveDigest != PinnedBubblewrapArchiveSHA256 ||
		!validCgroupV2Hierarchy(attestation.CgroupV2Hierarchy) ||
		!validCgroupV2Controllers(attestation.CgroupV2Controllers) ||
		!isLowerSHA256(attestation.NetworkPolicyRevision) ||
		!validCredentialClass(attestation.CredentialClass) ||
		!validLifecycleState(attestation.LifecycleState) ||
		!validUnavailableReason(attestation.UnavailableReason) ||
		!isCanonicalStoreTime(attestation.LaunchTime) ||
		!isCanonicalStoreTime(attestation.LastReconciliationTime) ||
		attestation.LastReconciliationTime < attestation.LaunchTime {
		return false
	}
	if attestationHasUnavailableEvidence(attestation) &&
		attestation.UnavailableReason == AttestationReasonNone {
		return false
	}
	return true
}

func attestationMatchesBinding(attestation AttestationV1, binding AttestationBinding) bool {
	return attestation.ProfileRevision == binding.ProfileRevision &&
		attestation.ApplicationSessionID == binding.ApplicationSessionID &&
		attestation.ApplicationSessionGeneration == binding.ApplicationSessionGeneration &&
		attestation.SupervisorRunID == binding.SupervisorRunID &&
		attestation.SandboxGeneration == binding.SandboxGeneration &&
		attestation.BootID == binding.BootID &&
		attestation.PTYBindingDigest == binding.PTYBindingDigest &&
		attestation.ProjectPolicyRevision == binding.ProjectPolicyRevision &&
		attestation.ProviderProfileRevision == binding.ProviderProfileRevision &&
		attestation.NetworkPolicyRevision == binding.NetworkPolicyRevision
}

func attestationHasUnavailableEvidence(attestation AttestationV1) bool {
	return attestation.NamespaceResult == ProbeUnavailable ||
		attestation.SeccompResult == ProbeUnavailable ||
		attestation.CgroupV2Hierarchy == CgroupV2Unavailable ||
		attestation.CgroupV2Controllers == CgroupV2ControllersUnavailable
}

func validProbeResult(result ProbeResult) bool {
	return result == ProbeEffective || result == ProbeUnavailable
}

func validCgroupV2Hierarchy(hierarchy CgroupV2Hierarchy) bool {
	return hierarchy == CgroupV2Unified || hierarchy == CgroupV2Unavailable
}

func validCgroupV2Controllers(controllers CgroupV2Controllers) bool {
	return controllers == CgroupV2RequiredControllers || controllers == CgroupV2ControllersUnavailable
}

func validCredentialClass(class CredentialClass) bool {
	return class == CredentialNone || class == CredentialProviderSubscription
}

func validLifecycleState(state Phase) bool {
	switch state {
	case PhasePrepared, PhaseStarting, PhaseAuthorized, PhaseRunning, PhaseTerminal:
		return true
	}
	return false
}

func validUnavailableReason(reason UnavailableReason) bool {
	switch reason {
	case AttestationReasonNone, AttestationReasonHostPrimitive,
		AttestationReasonRevisionMismatch, AttestationReasonVerificationFail,
		AttestationReasonUnverifiedState:
		return true
	}
	return false
}

func isCanonicalStoreTime(value string) bool {
	parsed, err := time.Parse(storeTimeFormat, value)
	if err != nil {
		return false
	}
	return parsed.UTC().Format(storeTimeFormat) == value
}
