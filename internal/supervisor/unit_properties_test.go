package supervisor

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const (
	goldenTransientUnitName = "noisedeck-confined-890d91149d8d9296e38261b7765631e0910ae990bac1215e441089b696190444.service"
	goldenTransientUnitJSON = `{"schema":"noisedeck.transient-unit-properties.v2","profile":"confined-linux-v1","run_id":"223e4567-e89b-42d3-a456-426614174000","sandbox_generation":"323e4567-e89b-42d3-a456-426614174000","unit_name":"noisedeck-confined-890d91149d8d9296e38261b7765631e0910ae990bac1215e441089b696190444.service","properties":[{"name":"Type","signature":"s","value":"exec"},{"name":"ExitType","signature":"s","value":"cgroup"},{"name":"RemainAfterExit","signature":"b","value":true},{"name":"CollectMode","signature":"s","value":"inactive"},{"name":"Restart","signature":"s","value":"no"},{"name":"KillMode","signature":"s","value":"control-group"},{"name":"SendSIGKILL","signature":"b","value":true},{"name":"TimeoutStopUSec","signature":"t","value":30000000},{"name":"Slice","signature":"s","value":"noisedeck-confined.slice"},{"name":"StandardInput","signature":"s","value":"null"},{"name":"StandardOutput","signature":"s","value":"null"},{"name":"StandardError","signature":"s","value":"null"},{"name":"ExecStart","signature":"a(sasb)","value":[{"path":"/usr/local/libexec/noise/noise-deck","argv":["/usr/local/libexec/noise/noise-deck","confined-launcher","--run-id","223e4567-e89b-42d3-a456-426614174000"],"ignore_failure":false}]}]}`
	goldenTransientUnitHash = "532d2e819a7e852012c91f90c5d63930e2dec5e58b4bab246e2148033fbb800a"
)

func TestDeriveTransientUnitPropertiesGoldenVector(t *testing.T) {
	revision, err := DeriveTransientUnitProperties(validTransientUnitBindingForTest())
	if err != nil {
		t.Fatalf("DeriveTransientUnitProperties: %v", err)
	}
	if revision.UnitName != goldenTransientUnitName {
		t.Fatalf("unit name = %q, want %q", revision.UnitName, goldenTransientUnitName)
	}
	if revision.CanonicalJSON != goldenTransientUnitJSON {
		t.Fatalf("canonical JSON = %q, want %q", revision.CanonicalJSON, goldenTransientUnitJSON)
	}
	if revision.Digest != goldenTransientUnitHash {
		t.Fatalf("digest = %q, want %q", revision.Digest, goldenTransientUnitHash)
	}
	if len(revision.Properties.Properties) != 13 {
		t.Fatalf("property count = %d, want 13", len(revision.Properties.Properties))
	}
}

func TestTransientUnitPropertyVectorIsExactlyOrdered(t *testing.T) {
	revision, err := DeriveTransientUnitProperties(validTransientUnitBindingForTest())
	if err != nil {
		t.Fatalf("DeriveTransientUnitProperties: %v", err)
	}
	want := []struct {
		name      string
		signature string
	}{
		{"Type", "s"},
		{"ExitType", "s"},
		{"RemainAfterExit", "b"},
		{"CollectMode", "s"},
		{"Restart", "s"},
		{"KillMode", "s"},
		{"SendSIGKILL", "b"},
		{"TimeoutStopUSec", "t"},
		{"Slice", "s"},
		{"StandardInput", "s"},
		{"StandardOutput", "s"},
		{"StandardError", "s"},
		{"ExecStart", "a(sasb)"},
	}
	props := revision.Properties.Properties
	if len(props) != len(want) {
		t.Fatalf("property count = %d, want %d", len(props), len(want))
	}
	for i, w := range want {
		if props[i].Name != w.name || props[i].Signature != w.signature {
			t.Fatalf("property %d = %q/%q, want %q/%q", i, props[i].Name, props[i].Signature, w.name, w.signature)
		}
	}
}

func TestTransientUnitPropertyTypedSetterRepresentation(t *testing.T) {
	revision, err := DeriveTransientUnitProperties(validTransientUnitBindingForTest())
	if err != nil {
		t.Fatalf("DeriveTransientUnitProperties: %v", err)
	}
	props := revision.Properties.Properties
	byName := make(map[string]TransientUnitProperty, len(props))
	for _, p := range props {
		byName[p.Name] = p
	}
	if got := byName["Type"]; got.Signature != "s" || got.String != "exec" {
		t.Fatalf("Type = %+v, want s/exec", got)
	}
	if got := byName["RemainAfterExit"]; got.Signature != "b" || !got.Boolean {
		t.Fatalf("RemainAfterExit = %+v, want b/true", got)
	}
	if got := byName["SendSIGKILL"]; got.Signature != "b" || !got.Boolean {
		t.Fatalf("SendSIGKILL = %+v, want b/true", got)
	}
	if got := byName["TimeoutStopUSec"]; got.Signature != "t" || got.Uint != 30000000 {
		t.Fatalf("TimeoutStopUSec = %+v, want t/30000000", got)
	}
	if got := byName["Slice"]; got.Signature != "s" || got.String != "noisedeck-confined.slice" {
		t.Fatalf("Slice = %+v, want s/noisedeck-confined.slice", got)
	}
	for _, stream := range []string{"StandardInput", "StandardOutput", "StandardError"} {
		if got := byName[stream]; got.Signature != "s" || got.String != "null" {
			t.Fatalf("%s = %+v, want s/null", stream, got)
		}
	}
	exec := byName["ExecStart"]
	if exec.Signature != "a(sasb)" || len(exec.ExecStart) != 1 {
		t.Fatalf("ExecStart = %+v, want a(sasb) one command", exec)
	}
	cmd := exec.ExecStart[0]
	wantArgv := []string{
		"/usr/local/libexec/noise/noise-deck",
		"confined-launcher",
		"--run-id",
		"223e4567-e89b-42d3-a456-426614174000",
	}
	if cmd.Path != wantArgv[0] || cmd.IgnoreFailure {
		t.Fatalf("ExecStart command = %+v, want path %q ignore-failure false", cmd, wantArgv[0])
	}
	if len(cmd.Argv) != len(wantArgv) {
		t.Fatalf("argv length = %d, want %d", len(cmd.Argv), len(wantArgv))
	}
	for i := range wantArgv {
		if cmd.Argv[i] != wantArgv[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, cmd.Argv[i], wantArgv[i])
		}
	}

	// the canonical bytes never stringify a boolean, uint64, or ExecStart
	if !strings.Contains(revision.CanonicalJSON, `"RemainAfterExit","signature":"b","value":true`) {
		t.Fatalf("canonical JSON did not emit RemainAfterExit as a boolean: %s", revision.CanonicalJSON)
	}
	if !strings.Contains(revision.CanonicalJSON, `"TimeoutStopUSec","signature":"t","value":30000000`) {
		t.Fatalf("canonical JSON did not emit TimeoutStopUSec as a uint64: %s", revision.CanonicalJSON)
	}
	if !strings.Contains(revision.CanonicalJSON, `"--run-id","223e4567-e89b-42d3-a456-426614174000"`+`],"ignore_failure":false}`) {
		t.Fatalf("canonical JSON did not emit the exact ExecStart command: %s", revision.CanonicalJSON)
	}
}

func TestTransientUnitDigestBindsRunIDThroughExecStart(t *testing.T) {
	base := validTransientUnitBindingForTest()
	baseRevision, err := DeriveTransientUnitProperties(base)
	if err != nil {
		t.Fatalf("derive base: %v", err)
	}
	other, err := DeriveTransientUnitProperties(TransientUnitPropertiesBinding{
		RunID:             "423e4567-e89b-42d3-a456-426614174000",
		SandboxGeneration: base.SandboxGeneration,
	})
	if err != nil {
		t.Fatalf("derive other: %v", err)
	}
	if !strings.Contains(other.CanonicalJSON, `"--run-id","423e4567-e89b-42d3-a456-426614174000"`) {
		t.Fatalf("other ExecStart did not bind its run id: %s", other.CanonicalJSON)
	}
	if other.Digest == baseRevision.Digest {
		t.Fatal("changing the run id did not change the digest")
	}
}

func TestTransientUnitIdentityBindsRunAndSandbox(t *testing.T) {
	base := validTransientUnitBindingForTest()
	baseRevision, err := DeriveTransientUnitProperties(base)
	if err != nil {
		t.Fatalf("derive base: %v", err)
	}
	variants := []struct {
		name    string
		binding TransientUnitPropertiesBinding
	}{
		{
			name: "different run",
			binding: TransientUnitPropertiesBinding{
				RunID:             "423e4567-e89b-42d3-a456-426614174000",
				SandboxGeneration: base.SandboxGeneration,
			},
		},
		{
			name: "different sandbox generation",
			binding: TransientUnitPropertiesBinding{
				RunID:             base.RunID,
				SandboxGeneration: "523e4567-e89b-42d3-a456-426614174000",
			},
		},
	}
	for _, tc := range variants {
		t.Run(tc.name, func(t *testing.T) {
			revision, err := DeriveTransientUnitProperties(tc.binding)
			if err != nil {
				t.Fatalf("derive: %v", err)
			}
			if revision.UnitName == baseRevision.UnitName {
				t.Fatal("unit name did not change")
			}
			if revision.Digest == baseRevision.Digest {
				t.Fatal("property digest did not change")
			}
		})
	}
}

func TestDeriveTransientUnitPropertiesRejectsMalformedIdentity(t *testing.T) {
	base := validTransientUnitBindingForTest()
	tests := []struct {
		name    string
		binding TransientUnitPropertiesBinding
	}{
		{"malformed run", TransientUnitPropertiesBinding{RunID: "not-a-run", SandboxGeneration: base.SandboxGeneration}},
		{"non-v4 run", TransientUnitPropertiesBinding{RunID: "223e4567-e89b-32d3-a456-426614174000", SandboxGeneration: base.SandboxGeneration}},
		{"malformed sandbox", TransientUnitPropertiesBinding{RunID: base.RunID, SandboxGeneration: "not-a-sandbox"}},
		{"non-v4 sandbox", TransientUnitPropertiesBinding{RunID: base.RunID, SandboxGeneration: "323e4567-e89b-32d3-a456-426614174000"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DeriveTransientUnitProperties(tc.binding); !errors.Is(err, ErrInvalidTransientUnitProperties) {
				t.Fatalf("error = %v, want ErrInvalidTransientUnitProperties", err)
			}
		})
	}
}

func TestVerifyTransientUnitProperties(t *testing.T) {
	binding := validTransientUnitBindingForTest()
	revision, err := VerifyTransientUnitProperties(
		goldenTransientUnitName,
		goldenTransientUnitHash,
		goldenTransientUnitJSON,
		binding,
	)
	if err != nil {
		t.Fatalf("VerifyTransientUnitProperties: %v", err)
	}
	if revision.UnitName != goldenTransientUnitName || revision.Digest != goldenTransientUnitHash {
		t.Fatalf("verified revision = %#v", revision)
	}
}

func TestVerifyTransientUnitPropertiesRejectsSelectedOrMalformedName(t *testing.T) {
	binding := validTransientUnitBindingForTest()
	tests := []struct {
		name     string
		unitName string
		want     error
	}{
		{"caller-selected valid name", "noisedeck-confined-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.service", ErrTransientUnitPropertiesBinding},
		{"host path", "/etc/systemd/system/noisedeck.service", ErrInvalidTransientUnitProperties},
		{"uppercase", "Noisedeck.service", ErrInvalidTransientUnitProperties},
		{"missing suffix", "noisedeck-confined", ErrInvalidTransientUnitProperties},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyTransientUnitProperties(tc.unitName, goldenTransientUnitHash, goldenTransientUnitJSON, binding)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestVerifyTransientUnitPropertiesRejectsNoncanonicalPropertySets(t *testing.T) {
	missing := strings.Replace(goldenTransientUnitJSON,
		`,{"name":"StandardError","signature":"s","value":"null"}`, "", 1)
	duplicate := strings.Replace(goldenTransientUnitJSON,
		`"ignore_failure":false}]}]}`,
		`"ignore_failure":false}]},{"name":"Type","signature":"s","value":"exec"}]}`, 1)
	unknownProperty := strings.Replace(goldenTransientUnitJSON,
		`{"name":"Type","signature":"s","value":"exec"}`,
		`{"name":"Description","signature":"s","value":"TOPSECRET"}`, 1)
	trailingProperty := strings.Replace(goldenTransientUnitJSON,
		`"ignore_failure":false}]}]}`,
		`"ignore_failure":false}]},{"name":"Description","signature":"s","value":"TOPSECRET"}]}`, 1)
	reordered := strings.Replace(goldenTransientUnitJSON,
		`{"name":"Type","signature":"s","value":"exec"},{"name":"ExitType","signature":"s","value":"cgroup"}`,
		`{"name":"ExitType","signature":"s","value":"cgroup"},{"name":"Type","signature":"s","value":"exec"}`, 1)
	wrongValue := strings.Replace(goldenTransientUnitJSON,
		`{"name":"Type","signature":"s","value":"exec"}`,
		`{"name":"Type","signature":"s","value":"simple"}`, 1)
	wrongName := strings.Replace(goldenTransientUnitJSON,
		`{"name":"Type","signature":"s","value":"exec"}`,
		`{"name":"ExecType","signature":"s","value":"exec"}`, 1)
	unknown := strings.Replace(goldenTransientUnitJSON,
		`"schema":`, `"unknown":"TOPSECRET","schema":`, 1)
	duplicateField := strings.Replace(goldenTransientUnitJSON,
		`"schema":"noisedeck.transient-unit-properties.v2",`,
		`"schema":"noisedeck.transient-unit-properties.v2","schema":"noisedeck.transient-unit-properties.v2",`, 1)
	tests := []struct {
		name string
		body string
		want error
	}{
		{"missing property", missing, ErrInvalidTransientUnitProperties},
		{"duplicate property", duplicate, ErrInvalidTransientUnitProperties},
		{"unknown property", unknownProperty, ErrInvalidTransientUnitProperties},
		{"trailing property", trailingProperty, ErrInvalidTransientUnitProperties},
		{"reordered properties", reordered, ErrInvalidTransientUnitProperties},
		{"wrong value", wrongValue, ErrInvalidTransientUnitProperties},
		{"wrong name", wrongName, ErrInvalidTransientUnitProperties},
		{"unknown field", unknown, ErrTransientUnitPropertiesEncoding},
		{"duplicate field", duplicateField, ErrTransientUnitPropertiesEncoding},
		{"trailing value", goldenTransientUnitJSON + `{}`, ErrTransientUnitPropertiesEncoding},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyTransientUnitProperties(
				goldenTransientUnitName,
				goldenTransientUnitHash,
				tc.body,
				validTransientUnitBindingForTest(),
			)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "TOPSECRET") {
				t.Fatalf("error disclosed input: %v", err)
			}
		})
	}
}

func TestVerifyTransientUnitPropertiesRejectsHostileTypedValues(t *testing.T) {
	tests := []struct {
		name string
		body string
		want error
	}{
		{"unknown signature", strings.Replace(goldenTransientUnitJSON,
			`{"name":"Type","signature":"s","value":"exec"}`,
			`{"name":"Type","signature":"x","value":"exec"}`, 1), ErrTransientUnitPropertiesEncoding},
		{"bool value as string", strings.Replace(goldenTransientUnitJSON,
			`{"name":"RemainAfterExit","signature":"b","value":true}`,
			`{"name":"RemainAfterExit","signature":"b","value":"TOPSECRET"}`, 1), ErrTransientUnitPropertiesEncoding},
		{"bool value as number", strings.Replace(goldenTransientUnitJSON,
			`{"name":"RemainAfterExit","signature":"b","value":true}`,
			`{"name":"RemainAfterExit","signature":"b","value":1}`, 1), ErrTransientUnitPropertiesEncoding},
		{"uint64 value as string", strings.Replace(goldenTransientUnitJSON,
			`{"name":"TimeoutStopUSec","signature":"t","value":30000000}`,
			`{"name":"TimeoutStopUSec","signature":"t","value":"30000000"}`, 1), ErrTransientUnitPropertiesEncoding},
		{"uint64 value as float", strings.Replace(goldenTransientUnitJSON,
			`{"name":"TimeoutStopUSec","signature":"t","value":30000000}`,
			`{"name":"TimeoutStopUSec","signature":"t","value":30000000.0}`, 1), ErrTransientUnitPropertiesEncoding},
		{"string value as bool", strings.Replace(goldenTransientUnitJSON,
			`{"name":"Type","signature":"s","value":"exec"}`,
			`{"name":"Type","signature":"s","value":true}`, 1), ErrTransientUnitPropertiesEncoding},
		{"exec value as string", strings.Replace(goldenTransientUnitJSON,
			`"value":[{"path":"/usr/local/libexec/noise/noise-deck"`,
			`"value":"TOPSECRET"`, 1), ErrTransientUnitPropertiesEncoding},
		{"empty exec union", strings.Replace(goldenTransientUnitJSON,
			`"value":[{"path":"/usr/local/libexec/noise/noise-deck","argv":["/usr/local/libexec/noise/noise-deck","confined-launcher","--run-id","223e4567-e89b-42d3-a456-426614174000"],"ignore_failure":false}]}]}`,
			`"value":[]}]}`, 1), ErrInvalidTransientUnitProperties},
		{"unknown command field", strings.Replace(goldenTransientUnitJSON,
			`"ignore_failure":false}]}]}`,
			`"ignore_failure":false,"argv0":"TOPSECRET"}]}]}`, 1), ErrTransientUnitPropertiesEncoding},
		{"property trailing content", strings.Replace(goldenTransientUnitJSON,
			`{"name":"Type","signature":"s","value":"exec"}`,
			`{"name":"Type","signature":"s","value":"exec","extra":"TOPSECRET"}`, 1), ErrTransientUnitPropertiesEncoding},
		{"missing value arm", strings.Replace(goldenTransientUnitJSON,
			`{"name":"Type","signature":"s","value":"exec"}`,
			`{"name":"Type","signature":"s"}`, 1), ErrTransientUnitPropertiesEncoding},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyTransientUnitProperties(
				goldenTransientUnitName,
				goldenTransientUnitHash,
				tc.body,
				validTransientUnitBindingForTest(),
			)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "TOPSECRET") {
				t.Fatalf("error disclosed input: %v", err)
			}
		})
	}
}

func TestVerifyTransientUnitPropertiesRejectsExecStartMutations(t *testing.T) {
	mutations := []struct {
		name string
		body string
	}{
		{"wrong executable path", strings.Replace(goldenTransientUnitJSON,
			`"path":"/usr/local/libexec/noise/noise-deck"`,
			`"path":"/usr/bin/TOPSECRET"`, 1)},
		{"extra argv element", strings.Replace(goldenTransientUnitJSON,
			`"--run-id","223e4567-e89b-42d3-a456-426614174000"`,
			`"--run-id","223e4567-e89b-42d3-a456-426614174000","TOPSECRET"`, 1)},
		{"changed run id argv", strings.Replace(goldenTransientUnitJSON,
			`"--run-id","223e4567-e89b-42d3-a456-426614174000"`,
			`"--run-id","423e4567-e89b-42d3-a456-426614174000"`, 1)},
		{"ignore failure true", strings.Replace(goldenTransientUnitJSON,
			`"ignore_failure":false}`, `"ignore_failure":true}`, 1)},
		{"two commands", strings.Replace(goldenTransientUnitJSON,
			`"ignore_failure":false}]}]}`,
			`"ignore_failure":false},{"path":"/usr/local/libexec/noise/noise-deck","argv":["/usr/local/libexec/noise/noise-deck","confined-launcher","--run-id","223e4567-e89b-42d3-a456-426614174000"],"ignore_failure":false}]}]}`, 1)},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyTransientUnitProperties(
				goldenTransientUnitName,
				goldenTransientUnitHash,
				tc.body,
				validTransientUnitBindingForTest(),
			)
			if !errors.Is(err, ErrInvalidTransientUnitProperties) {
				t.Fatalf("error = %v, want ErrInvalidTransientUnitProperties", err)
			}
			if strings.Contains(err.Error(), "TOPSECRET") {
				t.Fatalf("error disclosed input: %v", err)
			}
		})
	}
}

func TestVerifyTransientUnitPropertiesRejectsDigestMismatch(t *testing.T) {
	binding := validTransientUnitBindingForTest()
	if _, err := VerifyTransientUnitProperties(goldenTransientUnitName, "malformed", goldenTransientUnitJSON, binding); !errors.Is(err, ErrTransientUnitPropertiesDigest) {
		t.Fatalf("malformed digest error = %v", err)
	}
	if _, err := VerifyTransientUnitProperties(goldenTransientUnitName, strings.Repeat("a", 64), goldenTransientUnitJSON, binding); !errors.Is(err, ErrTransientUnitPropertiesDigest) {
		t.Fatalf("mismatched digest error = %v", err)
	}
}

func TestVerifyTransientUnitPropertiesRejectsCrossRunBinding(t *testing.T) {
	base := validTransientUnitBindingForTest()
	tests := []TransientUnitPropertiesBinding{
		{RunID: "423e4567-e89b-42d3-a456-426614174000", SandboxGeneration: base.SandboxGeneration},
		{RunID: base.RunID, SandboxGeneration: "523e4567-e89b-42d3-a456-426614174000"},
	}
	for _, binding := range tests {
		_, err := VerifyTransientUnitProperties(goldenTransientUnitName, goldenTransientUnitHash, goldenTransientUnitJSON, binding)
		if !errors.Is(err, ErrTransientUnitPropertiesBinding) {
			t.Fatalf("error = %v, want ErrTransientUnitPropertiesBinding", err)
		}
	}
}

func TestTransientUnitPropertyErrorsAreContentFree(t *testing.T) {
	for _, err := range []error{
		ErrInvalidTransientUnitProperties,
		ErrTransientUnitPropertiesEncoding,
		ErrTransientUnitPropertiesDigest,
		ErrTransientUnitPropertiesBinding,
	} {
		if strings.Contains(err.Error(), "TOPSECRET") || strings.ContainsAny(err.Error(), "{}[]\"") {
			t.Fatalf("error is not content-free: %q", err)
		}
	}
}

func TestTransientUnitPropertyUnmarshalClearsInactiveArms(t *testing.T) {
	// Decoding into a reused value must clear every arm the new signature does
	// not own, so a stale active arm cannot survive a re-decode.
	p := TransientUnitProperty{
		Name:      "Prior",
		Signature: transientSigString,
		String:    "stale",
		Boolean:   true,
		Uint:      9,
		ExecStart: []TransientUnitExecCommand{{Path: "/stale", Argv: []string{"/stale"}}},
	}
	if err := json.Unmarshal([]byte(`{"name":"Next","signature":"b","value":true}`), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Name != "Next" || p.Signature != transientSigBool || !p.Boolean {
		t.Fatalf("active arm not decoded: %+v", p)
	}
	if p.String != "" || p.Uint != 0 || len(p.ExecStart) != 0 {
		t.Fatalf("inactive arms not cleared: %+v", p)
	}
}

// transientUnitPropertyActiveArmOnly returns a copy of p carrying only the
// single arm its signature designates as active; every inactive arm is the
// zero value. The result is arms-clean and its active arm equals p's, so a
// comparator built this way can fail only on the inactive-arm guard it is
// meant to exercise, never on an unrelated active-value difference.
func transientUnitPropertyActiveArmOnly(p TransientUnitProperty) TransientUnitProperty {
	clean := TransientUnitProperty{Name: p.Name, Signature: p.Signature}
	switch p.Signature {
	case transientSigString:
		clean.String = p.String
	case transientSigBool:
		clean.Boolean = p.Boolean
	case transientSigUint64:
		clean.Uint = p.Uint
	case transientSigExecStart:
		clean.ExecStart = p.ExecStart
	}
	return clean
}

func TestTransientUnitPropertyRejectsAmbiguousArms(t *testing.T) {
	// A programmatically constructed value with a populated inactive arm must
	// fail closed at the arms check, canonicalization, and equality, never
	// silently drop the stale arm.
	cases := []struct {
		name string
		prop TransientUnitProperty
	}{
		{"string arm with stale bool", TransientUnitProperty{Name: "Type", Signature: transientSigString, String: "exec", Boolean: true}},
		{"bool arm with stale string", TransientUnitProperty{Name: "RemainAfterExit", Signature: transientSigBool, Boolean: true, String: "exec"}},
		{"uint arm with stale exec", TransientUnitProperty{Name: "TimeoutStopUSec", Signature: transientSigUint64, Uint: 1, ExecStart: []TransientUnitExecCommand{{Path: "/x"}}}},
		{"exec arm with stale uint", TransientUnitProperty{Name: "ExecStart", Signature: transientSigExecStart, ExecStart: []TransientUnitExecCommand{{Path: "/x"}}, Uint: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if transientPropertyArmsClean(tc.prop) {
				t.Fatalf("arms check accepted a nonzero inactive arm")
			}
			if _, err := tc.prop.MarshalJSON(); !errors.Is(err, ErrInvalidTransientUnitProperties) {
				t.Fatalf("MarshalJSON error = %v, want ErrInvalidTransientUnitProperties", err)
			}
			clean := transientUnitPropertyActiveArmOnly(tc.prop)
			if transientPropertyEqual(tc.prop, clean) {
				t.Fatalf("equality accepted a nonzero inactive arm")
			}
		})
	}
}

func TestTransientUnitPropertyAcceptsZeroActiveArm(t *testing.T) {
	// The active arm may hold its native zero value; only inactive arms must be
	// empty. This is the lower bound of the exactly-one-arm invariant.
	for _, p := range []TransientUnitProperty{
		{Name: "Type", Signature: transientSigString},
		{Name: "Flag", Signature: transientSigBool},
		{Name: "TimeoutStopUSec", Signature: transientSigUint64},
		{Name: "ExecStart", Signature: transientSigExecStart},
	} {
		if !transientPropertyArmsClean(p) {
			t.Fatalf("arms check rejected a zero active arm for %q", p.Signature)
		}
		if _, err := p.MarshalJSON(); err != nil {
			t.Fatalf("MarshalJSON rejected a zero active arm for %q: %v", p.Signature, err)
		}
	}
}

func validTransientUnitBindingForTest() TransientUnitPropertiesBinding {
	return TransientUnitPropertiesBinding{
		RunID:             "223e4567-e89b-42d3-a456-426614174000",
		SandboxGeneration: "323e4567-e89b-42d3-a456-426614174000",
	}
}
