package projectpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ProjectBackingAttestationSchemaV1 is the only supported backing-attestation
// schema.
const ProjectBackingAttestationSchemaV1 = "noisedeck.project-backing-attestation.v1"

var (
	ErrInvalidBackingAttestation  = errors.New("invalid project backing attestation")
	ErrBackingAttestationDigest   = errors.New("project backing attestation digest mismatch")
	ErrBackingAttestationEncoding = errors.New("project backing attestation is not canonical")
	ErrBackingAttestationIdentity = errors.New("project backing attestation identity mismatch")
)

// ProjectBackingAttestationV1 is the exact content-free evidence that the
// trusted supervisor resolved the managed backing named by one immutable policy
// revision. It binds only protected identities, the
// supervisor-attested backing digest, and the pinned confinement profile: no
// managed view, runtime, supervisor build, path, state, secret, or expiry field
// exists in the shape. Migration 020 persists its canonical bytes as opaque
// canonical_json; the meaning is established here.
type ProjectBackingAttestationV1 struct {
	Schema                   string `json:"schema"`
	AttestationID            string `json:"attestation_id"`
	RevisionID               string `json:"revision_id"`
	SecurityDomainID         string `json:"security_domain_id"`
	PolicyDigest             string `json:"policy_digest"`
	BackingDigest            string `json:"backing_digest"`
	ConfinementProfileDigest string `json:"confinement_profile_digest"`
}

// BackingAttestationRevision is the exact stored representation of one
// backing attestation.
type BackingAttestationRevision struct {
	Digest        string
	CanonicalJSON string
	Attestation   ProjectBackingAttestationV1
}

// BackingAttestationBinding is the exact policy identity a retained
// attestation must bind. It is supplied by the caller from an already-verified
// policy revision, never by the evidence bytes: a copied identity in the
// canonical object confers no authority by itself. The backing digest
// must equal the verified policy's ProjectPolicyV1.BackingDigest and the
// confinement profile digest must equal its
// ProjectPolicyV1.ConfinementProfileDigest.
type BackingAttestationBinding struct {
	AttestationID            string
	RevisionID               string
	SecurityDomainID         string
	PolicyDigest             string
	BackingDigest            string
	ConfinementProfileDigest string
}

// CanonicalizeProjectBackingAttestation validates one attestation and digests
// its canonical form. The input value is never mutated; the returned
// Attestation is a value copy of the validated fields.
func CanonicalizeProjectBackingAttestation(attestation ProjectBackingAttestationV1) (*BackingAttestationRevision, error) {
	if attestation.Schema != ProjectBackingAttestationSchemaV1 {
		return nil, fmt.Errorf("%w: unsupported schema", ErrInvalidBackingAttestation)
	}
	if !isLowerHex(attestation.AttestationID, 32) {
		return nil, fmt.Errorf("%w: attestation id", ErrInvalidBackingAttestation)
	}
	if !isLowerHex(attestation.RevisionID, 32) {
		return nil, fmt.Errorf("%w: revision id", ErrInvalidBackingAttestation)
	}
	if !isLowerHex(attestation.SecurityDomainID, 32) {
		return nil, fmt.Errorf("%w: security domain id", ErrInvalidBackingAttestation)
	}
	if !isLowerHex(attestation.PolicyDigest, 64) {
		return nil, fmt.Errorf("%w: policy digest", ErrInvalidBackingAttestation)
	}
	if !isLowerHex(attestation.BackingDigest, 64) {
		return nil, fmt.Errorf("%w: backing digest", ErrInvalidBackingAttestation)
	}
	if !isLowerHex(attestation.ConfinementProfileDigest, 64) {
		return nil, fmt.Errorf("%w: confinement profile digest", ErrInvalidBackingAttestation)
	}
	raw, err := json.Marshal(attestation)
	if err != nil {
		return nil, fmt.Errorf("%w: encode: %w", ErrInvalidBackingAttestation, err)
	}
	digest := sha256.Sum256(raw)
	return &BackingAttestationRevision{
		Digest:        hex.EncodeToString(digest[:]),
		CanonicalJSON: string(raw),
		Attestation:   attestation,
	}, nil
}

// VerifyProjectBackingAttestation is the authority-bearing decode path for a
// retained or received backing attestation. It rejects unknown, duplicate,
// reordered, or otherwise non-canonical JSON rather than normalizing it into a
// different identity, and it requires the attestation to bind the exact policy
// identity the caller supplies from an already-verified policy revision: the
// attestation id, revision id, security domain id, policy digest, backing
// digest, and confinement profile digest must all match. The binding value is
// never derived from the evidence bytes.
func VerifyProjectBackingAttestation(
	binding BackingAttestationBinding, digest, canonicalJSON string,
) (*BackingAttestationRevision, error) {
	if !isLowerHex(digest, 64) {
		return nil, fmt.Errorf("%w: malformed digest", ErrBackingAttestationDigest)
	}
	// The evidence bytes are attacker-influenced; decode failures are reported
	// as fixed text so no input fragment reaches an error consumer.
	var attestation ProjectBackingAttestationV1
	decoder := json.NewDecoder(strings.NewReader(canonicalJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&attestation); err != nil {
		return nil, fmt.Errorf("%w: decode", ErrBackingAttestationEncoding)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%w: trailing data", ErrBackingAttestationEncoding)
	}
	revision, err := CanonicalizeProjectBackingAttestation(attestation)
	if err != nil {
		return nil, err
	}
	if revision.CanonicalJSON != canonicalJSON {
		return nil, ErrBackingAttestationEncoding
	}
	if revision.Digest != digest {
		return nil, ErrBackingAttestationDigest
	}
	if !attestationMatchesBinding(revision.Attestation, binding) {
		return nil, ErrBackingAttestationIdentity
	}
	return revision, nil
}

// attestationMatchesBinding reports whether the canonical attestation binds
// exactly the caller-supplied policy identity.
func attestationMatchesBinding(attestation ProjectBackingAttestationV1, binding BackingAttestationBinding) bool {
	return attestation.AttestationID == binding.AttestationID &&
		attestation.RevisionID == binding.RevisionID &&
		attestation.SecurityDomainID == binding.SecurityDomainID &&
		attestation.PolicyDigest == binding.PolicyDigest &&
		attestation.BackingDigest == binding.BackingDigest &&
		attestation.ConfinementProfileDigest == binding.ConfinementProfileDigest
}
