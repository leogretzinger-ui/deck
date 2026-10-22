package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/noisefloor-oss/deck/internal/projectpolicy"
)

const SandboxPlanSchemaV1 = "noisedeck.sandbox-plan.v1"

// MaxSandboxPlanCanonicalBytes bounds the closed canonical plan bytes. A
// retained or received plan larger than this is rejected before decoding so a
// hostile sender cannot use the plan as a storage vector.
const MaxSandboxPlanCanonicalBytes = 4096

var (
	ErrInvalidSandboxPlan  = errors.New("invalid sandbox plan")
	ErrSandboxPlanDigest   = errors.New("sandbox plan digest mismatch")
	ErrSandboxPlanEncoding = errors.New("sandbox plan is not canonical")
	ErrSandboxPlanBinding  = errors.New("sandbox plan does not match protected binding")
	ErrSandboxPlanPolicy   = errors.New("sandbox plan does not match project policy")
)

// SandboxPlanRole is one closed managed-project-view role.
// The set and the role order are profile constants; a plan admits each role
// exactly once, in this order.
type SandboxPlanRole string

const (
	// SandboxRoleRuntime is the immutable runtime subtree carrying the pinned
	// provider executable. Its identity is the measured digest of that
	// immutable tree, never a host path.
	SandboxRoleRuntime SandboxPlanRole = "runtime"
	// SandboxRoleProject is the one managed project view, including the
	// project-local .worktrees/ and .review/ reservations. Its
	// identity is the policy's supervisor-attested managed backing digest.
	SandboxRoleProject SandboxPlanRole = "project"
	// SandboxRoleGitCommon is the in-backing Git common and administrative
	// state of the same project domain, never a live sibling or external
	// common directory. Its identity is a measured digest.
	SandboxRoleGitCommon SandboxPlanRole = "git_common"
	// SandboxRoleSessionState is the private per-application-session home,
	// provider history, and XDG state. Its identity is a measured digest.
	SandboxRoleSessionState SandboxPlanRole = "session_state"
	// SandboxRoleScratch is the bounded per-run scratch store. Its identity is
	// a measured digest; the byte and inode bounds live in the policy.
	SandboxRoleScratch SandboxPlanRole = "scratch"
	// SandboxRoleImports is the set of admitted immutable content-addressed
	// inputs. Its identity is the digest of the policy's canonical input set.
	SandboxRoleImports SandboxPlanRole = "imports"
)

// sandboxPlanRoleOrder is the canonical layout order. Every plan layout is
// exactly this sequence.
var sandboxPlanRoleOrder = []SandboxPlanRole{
	SandboxRoleRuntime,
	SandboxRoleProject,
	SandboxRoleGitCommon,
	SandboxRoleSessionState,
	SandboxRoleScratch,
	SandboxRoleImports,
}

// sandboxPlanDestinations maps each closed role to its one fixed destination
// inside the managed view. Destinations are confined-linux-v1 constants: no
// caller may select, add, reorder, or rename one, and the fixed set is
// pairwise disjoint at path-component boundaries so no destination can
// overlap or ancestor-shadow another.
var sandboxPlanDestinations = map[SandboxPlanRole]string{
	SandboxRoleRuntime:      "/usr",
	SandboxRoleProject:      "/project",
	SandboxRoleGitCommon:    "/project.git",
	SandboxRoleSessionState: "/home/agent",
	SandboxRoleScratch:      "/tmp",
	SandboxRoleImports:      "/imports",
}

// SandboxPlanBinding is one closed layout entry: a fixed role at its fixed
// destination, bound to one protected content-addressed identity. Symlink,
// special-file, and Git-common safety appear only through that measured
// digest; this record holds no host path, source, or probe result.
type SandboxPlanBinding struct {
	Role        SandboxPlanRole `json:"role"`
	Destination string          `json:"destination"`
	SHA256      string          `json:"sha256"`
}

// SandboxPlanV1 is the closed canonical managed-project-view plan the
// supervisor hands one authenticated launcher. It carries only supervisor-
// derived identities: the run, sandbox, and request bindings; the verified
// policy revision with its confinement- and provider-profile digests, which
// commit the resource and network authority; and the fixed role layout. It
// contains no host path, mount source, executable, argument, environment
// entry, UID, socket, credential, or option. Defining this record constructs,
// resolves, probes, or launches nothing.
type SandboxPlanV1 struct {
	Schema                   string               `json:"schema"`
	Profile                  ConfinementProfile   `json:"profile"`
	RunID                    string               `json:"run_id"`
	SandboxGeneration        string               `json:"sandbox_generation"`
	LaunchRequestDigest      string               `json:"launch_request_digest"`
	ProjectPolicyRevision    string               `json:"project_policy_revision"`
	ConfinementProfileDigest string               `json:"confinement_profile_digest"`
	ProviderProfileDigest    string               `json:"provider_profile_digest"`
	ResourcePolicyDigest     string               `json:"resource_policy_digest"`
	NetworkPolicyDigest      string               `json:"network_policy_digest"`
	Layout                   []SandboxPlanBinding `json:"layout"`
}

// SandboxPlanRevision is the exact plan identity retained by the supervisor
// and digest-bound into the launch ticket's sandbox-plan slot.
type SandboxPlanRevision struct {
	Digest        string
	CanonicalJSON string
	Plan          SandboxPlanV1
}

// SandboxPlanFacts are the protected supervisor-derived identities a plan is
// constructed from: the issued run and sandbox identities, the launch-request
// digest, an already-resolved and verified project-policy revision, and the
// measured content digests of the immutable runtime, the in-backing Git
// common state, the private application-session state, and the bounded
// scratch store. Nothing here is caller- or Deck-supplied, and no field is a
// host path.
type SandboxPlanFacts struct {
	RunID               string
	SandboxGeneration   string
	LaunchRequestDigest string
	Policy              *projectpolicy.ProjectPolicyRevision
	RuntimeRootSHA256   string
	GitCommonSHA256     string
	SessionStateSHA256  string
	ScratchSHA256       string
}

// DeriveSandboxPlan constructs the only managed-project-view plan
// confined-linux-v1 admits for one run. The policy revision must
// re-canonicalize to its own digest; a policy that cannot be re-derived is
// not a verified identity and never reaches a plan. The inputs are never
// mutated.
func DeriveSandboxPlan(facts SandboxPlanFacts) (*SandboxPlanRevision, error) {
	if !validSandboxPlanFacts(facts) {
		return nil, ErrInvalidSandboxPlan
	}
	if !sandboxPlanPolicyConsistent(facts.Policy) {
		return nil, ErrSandboxPlanPolicy
	}
	importsDigest, ok := sandboxPlanImportsDigest(facts.Policy)
	if !ok {
		return nil, ErrInvalidSandboxPlan
	}
	resourceDigest, networkDigest, ok := sandboxPlanPolicyComponentDigests(facts.Policy)
	if !ok {
		return nil, ErrInvalidSandboxPlan
	}

	identities := map[SandboxPlanRole]string{
		SandboxRoleRuntime:      facts.RuntimeRootSHA256,
		SandboxRoleProject:      facts.Policy.Policy.BackingDigest,
		SandboxRoleGitCommon:    facts.GitCommonSHA256,
		SandboxRoleSessionState: facts.SessionStateSHA256,
		SandboxRoleScratch:      facts.ScratchSHA256,
		SandboxRoleImports:      importsDigest,
	}
	plan := SandboxPlanV1{
		Schema:                   SandboxPlanSchemaV1,
		Profile:                  ProfileConfinedLinuxV1,
		RunID:                    facts.RunID,
		SandboxGeneration:        facts.SandboxGeneration,
		LaunchRequestDigest:      facts.LaunchRequestDigest,
		ProjectPolicyRevision:    facts.Policy.Digest,
		ConfinementProfileDigest: facts.Policy.Policy.ConfinementProfileDigest,
		ProviderProfileDigest:    facts.Policy.Policy.ProviderProfileDigest,
		ResourcePolicyDigest:     resourceDigest,
		NetworkPolicyDigest:      networkDigest,
		Layout:                   make([]SandboxPlanBinding, 0, len(sandboxPlanRoleOrder)),
	}
	for _, role := range sandboxPlanRoleOrder {
		plan.Layout = append(plan.Layout, SandboxPlanBinding{
			Role:        role,
			Destination: sandboxPlanDestinations[role],
			SHA256:      identities[role],
		})
	}
	if !validSandboxPlan(plan) {
		return nil, ErrInvalidSandboxPlan
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, ErrInvalidSandboxPlan
	}
	digest := sha256.Sum256(raw)
	return &SandboxPlanRevision{
		Digest:        hex.EncodeToString(digest[:]),
		CanonicalJSON: string(raw),
		Plan:          plan,
	}, nil
}

// VerifySandboxPlan is the authority-bearing decode path for a retained or
// received plan. It rejects an oversized, malformed, unknown, duplicate,
// reordered, trailing, or otherwise noncanonical body, any digest mismatch,
// any caller-selected or overlapping layout destination, and any plan that
// does not match the protected run, sandbox, request, or verified policy
// binding.
func VerifySandboxPlan(
	digest string,
	canonicalJSON string,
	facts SandboxPlanFacts,
) (*SandboxPlanRevision, error) {
	if !isLowerSHA256(digest) {
		return nil, ErrSandboxPlanDigest
	}
	if len(canonicalJSON) > MaxSandboxPlanCanonicalBytes {
		return nil, ErrSandboxPlanEncoding
	}
	if !validSandboxPlanFacts(facts) {
		return nil, ErrInvalidSandboxPlan
	}

	var plan SandboxPlanV1
	decoder := json.NewDecoder(strings.NewReader(canonicalJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return nil, ErrSandboxPlanEncoding
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, ErrSandboxPlanEncoding
	}
	if !validSandboxPlan(plan) {
		return nil, ErrInvalidSandboxPlan
	}
	raw, err := json.Marshal(plan)
	if err != nil || string(raw) != canonicalJSON {
		return nil, ErrSandboxPlanEncoding
	}
	actualDigest := sha256.Sum256(raw)
	if hex.EncodeToString(actualDigest[:]) != digest {
		return nil, ErrSandboxPlanDigest
	}

	expected, err := DeriveSandboxPlan(facts)
	if err != nil {
		return nil, err
	}
	if plan.RunID != expected.Plan.RunID ||
		plan.SandboxGeneration != expected.Plan.SandboxGeneration ||
		plan.LaunchRequestDigest != expected.Plan.LaunchRequestDigest {
		return nil, ErrSandboxPlanBinding
	}
	if plan.ProjectPolicyRevision != expected.Plan.ProjectPolicyRevision ||
		plan.ConfinementProfileDigest != expected.Plan.ConfinementProfileDigest ||
		plan.ProviderProfileDigest != expected.Plan.ProviderProfileDigest ||
		plan.ResourcePolicyDigest != expected.Plan.ResourcePolicyDigest ||
		plan.NetworkPolicyDigest != expected.Plan.NetworkPolicyDigest ||
		!slices.Equal(plan.Layout, expected.Plan.Layout) {
		return nil, ErrSandboxPlanPolicy
	}
	return expected, nil
}

// validSandboxPlanFacts admits only well-formed protected identities and a
// present policy revision.
func validSandboxPlanFacts(facts SandboxPlanFacts) bool {
	if facts.Policy == nil {
		return false
	}
	return isCanonicalUUIDv4(facts.RunID) &&
		isCanonicalUUIDv4(facts.SandboxGeneration) &&
		isLowerSHA256(facts.LaunchRequestDigest) &&
		isLowerSHA256(facts.RuntimeRootSHA256) &&
		isLowerSHA256(facts.GitCommonSHA256) &&
		isLowerSHA256(facts.SessionStateSHA256) &&
		isLowerSHA256(facts.ScratchSHA256)
}

// validSandboxPlan admits only the closed plan shape: the one schema and
// profile, canonical run identities, well-formed digests, and a layout that
// is exactly the fixed role sequence, each at its fixed destination with a
// well-formed measured identity. A caller-selected, duplicated, unknown,
// extra, missing, or reordered role or destination fails here.
func validSandboxPlan(plan SandboxPlanV1) bool {
	if plan.Schema != SandboxPlanSchemaV1 || plan.Profile != ProfileConfinedLinuxV1 {
		return false
	}
	if !isCanonicalUUIDv4(plan.RunID) || !isCanonicalUUIDv4(plan.SandboxGeneration) {
		return false
	}
	if !isLowerSHA256(plan.LaunchRequestDigest) ||
		!isLowerSHA256(plan.ProjectPolicyRevision) ||
		!isLowerSHA256(plan.ConfinementProfileDigest) ||
		!isLowerSHA256(plan.ProviderProfileDigest) ||
		!isLowerSHA256(plan.ResourcePolicyDigest) ||
		!isLowerSHA256(plan.NetworkPolicyDigest) {
		return false
	}
	if len(plan.Layout) != len(sandboxPlanRoleOrder) {
		return false
	}
	for i, binding := range plan.Layout {
		if binding.Role != sandboxPlanRoleOrder[i] {
			return false
		}
		if binding.Destination != sandboxPlanDestinations[binding.Role] {
			return false
		}
		if !isLowerSHA256(binding.SHA256) {
			return false
		}
	}
	return true
}

// sandboxPlanPolicyComponentDigests make the resource and destination
// assignments explicit in the plan while the complete policy revision remains
// their authority. Both inputs have already passed project-policy
// canonicalization, so their ordinary JSON encoding is stable.
func sandboxPlanPolicyComponentDigests(policy *projectpolicy.ProjectPolicyRevision) (string, string, bool) {
	resources, err := json.Marshal(policy.Policy.Resources)
	if err != nil {
		return "", "", false
	}
	destinations := policy.Policy.Destinations
	if destinations == nil {
		destinations = []projectpolicy.DestinationV1{}
	}
	network, err := json.Marshal(destinations)
	if err != nil {
		return "", "", false
	}
	resourceDigest := sha256.Sum256(resources)
	networkDigest := sha256.Sum256(network)
	return hex.EncodeToString(resourceDigest[:]), hex.EncodeToString(networkDigest[:]), true
}

// sandboxPlanPolicyConsistent re-derives the canonical digest from a policy
// revision's value. Only a revision that re-canonicalizes to its own digest
// is a verified identity.
func sandboxPlanPolicyConsistent(policy *projectpolicy.ProjectPolicyRevision) bool {
	rederived, err := projectpolicy.CanonicalizeProjectPolicy(policy.Policy)
	return err == nil && rederived.Digest == policy.Digest
}

// sandboxPlanImportsDigest is the content-addressed identity of the policy's
// admitted immutable inputs: the SHA-256 over their canonical JSON array
// bytes, which binds the exact input set and order committed by the policy
// revision.
func sandboxPlanImportsDigest(policy *projectpolicy.ProjectPolicyRevision) (string, bool) {
	inputs := policy.Policy.Inputs
	if inputs == nil {
		inputs = []projectpolicy.InputV1{}
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return "", false
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), true
}
