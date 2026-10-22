package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const LauncherHelloSchemaV1 = "noisedeck.supervisor-launcher-hello.v1"

var (
	ErrInvalidLauncherHello  = errors.New("invalid supervisor launcher hello")
	ErrLauncherHelloDigest   = errors.New("supervisor launcher hello digest mismatch")
	ErrLauncherHelloEncoding = errors.New("supervisor launcher hello is not canonical")
)

// LauncherHelloV1 is the one canonical message the systemd launcher sends to
// the supervisor bootstrap endpoint. It carries only the supervisor-generated
// run identity and the launcher's own systemd invocation identity; no path,
// executable, argv, environment, plan, credential, or descriptor can be
// represented here.
type LauncherHelloV1 struct {
	Schema       string `json:"schema"`
	RunID        string `json:"run_id"`
	InvocationID string `json:"invocation_id"`
}

// LauncherHelloRevision is the exact hello identity retained by the
// supervisor and compared by the bootstrap endpoint.
type LauncherHelloRevision struct {
	Digest        string
	CanonicalJSON string
	Hello         LauncherHelloV1
}

// CanonicalizeLauncherHello validates and canonicalizes one hello.
func CanonicalizeLauncherHello(hello LauncherHelloV1) (*LauncherHelloRevision, error) {
	if err := validateLauncherHello(hello); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(hello)
	if err != nil {
		return nil, fmt.Errorf("%w: encode", ErrInvalidLauncherHello)
	}
	digest := sha256.Sum256(raw)
	return &LauncherHelloRevision{
		Digest:        hex.EncodeToString(digest[:]),
		CanonicalJSON: string(raw),
		Hello:         hello,
	}, nil
}

// VerifyLauncherHello is the authority-bearing decode path for a retained or
// received hello. It rejects unknown, duplicate, reordered, or otherwise
// non-canonical JSON rather than normalizing it into a different identity.
func VerifyLauncherHello(digest, canonicalJSON string) (*LauncherHelloRevision, error) {
	if !isLowerSHA256(digest) {
		return nil, ErrLauncherHelloDigest
	}
	var hello LauncherHelloV1
	decoder := json.NewDecoder(strings.NewReader(canonicalJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&hello); err != nil {
		return nil, ErrLauncherHelloEncoding
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, ErrLauncherHelloEncoding
	}
	revision, err := CanonicalizeLauncherHello(hello)
	if err != nil {
		return nil, err
	}
	if revision.CanonicalJSON != canonicalJSON {
		return nil, ErrLauncherHelloEncoding
	}
	if revision.Digest != digest {
		return nil, ErrLauncherHelloDigest
	}
	return revision, nil
}

func validateLauncherHello(hello LauncherHelloV1) error {
	if hello.Schema != LauncherHelloSchemaV1 {
		return ErrInvalidLauncherHello
	}
	if !isCanonicalUUIDv4(hello.RunID) {
		return ErrInvalidLauncherHello
	}
	if !isCanonicalInvocationID(hello.InvocationID) {
		return ErrInvalidLauncherHello
	}
	return nil
}

// isCanonicalInvocationID accepts the systemd invocation identity's canonical
// form: 32 lowercase hexadecimal characters encoding one 128-bit value.
func isCanonicalInvocationID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if (value[i] < '0' || value[i] > '9') && (value[i] < 'a' || value[i] > 'f') {
			return false
		}
	}
	return true
}

// parseLauncherHelloPayload decodes canonical bytes received over the
// transport into a verified hello. It is the receive-side counterpart of
// CanonicalizeLauncherHello and rejects any non-canonical encoding so a
// reordered, duplicate, trailing, unknown, or otherwise reserialized body
// never becomes a different-but-equal identity. Decode failures are wrapped
// content-free: a malformed JSON error can carry payload bytes, which the
// transport contract never reports.
func parseLauncherHelloPayload(payload []byte) (*LauncherHelloRevision, error) {
	body := string(payload)
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	var hello LauncherHelloV1
	if err := decoder.Decode(&hello); err != nil {
		return nil, ErrLauncherHelloEncoding
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, ErrLauncherHelloEncoding
	}
	revision, err := CanonicalizeLauncherHello(hello)
	if err != nil {
		return nil, err
	}
	if revision.CanonicalJSON != body {
		return nil, ErrLauncherHelloEncoding
	}
	return revision, nil
}
