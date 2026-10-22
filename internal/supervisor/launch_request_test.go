package supervisor

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/noisefloor-oss/deck/internal/provider"
)

func TestLaunchRequestGolden(t *testing.T) {
	revision, err := CanonicalizeLaunchRequest(validLaunchRequest())
	if err != nil {
		t.Fatalf("CanonicalizeLaunchRequest: %v", err)
	}
	const wantJSON = `{"schema":"noisedeck.supervisor-launch-request.v1","application_session_id":"123e4567-e89b-42d3-a456-426614174000","application_session_generation":7,"project_policy_revision":"1111111111111111111111111111111111111111111111111111111111111111","provider_profile_revision":"2222222222222222222222222222222222222222222222222222222222222222"}`
	const wantDigest = "835ecac7acb85d3786a7626b4a9a632b0a44e1845f52f89b8013ca3fc1f37165"
	if revision.CanonicalJSON != wantJSON {
		t.Fatalf("canonical JSON = %s, want %s", revision.CanonicalJSON, wantJSON)
	}
	if revision.Digest != wantDigest {
		t.Fatalf("digest = %s, want %s", revision.Digest, wantDigest)
	}
	verified, err := VerifyLaunchRequest(revision.Digest, revision.CanonicalJSON)
	if err != nil {
		t.Fatalf("VerifyLaunchRequest: %v", err)
	}
	if !reflect.DeepEqual(verified, revision) {
		t.Fatalf("verified = %#v, want %#v", verified, revision)
	}
}

func TestLaunchRequestHasOnlyProtectedIdentifiers(t *testing.T) {
	typeOf := reflect.TypeOf(LaunchRequestV1{})
	want := []string{
		"schema",
		"application_session_id",
		"application_session_generation",
		"project_policy_revision",
		"provider_profile_revision",
	}
	if typeOf.NumField() != len(want) {
		t.Fatalf("field count = %d, want %d", typeOf.NumField(), len(want))
	}
	for i, tag := range want {
		if got := typeOf.Field(i).Tag.Get("json"); got != tag {
			t.Errorf("field %d JSON tag = %q, want %q", i, got, tag)
		}
	}
}

func TestLaunchRequestRejectsNonCanonicalAuthority(t *testing.T) {
	revision, err := CanonicalizeLaunchRequest(validLaunchRequest())
	if err != nil {
		t.Fatalf("CanonicalizeLaunchRequest: %v", err)
	}
	tests := []struct {
		name   string
		digest string
		body   string
		want   error
	}{
		{"uppercase digest", strings.ToUpper(revision.Digest), revision.CanonicalJSON, ErrLaunchRequestDigest},
		{"wrong digest", strings.Repeat("0", 64), revision.CanonicalJSON, ErrLaunchRequestDigest},
		{"unknown field", revision.Digest, strings.TrimSuffix(revision.CanonicalJSON, "}") + `,"argv":["/bin/sh"]}`,
			ErrLaunchRequestEncoding},
		{"leading whitespace", revision.Digest, " " + revision.CanonicalJSON, ErrLaunchRequestEncoding},
		{"reordered fields", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`{"schema":"noisedeck.supervisor-launch-request.v1","application_session_id":"123e4567-e89b-42d3-a456-426614174000"`,
			`{"application_session_id":"123e4567-e89b-42d3-a456-426614174000","schema":"noisedeck.supervisor-launch-request.v1"`, 1),
			ErrLaunchRequestEncoding},
		{"trailing value", revision.Digest, revision.CanonicalJSON + `{}`, ErrLaunchRequestEncoding},
		{"duplicate field", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`"schema":`, `"schema":"noisedeck.supervisor-launch-request.v1","schema":`, 1), ErrLaunchRequestEncoding},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := VerifyLaunchRequest(test.digest, test.body)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestLaunchRequestRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*LaunchRequestV1)
	}{
		{"unsupported schema", func(r *LaunchRequestV1) { r.Schema += ".next" }},
		{"non-v4 session", func(r *LaunchRequestV1) { r.ApplicationSessionID = "123e4567-e89b-12d3-a456-426614174000" }},
		{"uppercase session", func(r *LaunchRequestV1) { r.ApplicationSessionID = strings.ToUpper(r.ApplicationSessionID) }},
		{"zero generation", func(r *LaunchRequestV1) { r.ApplicationSessionGeneration = 0 }},
		{"negative generation", func(r *LaunchRequestV1) { r.ApplicationSessionGeneration = -1 }},
		{"malformed policy", func(r *LaunchRequestV1) { r.ProjectPolicyRevision = "sha256:wrong" }},
		{"uppercase profile", func(r *LaunchRequestV1) { r.ProviderProfileRevision = strings.Repeat("A", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validLaunchRequest()
			test.mutate(&request)
			if _, err := CanonicalizeLaunchRequest(request); !errors.Is(err, ErrInvalidLaunchRequest) {
				t.Fatalf("error = %v, want ErrInvalidLaunchRequest", err)
			}
		})
	}
}

func TestVerifyLaunchRequestPreservesInvalidRequestError(t *testing.T) {
	request := validLaunchRequest()
	request.ApplicationSessionGeneration = 0
	body := `{"schema":"noisedeck.supervisor-launch-request.v1","application_session_id":"` +
		request.ApplicationSessionID + `","application_session_generation":0,` +
		`"project_policy_revision":"` + request.ProjectPolicyRevision + `",` +
		`"provider_profile_revision":"` + request.ProviderProfileRevision + `"}`
	_, err := VerifyLaunchRequest(strings.Repeat("0", 64), body)
	if !errors.Is(err, ErrInvalidLaunchRequest) || errors.Is(err, ErrLaunchRequestEncoding) {
		t.Fatalf("error = %v, want only ErrInvalidLaunchRequest", err)
	}
}

func TestLaunchRequestDigestCoversEveryField(t *testing.T) {
	base := validLaunchRequest()
	revision, err := CanonicalizeLaunchRequest(base)
	if err != nil {
		t.Fatalf("CanonicalizeLaunchRequest: %v", err)
	}
	mutations := []func(*LaunchRequestV1){
		func(r *LaunchRequestV1) { r.ApplicationSessionID = "223e4567-e89b-42d3-a456-426614174000" },
		func(r *LaunchRequestV1) { r.ApplicationSessionGeneration++ },
		func(r *LaunchRequestV1) { r.ProjectPolicyRevision = strings.Repeat("3", 64) },
		func(r *LaunchRequestV1) { r.ProviderProfileRevision = strings.Repeat("4", 64) },
	}
	for i, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		changed, err := CanonicalizeLaunchRequest(candidate)
		if err != nil {
			t.Fatalf("mutation %d: %v", i, err)
		}
		if changed.Digest == revision.Digest {
			t.Errorf("mutation %d did not change digest", i)
		}
	}
}

func TestLaunchRequestProfileMustMatchBoundPolicy(t *testing.T) {
	profile, err := CurrentProviderProfile(provider.Claude)
	if err != nil {
		t.Fatalf("CurrentProviderProfile: %v", err)
	}
	request := validLaunchRequest()
	request.ProviderProfileRevision = strings.Repeat("3", 64)
	revision, err := CanonicalizeLaunchRequest(request)
	if err != nil {
		t.Fatalf("CanonicalizeLaunchRequest: %v", err)
	}
	_, err = ResolveProviderProfile(
		profile.Digest,
		revision.Request.ProviderProfileRevision, profile.Profile.ExecutableSHA256)
	if !errors.Is(err, ErrProviderProfilePolicy) {
		t.Fatalf("error = %v, want ErrProviderProfilePolicy", err)
	}
}

func validLaunchRequest() LaunchRequestV1 {
	return LaunchRequestV1{
		Schema:                       LaunchRequestSchemaV1,
		ApplicationSessionID:         "123e4567-e89b-42d3-a456-426614174000",
		ApplicationSessionGeneration: 7,
		ProjectPolicyRevision:        strings.Repeat("1", 64),
		ProviderProfileRevision:      strings.Repeat("2", 64),
	}
}
