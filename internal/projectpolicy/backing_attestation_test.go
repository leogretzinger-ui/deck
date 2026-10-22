package projectpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func validBackingAttestation() ProjectBackingAttestationV1 {
	return ProjectBackingAttestationV1{
		Schema:                   ProjectBackingAttestationSchemaV1,
		AttestationID:            "00112233445566778899aabbccddeeff",
		RevisionID:               strings.Repeat("1", 32),
		SecurityDomainID:         strings.Repeat("2", 32),
		PolicyDigest:             strings.Repeat("3", 64),
		BackingDigest:            strings.Repeat("4", 64),
		ConfinementProfileDigest: strings.Repeat("5", 64),
	}
}

func validBackingBinding() BackingAttestationBinding {
	return BackingAttestationBinding{
		AttestationID:            "00112233445566778899aabbccddeeff",
		RevisionID:               strings.Repeat("1", 32),
		SecurityDomainID:         strings.Repeat("2", 32),
		PolicyDigest:             strings.Repeat("3", 64),
		BackingDigest:            strings.Repeat("4", 64),
		ConfinementProfileDigest: strings.Repeat("5", 64),
	}
}

// goldenBackingAttestationJSON and goldenBackingAttestationDigest are the
// exact canonical bytes and sha256 of validBackingAttestation, produced by
// CanonicalizeProjectBackingAttestation.
const goldenBackingAttestationJSON = `{"schema":"noisedeck.project-backing-attestation.v1","attestation_id":"00112233445566778899aabbccddeeff","revision_id":"11111111111111111111111111111111","security_domain_id":"22222222222222222222222222222222","policy_digest":"3333333333333333333333333333333333333333333333333333333333333333","backing_digest":"4444444444444444444444444444444444444444444444444444444444444444","confinement_profile_digest":"5555555555555555555555555555555555555555555555555555555555555555"}`
const goldenBackingAttestationDigest = "5b59b08beb6132aebd2a3ee7df1d8edaa1c5b301539135a7bc91f24d9eb8b72c"

func TestCanonicalizeBackingAttestationGolden(t *testing.T) {
	revision, err := CanonicalizeProjectBackingAttestation(validBackingAttestation())
	if err != nil {
		t.Fatalf("CanonicalizeProjectBackingAttestation: %v", err)
	}
	if revision.CanonicalJSON != goldenBackingAttestationJSON {
		t.Fatalf("canonical JSON =\n%s\nwant\n%s", revision.CanonicalJSON, goldenBackingAttestationJSON)
	}
	if revision.Digest != goldenBackingAttestationDigest {
		t.Fatalf("digest = %s, want %s", revision.Digest, goldenBackingAttestationDigest)
	}
	sum := sha256.Sum256([]byte(revision.CanonicalJSON))
	if revision.Digest != hex.EncodeToString(sum[:]) {
		t.Fatal("digest is not the sha256 of the canonical bytes")
	}
}

func TestVerifyBackingAttestationGoldenRoundTrip(t *testing.T) {
	revision, err := VerifyProjectBackingAttestation(
		validBackingBinding(), goldenBackingAttestationDigest, goldenBackingAttestationJSON)
	if err != nil {
		t.Fatalf("VerifyProjectBackingAttestation: %v", err)
	}
	if !reflect.DeepEqual(revision.Attestation, validBackingAttestation()) {
		t.Fatalf("decoded attestation = %+v, want %+v", revision.Attestation, validBackingAttestation())
	}
	again, err := VerifyProjectBackingAttestation(
		validBackingBinding(), revision.Digest, revision.CanonicalJSON)
	if err != nil {
		t.Fatalf("re-verify: %v", err)
	}
	if again.Digest != revision.Digest || again.CanonicalJSON != revision.CanonicalJSON {
		t.Fatal("verify is not idempotent")
	}
}

func TestVerifyBackingAttestationRejectsNonCanonicalJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
		err  error
	}{
		{
			"reordered fields",
			strings.Replace(goldenBackingAttestationJSON,
				`{"schema":"noisedeck.project-backing-attestation.v1","attestation_id":"00112233445566778899aabbccddeeff"`,
				`{"attestation_id":"00112233445566778899aabbccddeeff","schema":"noisedeck.project-backing-attestation.v1"`, 1),
			ErrBackingAttestationEncoding,
		},
		{
			"duplicate field",
			strings.Replace(goldenBackingAttestationJSON, `"schema":`,
				`"schema":"noisedeck.project-backing-attestation.v1","schema":`, 1),
			ErrBackingAttestationEncoding,
		},
		{"trailing value", goldenBackingAttestationJSON + `{}`, ErrBackingAttestationEncoding},
		{"leading whitespace", " " + goldenBackingAttestationJSON, ErrBackingAttestationEncoding},
		{
			"unknown field",
			strings.Replace(goldenBackingAttestationJSON, `"backing_digest":`,
				`"extra":1,"backing_digest":`, 1),
			ErrBackingAttestationEncoding,
		},
		{
			"uppercase id bytes",
			strings.Replace(goldenBackingAttestationJSON,
				"00112233445566778899aabbccddeeff", "00112233445566778899AABBCCDDEEFF", 1),
			ErrInvalidBackingAttestation,
		},
		{
			"uppercase digest bytes",
			strings.Replace(goldenBackingAttestationJSON,
				strings.Repeat("3", 64), strings.Repeat("3", 63)+"F", 1),
			ErrInvalidBackingAttestation,
		},
		{
			"short id",
			strings.Replace(goldenBackingAttestationJSON,
				"00112233445566778899aabbccddeeff", "00112233445566778899aabbccddee", 1),
			ErrInvalidBackingAttestation,
		},
		{
			"short digest",
			strings.Replace(goldenBackingAttestationJSON,
				strings.Repeat("3", 64), strings.Repeat("3", 63), 1),
			ErrInvalidBackingAttestation,
		},
		{
			"non-hex id",
			strings.Replace(goldenBackingAttestationJSON,
				"00112233445566778899aabbccddeeff", strings.Repeat("z", 32), 1),
			ErrInvalidBackingAttestation,
		},
		{
			"non-hex digest",
			strings.Replace(goldenBackingAttestationJSON,
				strings.Repeat("3", 64), strings.Repeat("g", 64), 1),
			ErrInvalidBackingAttestation,
		},
		{
			"schema drift",
			strings.Replace(goldenBackingAttestationJSON,
				"noisedeck.project-backing-attestation.v1", "noisedeck.project-backing-attestation.v2", 1),
			ErrInvalidBackingAttestation,
		},
		{
			"null collection",
			strings.Replace(goldenBackingAttestationJSON, `"backing_digest":`,
				`"backing_digest":null,"keep":`, 1),
			ErrBackingAttestationEncoding,
		},
		{
			"missing field",
			strings.Replace(goldenBackingAttestationJSON,
				`,"confinement_profile_digest":"`+strings.Repeat("5", 64)+`"`, "", 1),
			ErrInvalidBackingAttestation,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := VerifyProjectBackingAttestation(
				validBackingBinding(), goldenBackingAttestationDigest, test.body)
			if !errors.Is(err, test.err) {
				t.Fatalf("VerifyProjectBackingAttestation error = %v, want %v", err, test.err)
			}
		})
	}
}

func TestVerifyBackingAttestationRejectsBadDigest(t *testing.T) {
	if _, err := VerifyProjectBackingAttestation(
		validBackingBinding(), "not-a-digest", goldenBackingAttestationJSON); !errors.Is(err, ErrBackingAttestationDigest) {
		t.Fatalf("malformed digest error = %v, want %v", err, ErrBackingAttestationDigest)
	}
	tampered := strings.Replace(goldenBackingAttestationJSON,
		strings.Repeat("4", 64), strings.Repeat("4", 63)+"e", 1)
	tamperedBinding := validBackingBinding()
	tamperedBinding.BackingDigest = strings.Repeat("4", 63) + "e"
	sum := sha256.Sum256([]byte(tampered))
	if _, err := VerifyProjectBackingAttestation(
		tamperedBinding, hex.EncodeToString(sum[:]), tampered); err != nil {
		t.Fatalf("self-consistent tampered attestation should re-digest: %v", err)
	}
	if _, err := VerifyProjectBackingAttestation(
		validBackingBinding(), hex.EncodeToString(sum[:]), goldenBackingAttestationJSON); !errors.Is(err, ErrBackingAttestationDigest) {
		t.Fatalf("digest mismatch error = %v, want %v", err, ErrBackingAttestationDigest)
	}
}

func TestVerifyBackingAttestationRejectsBindingMismatch(t *testing.T) {
	binding := validBackingBinding()
	revision, err := VerifyProjectBackingAttestation(binding, goldenBackingAttestationDigest, goldenBackingAttestationJSON)
	if err != nil {
		t.Fatalf("matching binding rejected: %v", err)
	}
	if revision.Attestation.ConfinementProfileDigest != binding.ConfinementProfileDigest {
		t.Fatal("confinement profile digest did not bind")
	}
	mismatches := map[string]func(*BackingAttestationBinding){
		"attestation id":             func(b *BackingAttestationBinding) { b.AttestationID = strings.Repeat("a", 32) },
		"revision id":                func(b *BackingAttestationBinding) { b.RevisionID = strings.Repeat("b", 32) },
		"security domain id":         func(b *BackingAttestationBinding) { b.SecurityDomainID = strings.Repeat("c", 32) },
		"policy digest":              func(b *BackingAttestationBinding) { b.PolicyDigest = strings.Repeat("d", 64) },
		"backing digest":             func(b *BackingAttestationBinding) { b.BackingDigest = strings.Repeat("f", 64) },
		"confinement profile digest": func(b *BackingAttestationBinding) { b.ConfinementProfileDigest = strings.Repeat("e", 64) },
	}
	for name, mutate := range mismatches {
		t.Run(name, func(t *testing.T) {
			other := binding
			mutate(&other)
			if _, err := VerifyProjectBackingAttestation(
				other, goldenBackingAttestationDigest, goldenBackingAttestationJSON); !errors.Is(err, ErrBackingAttestationIdentity) {
				t.Fatalf("error = %v, want %v", err, ErrBackingAttestationIdentity)
			}
		})
	}
}

// Rejected evidence is attacker-influenced, so no attacker-chosen string may
// appear in a returned error.
func TestBackingAttestationErrorsDoNotEchoAttackerInput(t *testing.T) {
	const hostileName = "DECK214-\u2620-probe"

	attestation := validBackingAttestation()
	attestation.Schema = hostileName
	_, err := CanonicalizeProjectBackingAttestation(attestation)
	if !errors.Is(err, ErrInvalidBackingAttestation) {
		t.Fatalf("hostile schema error = %v, want %v", err, ErrInvalidBackingAttestation)
	}
	if strings.Contains(err.Error(), hostileName) {
		t.Fatalf("schema error echoes attacker input: %v", err)
	}

	// Markers use the decoder's own echo forms (escaped control bytes, the
	// offending character) so wrapping the raw json error fails this test.
	hostileBodies := []struct {
		marker string
		body   string
	}{
		{
			hostileName,
			strings.Replace(goldenBackingAttestationJSON, `"backing_digest":`,
				`"`+hostileName+`":1,"backing_digest":`, 1),
		},
		{
			`\x01`,
			strings.Replace(goldenBackingAttestationJSON,
				strings.Repeat("4", 64), strings.Repeat("4", 32)+"\x01"+strings.Repeat("4", 31), 1),
		},
		{"~", goldenBackingAttestationJSON + "~"},
	}
	for _, hostile := range hostileBodies {
		_, err := VerifyProjectBackingAttestation(
			validBackingBinding(), goldenBackingAttestationDigest, hostile.body)
		if !errors.Is(err, ErrBackingAttestationEncoding) {
			t.Fatalf("hostile body error = %v, want %v", err, ErrBackingAttestationEncoding)
		}
		if strings.Contains(err.Error(), hostile.marker) {
			t.Fatalf("decode error echoes attacker input: %v", err)
		}
	}

	_, err = VerifyProjectBackingAttestation(
		validBackingBinding(), hostileName, goldenBackingAttestationJSON)
	if !errors.Is(err, ErrBackingAttestationDigest) {
		t.Fatalf("hostile digest error = %v, want %v", err, ErrBackingAttestationDigest)
	}
	if strings.Contains(err.Error(), hostileName) {
		t.Fatalf("digest error echoes attacker input: %v", err)
	}
}

func TestBackingAttestationIdentityFieldsAreClosed(t *testing.T) {
	rejected := map[string]func(*ProjectBackingAttestationV1){
		"schema":            func(a *ProjectBackingAttestationV1) { a.Schema = "noisedeck.project-backing-attestation.v2" },
		"empty schema":      func(a *ProjectBackingAttestationV1) { a.Schema = "" },
		"upper attestation": func(a *ProjectBackingAttestationV1) { a.AttestationID = "00112233445566778899AABBCCDDEEFF" },
		"short revision":    func(a *ProjectBackingAttestationV1) { a.RevisionID = strings.Repeat("1", 31) },
		"long domain":       func(a *ProjectBackingAttestationV1) { a.SecurityDomainID = strings.Repeat("2", 33) },
		"non-hex policy":    func(a *ProjectBackingAttestationV1) { a.PolicyDigest = strings.Repeat("3", 63) + "g" },
		"upper backing":     func(a *ProjectBackingAttestationV1) { a.BackingDigest = strings.ToUpper(strings.Repeat("e", 64)) },
		"non-hex confinement": func(a *ProjectBackingAttestationV1) {
			a.ConfinementProfileDigest = strings.Repeat("5", 63) + "x"
		},
	}
	for name, mutate := range rejected {
		t.Run(name, func(t *testing.T) {
			attestation := validBackingAttestation()
			mutate(&attestation)
			if _, err := CanonicalizeProjectBackingAttestation(attestation); !errors.Is(err, ErrInvalidBackingAttestation) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidBackingAttestation)
			}
		})
	}
}

func TestCanonicalizeBackingAttestationDoesNotMutateInput(t *testing.T) {
	attestation := validBackingAttestation()
	clone := attestation
	if _, err := CanonicalizeProjectBackingAttestation(attestation); err != nil {
		t.Fatalf("CanonicalizeProjectBackingAttestation: %v", err)
	}
	if !reflect.DeepEqual(attestation, clone) {
		t.Fatalf("canonicalization mutated its input: %+v", attestation)
	}
}
