package supervisor

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func validLauncherHello() LauncherHelloV1 {
	return LauncherHelloV1{
		Schema:       LauncherHelloSchemaV1,
		RunID:        "123e4567-e89b-42d3-a456-426614174000",
		InvocationID: "0123456789abcdef0123456789abcdef",
	}
}

func TestLauncherHelloGolden(t *testing.T) {
	revision, err := CanonicalizeLauncherHello(validLauncherHello())
	if err != nil {
		t.Fatalf("CanonicalizeLauncherHello: %v", err)
	}
	const wantJSON = `{"schema":"noisedeck.supervisor-launcher-hello.v1","run_id":"123e4567-e89b-42d3-a456-426614174000","invocation_id":"0123456789abcdef0123456789abcdef"}`
	const wantDigest = "d3bc55a6caf5a987774124e3939880b788ce4575921735ae40c1a8974067a32d"
	if revision.CanonicalJSON != wantJSON {
		t.Fatalf("canonical JSON = %s, want %s", revision.CanonicalJSON, wantJSON)
	}
	if revision.Digest != wantDigest {
		t.Fatalf("digest = %s, want %s", revision.Digest, wantDigest)
	}
	verified, err := VerifyLauncherHello(revision.Digest, revision.CanonicalJSON)
	if err != nil {
		t.Fatalf("VerifyLauncherHello: %v", err)
	}
	if !reflect.DeepEqual(verified, revision) {
		t.Fatalf("verified = %#v, want %#v", verified, revision)
	}
}

func TestLauncherHelloHasOnlyIdentityFields(t *testing.T) {
	typeOf := reflect.TypeOf(LauncherHelloV1{})
	want := []string{"schema", "run_id", "invocation_id"}
	if typeOf.NumField() != len(want) {
		t.Fatalf("field count = %d, want %d", typeOf.NumField(), len(want))
	}
	for i, tag := range want {
		if got := typeOf.Field(i).Tag.Get("json"); got != tag {
			t.Errorf("field %d JSON tag = %q, want %q", i, got, tag)
		}
	}
}

func TestCanonicalizeLauncherHelloRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*LauncherHelloV1)
	}{
		{"unsupported schema", func(h *LauncherHelloV1) { h.Schema += ".next" }},
		{"uppercase run id", func(h *LauncherHelloV1) { h.RunID = strings.ToUpper(h.RunID) }},
		{"truncated run id", func(h *LauncherHelloV1) { h.RunID = h.RunID[:35] }},
		{"non-v4 run id", func(h *LauncherHelloV1) { h.RunID = "1b4e28ba-2fa1-31d2-883f-0016d3cca427" }},
		{"uppercase invocation id", func(h *LauncherHelloV1) { h.InvocationID = strings.ToUpper(h.InvocationID) }},
		{"truncated invocation id", func(h *LauncherHelloV1) { h.InvocationID = h.InvocationID[:31] }},
		{"non-hex invocation id", func(h *LauncherHelloV1) { h.InvocationID = strings.Repeat("z", 32) }},
		{"dash in invocation id", func(h *LauncherHelloV1) { h.InvocationID = "01234567-89abcdef0123456789abcdef" }},
		{"empty invocation id", func(h *LauncherHelloV1) { h.InvocationID = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hello := validLauncherHello()
			test.mutate(&hello)
			if _, err := CanonicalizeLauncherHello(hello); !errors.Is(err, ErrInvalidLauncherHello) {
				t.Fatalf("error = %v, want ErrInvalidLauncherHello", err)
			}
		})
	}
}

func TestVerifyLauncherHelloRejectsNonCanonicalAuthority(t *testing.T) {
	revision, err := CanonicalizeLauncherHello(validLauncherHello())
	if err != nil {
		t.Fatalf("CanonicalizeLauncherHello: %v", err)
	}
	tests := []struct {
		name   string
		digest string
		body   string
		want   error
	}{
		{"uppercase digest", strings.ToUpper(revision.Digest), revision.CanonicalJSON, ErrLauncherHelloDigest},
		{"short digest", revision.Digest[:63], revision.CanonicalJSON, ErrLauncherHelloDigest},
		{"non-hex digest", strings.Repeat("g", 64), revision.CanonicalJSON, ErrLauncherHelloDigest},
		{"wrong digest", strings.Repeat("0", 64), revision.CanonicalJSON, ErrLauncherHelloDigest},
		{"malformed JSON", revision.Digest, `{`, ErrLauncherHelloEncoding},
		{"unknown field", revision.Digest, strings.TrimSuffix(revision.CanonicalJSON, "}") + `,"fd":3}`,
			ErrLauncherHelloEncoding},
		{"leading whitespace", revision.Digest, " " + revision.CanonicalJSON, ErrLauncherHelloEncoding},
		{"trailing value", revision.Digest, revision.CanonicalJSON + `{}`, ErrLauncherHelloEncoding},
		{"duplicate field", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`"schema":`, `"schema":"noisedeck.supervisor-launcher-hello.v1","schema":`, 1), ErrLauncherHelloEncoding},
		{"unsupported schema", revision.Digest, strings.Replace(revision.CanonicalJSON,
			LauncherHelloSchemaV1, LauncherHelloSchemaV1+".next", 1), ErrInvalidLauncherHello},
		{"reordered fields", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`{"schema":"noisedeck.supervisor-launcher-hello.v1","run_id":"123e4567-e89b-42d3-a456-426614174000"`,
			`{"run_id":"123e4567-e89b-42d3-a456-426614174000","schema":"noisedeck.supervisor-launcher-hello.v1"`, 1),
			ErrLauncherHelloEncoding},
		{"non-v4 run id", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`"123e4567-e89b-42d3-a456-426614174000"`, `"1b4e28ba-2fa1-31d2-883f-0016d3cca427"`, 1),
			ErrInvalidLauncherHello},
		{"uppercase invocation id", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`"0123456789abcdef0123456789abcdef"`, `"`+strings.ToUpper("0123456789abcdef0123456789abcdef")+`"`, 1),
			ErrInvalidLauncherHello},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := VerifyLauncherHello(test.digest, test.body)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err.Error() != test.want.Error() {
				t.Fatalf("error text = %q, want stable %q", err, test.want)
			}
		})
	}
}

func TestLauncherHelloDigestCoversEveryField(t *testing.T) {
	base := validLauncherHello()
	revision, err := CanonicalizeLauncherHello(base)
	if err != nil {
		t.Fatalf("CanonicalizeLauncherHello: %v", err)
	}
	mutations := []func(*LauncherHelloV1){
		func(h *LauncherHelloV1) { h.RunID = "223e4567-e89b-42d3-a456-426614174000" },
		func(h *LauncherHelloV1) { h.InvocationID = "ffffffffffffffffffffffffffffffff" },
	}
	for i, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		changed, err := CanonicalizeLauncherHello(candidate)
		if err != nil {
			t.Fatalf("mutation %d: %v", i, err)
		}
		if changed.Digest == revision.Digest {
			t.Errorf("mutation %d did not change digest", i)
		}
	}
}
