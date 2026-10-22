package supervisor

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const (
	goldenLaunchTicketJSON = `{"schema":"noisedeck.supervisor-launch-ticket.v1","run_id":"223e4567-e89b-42d3-a456-426614174000","sandbox_generation":"323e4567-e89b-42d3-a456-426614174000","launch_request_digest":"1111111111111111111111111111111111111111111111111111111111111111","pty_binding_digest":"2222222222222222222222222222222222222222222222222222222222222222","sandbox_plan_digest":"3333333333333333333333333333333333333333333333333333333333333333","unit_name":"noisedeck-confined-890d91149d8d9296e38261b7765631e0910ae990bac1215e441089b696190444.service","unit_property_digest":"532d2e819a7e852012c91f90c5d63930e2dec5e58b4bab246e2148033fbb800a","launcher_invocation_id":"0123456789abcdef0123456789abcdef"}`
	goldenLaunchTicketHash = "0e5c59d77e35e63e645b91adf86c5d2aca58e99fa132a6b09742bf0908132661"
)

func TestLaunchTicketGoldenVector(t *testing.T) {
	ticket := validLaunchTicketForTest(t)
	revision, err := CanonicalizeLaunchTicket(ticket)
	if err != nil {
		t.Fatalf("CanonicalizeLaunchTicket: %v", err)
	}
	if revision.CanonicalJSON != goldenLaunchTicketJSON {
		t.Fatalf("canonical JSON = %q, want %q", revision.CanonicalJSON, goldenLaunchTicketJSON)
	}
	if revision.Digest != goldenLaunchTicketHash {
		t.Fatalf("digest = %q, want %q", revision.Digest, goldenLaunchTicketHash)
	}
	verified, err := VerifyLaunchTicket(revision.Digest, revision.CanonicalJSON, launchTicketBinding(ticket))
	if err != nil {
		t.Fatalf("VerifyLaunchTicket: %v", err)
	}
	if !reflect.DeepEqual(verified, revision) {
		t.Fatalf("verified = %#v, want %#v", verified, revision)
	}
}

func TestLaunchTicketHasOnlyClosedBindingFields(t *testing.T) {
	typeOf := reflect.TypeOf(LaunchTicketV1{})
	want := []string{
		"schema",
		"run_id",
		"sandbox_generation",
		"launch_request_digest",
		"pty_binding_digest",
		"sandbox_plan_digest",
		"unit_name",
		"unit_property_digest",
		"launcher_invocation_id",
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

func TestCanonicalizeLaunchTicketRejectsInvalidBindings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*LaunchTicketV1)
	}{
		{"wrong schema", func(ticket *LaunchTicketV1) { ticket.Schema += ".next" }},
		{"malformed run", func(ticket *LaunchTicketV1) { ticket.RunID = "TOPSECRET" }},
		{"non-v4 run", func(ticket *LaunchTicketV1) { ticket.RunID = "223e4567-e89b-32d3-a456-426614174000" }},
		{"malformed sandbox", func(ticket *LaunchTicketV1) { ticket.SandboxGeneration = "TOPSECRET" }},
		{"non-v4 sandbox", func(ticket *LaunchTicketV1) { ticket.SandboxGeneration = "323e4567-e89b-32d3-a456-426614174000" }},
		{"malformed request digest", func(ticket *LaunchTicketV1) { ticket.LaunchRequestDigest = "TOPSECRET" }},
		{"uppercase PTY digest", func(ticket *LaunchTicketV1) { ticket.PTYBindingDigest = strings.Repeat("A", 64) }},
		{"short plan digest", func(ticket *LaunchTicketV1) { ticket.SandboxPlanDigest = ticket.SandboxPlanDigest[:63] }},
		{"caller unit name", func(ticket *LaunchTicketV1) { ticket.UnitName = "caller-selected.service" }},
		{"mismatched unit digest", func(ticket *LaunchTicketV1) { ticket.UnitPropertyDigest = strings.Repeat("4", 64) }},
		{"uppercase invocation", func(ticket *LaunchTicketV1) {
			ticket.LauncherInvocationID = strings.ToUpper(ticket.LauncherInvocationID)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ticket := validLaunchTicketForTest(t)
			test.mutate(&ticket)
			_, err := CanonicalizeLaunchTicket(ticket)
			assertLaunchTicketError(t, err, ErrInvalidLaunchTicket)
		})
	}
}

func TestVerifyLaunchTicketRejectsHostileAuthorityBytes(t *testing.T) {
	ticket := validLaunchTicketForTest(t)
	binding := launchTicketBinding(ticket)
	tests := []struct {
		name   string
		digest string
		body   string
		want   error
	}{
		{"uppercase digest", strings.ToUpper(goldenLaunchTicketHash), goldenLaunchTicketJSON, ErrLaunchTicketDigest},
		{"short digest", goldenLaunchTicketHash[:63], goldenLaunchTicketJSON, ErrLaunchTicketDigest},
		{"non-hex digest", strings.Repeat("g", 64), goldenLaunchTicketJSON, ErrLaunchTicketDigest},
		{"wrong digest", strings.Repeat("0", 64), goldenLaunchTicketJSON, ErrLaunchTicketDigest},
		{"malformed JSON", goldenLaunchTicketHash, `{`, ErrLaunchTicketEncoding},
		{"unknown field", goldenLaunchTicketHash, strings.TrimSuffix(goldenLaunchTicketJSON, "}") + `,"argv":"TOPSECRET"}`, ErrLaunchTicketEncoding},
		{"duplicate field", goldenLaunchTicketHash, strings.Replace(goldenLaunchTicketJSON,
			`"schema":`, `"schema":"noisedeck.supervisor-launch-ticket.v1","schema":`, 1), ErrLaunchTicketEncoding},
		{"reordered fields", goldenLaunchTicketHash, strings.Replace(goldenLaunchTicketJSON,
			`{"schema":"noisedeck.supervisor-launch-ticket.v1","run_id":"223e4567-e89b-42d3-a456-426614174000"`,
			`{"run_id":"223e4567-e89b-42d3-a456-426614174000","schema":"noisedeck.supervisor-launch-ticket.v1"`, 1), ErrLaunchTicketEncoding},
		{"trailing value", goldenLaunchTicketHash, goldenLaunchTicketJSON + `{}`, ErrLaunchTicketEncoding},
		{"reserialized whitespace", goldenLaunchTicketHash, "\n" + goldenLaunchTicketJSON, ErrLaunchTicketEncoding},
		{"wrong schema", goldenLaunchTicketHash, strings.Replace(goldenLaunchTicketJSON,
			LaunchTicketSchemaV1, LaunchTicketSchemaV1+".next", 1), ErrInvalidLaunchTicket},
		{"malformed bound digest", goldenLaunchTicketHash, strings.Replace(goldenLaunchTicketJSON,
			strings.Repeat("1", 64), "TOPSECRET", 1), ErrInvalidLaunchTicket},
		{"mismatched unit digest", goldenLaunchTicketHash, strings.Replace(goldenLaunchTicketJSON,
			goldenTransientUnitHash, strings.Repeat("4", 64), 1), ErrInvalidLaunchTicket},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := VerifyLaunchTicket(test.digest, test.body, binding)
			assertLaunchTicketError(t, err, test.want)
		})
	}
}

func TestVerifyLaunchTicketRejectsEveryBindingMutation(t *testing.T) {
	ticket := validLaunchTicketForTest(t)
	revision, err := CanonicalizeLaunchTicket(ticket)
	if err != nil {
		t.Fatalf("CanonicalizeLaunchTicket: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*LaunchTicketBinding)
	}{
		{"run", func(binding *LaunchTicketBinding) { binding.RunID = "423e4567-e89b-42d3-a456-426614174000" }},
		{"sandbox", func(binding *LaunchTicketBinding) { binding.SandboxGeneration = "523e4567-e89b-42d3-a456-426614174000" }},
		{"request", func(binding *LaunchTicketBinding) { binding.LaunchRequestDigest = strings.Repeat("4", 64) }},
		{"PTY", func(binding *LaunchTicketBinding) { binding.PTYBindingDigest = strings.Repeat("5", 64) }},
		{"plan", func(binding *LaunchTicketBinding) { binding.SandboxPlanDigest = strings.Repeat("6", 64) }},
		{"unit name", func(binding *LaunchTicketBinding) { binding.UnitName = "caller-selected.service" }},
		{"unit properties", func(binding *LaunchTicketBinding) { binding.UnitPropertyDigest = strings.Repeat("7", 64) }},
		{"invocation", func(binding *LaunchTicketBinding) { binding.LauncherInvocationID = strings.Repeat("f", 32) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding := launchTicketBinding(ticket)
			test.mutate(&binding)
			_, err := VerifyLaunchTicket(revision.Digest, revision.CanonicalJSON, binding)
			assertLaunchTicketError(t, err, ErrLaunchTicketBinding)
		})
	}
}

func TestLaunchTicketDigestCoversEveryIndependentBinding(t *testing.T) {
	base := validLaunchTicketForTest(t)
	revision, err := CanonicalizeLaunchTicket(base)
	if err != nil {
		t.Fatalf("CanonicalizeLaunchTicket: %v", err)
	}
	mutations := []struct {
		name   string
		mutate func(*LaunchTicketV1)
	}{
		{"run", func(ticket *LaunchTicketV1) {
			ticket.RunID = "423e4567-e89b-42d3-a456-426614174000"
			setLaunchTicketUnit(t, ticket)
		}},
		{"sandbox", func(ticket *LaunchTicketV1) {
			ticket.SandboxGeneration = "523e4567-e89b-42d3-a456-426614174000"
			setLaunchTicketUnit(t, ticket)
		}},
		{"request", func(ticket *LaunchTicketV1) { ticket.LaunchRequestDigest = strings.Repeat("4", 64) }},
		{"PTY", func(ticket *LaunchTicketV1) { ticket.PTYBindingDigest = strings.Repeat("5", 64) }},
		{"plan", func(ticket *LaunchTicketV1) { ticket.SandboxPlanDigest = strings.Repeat("6", 64) }},
		{"invocation", func(ticket *LaunchTicketV1) { ticket.LauncherInvocationID = strings.Repeat("f", 32) }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			test.mutate(&candidate)
			changed, err := CanonicalizeLaunchTicket(candidate)
			if err != nil {
				t.Fatalf("CanonicalizeLaunchTicket: %v", err)
			}
			if changed.Digest == revision.Digest {
				t.Fatal("mutation did not change ticket digest")
			}
		})
	}
}

func validLaunchTicketForTest(t *testing.T) LaunchTicketV1 {
	t.Helper()
	ticket := LaunchTicketV1{
		Schema:               LaunchTicketSchemaV1,
		RunID:                "223e4567-e89b-42d3-a456-426614174000",
		SandboxGeneration:    "323e4567-e89b-42d3-a456-426614174000",
		LaunchRequestDigest:  strings.Repeat("1", 64),
		PTYBindingDigest:     strings.Repeat("2", 64),
		SandboxPlanDigest:    strings.Repeat("3", 64),
		LauncherInvocationID: "0123456789abcdef0123456789abcdef",
	}
	setLaunchTicketUnit(t, &ticket)
	return ticket
}

func setLaunchTicketUnit(t *testing.T, ticket *LaunchTicketV1) {
	t.Helper()
	unit, err := DeriveTransientUnitProperties(TransientUnitPropertiesBinding{
		RunID:             ticket.RunID,
		SandboxGeneration: ticket.SandboxGeneration,
	})
	if err != nil {
		t.Fatalf("DeriveTransientUnitProperties: %v", err)
	}
	ticket.UnitName = unit.UnitName
	ticket.UnitPropertyDigest = unit.Digest
}

func launchTicketBinding(ticket LaunchTicketV1) LaunchTicketBinding {
	return LaunchTicketBinding{
		RunID:                ticket.RunID,
		SandboxGeneration:    ticket.SandboxGeneration,
		LaunchRequestDigest:  ticket.LaunchRequestDigest,
		PTYBindingDigest:     ticket.PTYBindingDigest,
		SandboxPlanDigest:    ticket.SandboxPlanDigest,
		UnitName:             ticket.UnitName,
		UnitPropertyDigest:   ticket.UnitPropertyDigest,
		LauncherInvocationID: ticket.LauncherInvocationID,
	}
}

func assertLaunchTicketError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if err.Error() != want.Error() {
		t.Fatalf("error text = %q, want content-free %q", err, want)
	}
}
