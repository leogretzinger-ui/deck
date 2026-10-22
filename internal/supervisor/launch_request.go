package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const LaunchRequestSchemaV1 = "noisedeck.supervisor-launch-request.v1"

var (
	ErrInvalidLaunchRequest  = errors.New("invalid supervisor launch request")
	ErrLaunchRequestDigest   = errors.New("supervisor launch request digest mismatch")
	ErrLaunchRequestEncoding = errors.New("supervisor launch request is not canonical")
)

// LaunchRequestV1 is the complete caller-visible launch shape. PTY file
// descriptors travel as authenticated ancillary data and the supervisor
// derives their binding; no path, executable, argv, environment, credential,
// or sandbox option can be represented here.
type LaunchRequestV1 struct {
	Schema                       string `json:"schema"`
	ApplicationSessionID         string `json:"application_session_id"`
	ApplicationSessionGeneration int64  `json:"application_session_generation"`
	ProjectPolicyRevision        string `json:"project_policy_revision"`
	ProviderProfileRevision      string `json:"provider_profile_revision"`
}

// LaunchRequestRevision is the exact request identity compared on an
// idempotent retry. The PTY binding is a separate supervisor-derived fact.
type LaunchRequestRevision struct {
	Digest        string
	CanonicalJSON string
	Request       LaunchRequestV1
}

func CanonicalizeLaunchRequest(request LaunchRequestV1) (*LaunchRequestRevision, error) {
	if err := validateLaunchRequest(request); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("%w: encode: %w", ErrInvalidLaunchRequest, err)
	}
	digest := sha256.Sum256(raw)
	return &LaunchRequestRevision{
		Digest:        hex.EncodeToString(digest[:]),
		CanonicalJSON: string(raw),
		Request:       request,
	}, nil
}

// VerifyLaunchRequest is the authority-bearing decode path for a retained or
// received request. It rejects unknown, duplicate, reordered, or otherwise
// non-canonical JSON rather than normalizing it into a different identity.
func VerifyLaunchRequest(digest, canonicalJSON string) (*LaunchRequestRevision, error) {
	if !isLowerSHA256(digest) {
		return nil, fmt.Errorf("%w: malformed digest", ErrLaunchRequestDigest)
	}
	var request LaunchRequestV1
	decoder := json.NewDecoder(strings.NewReader(canonicalJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("%w: decode: %w", ErrLaunchRequestEncoding, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLaunchRequestEncoding, err)
	}
	revision, err := CanonicalizeLaunchRequest(request)
	if err != nil {
		return nil, err
	}
	if revision.CanonicalJSON != canonicalJSON {
		return nil, ErrLaunchRequestEncoding
	}
	if revision.Digest != digest {
		return nil, ErrLaunchRequestDigest
	}
	return revision, nil
}

func validateLaunchRequest(request LaunchRequestV1) error {
	if request.Schema != LaunchRequestSchemaV1 {
		return fmt.Errorf("%w: unsupported schema %q", ErrInvalidLaunchRequest, request.Schema)
	}
	if !isCanonicalUUIDv4(request.ApplicationSessionID) {
		return fmt.Errorf("%w: application session id", ErrInvalidLaunchRequest)
	}
	if request.ApplicationSessionGeneration < 1 {
		return fmt.Errorf("%w: application session generation", ErrInvalidLaunchRequest)
	}
	if !isLowerSHA256(request.ProjectPolicyRevision) {
		return fmt.Errorf("%w: project policy revision", ErrInvalidLaunchRequest)
	}
	if !isLowerSHA256(request.ProviderProfileRevision) {
		return fmt.Errorf("%w: provider profile revision", ErrInvalidLaunchRequest)
	}
	return nil
}

func isCanonicalUUIDv4(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' ||
		value[18] != '-' || value[23] != '-' || value[14] != '4' {
		return false
	}
	if value[19] != '8' && value[19] != '9' && value[19] != 'a' && value[19] != 'b' {
		return false
	}
	for i := 0; i < len(value); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if (value[i] < '0' || value[i] > '9') && (value[i] < 'a' || value[i] > 'f') {
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
