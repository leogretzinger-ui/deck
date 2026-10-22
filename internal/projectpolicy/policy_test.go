package projectpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func validPolicy() ProjectPolicyV1 {
	return ProjectPolicyV1{
		Schema:                   ProjectPolicySchemaV1,
		SecurityDomainID:         "00112233445566778899aabbccddeeff",
		BackingDigest:            strings.Repeat("1", 64),
		ConfinementProfileDigest: strings.Repeat("2", 64),
		ProviderProfileDigest:    strings.Repeat("3", 64),
		MemoryScopeDigest:        strings.Repeat("4", 64),
		MappedRemote: MappedRemoteV1{
			Repository:      "github.com/noisefloor-oss/deck",
			PermissionClass: RemotePermissionContribute,
		},
		Destinations: []DestinationV1{
			{Host: "api.github.com", Port: 443},
			{Host: "2001:db8::1", Port: 443},
		},
		Inputs: []InputV1{
			{SHA256: strings.Repeat("a", 64), Bytes: 4096},
		},
		Resources: ResourceLimitsV1{
			Processes:     512,
			MemoryBytes:   8 << 30,
			OutputBytes:   1 << 30,
			ProjectBytes:  20 << 30,
			ScratchBytes:  4 << 30,
			ProjectInodes: 1 << 20,
			ScratchInodes: 1 << 18,
			ExchangeBytes: 1 << 30,
			CPU:           CPULimitV1{QuotaPerSecUsec: 800000, PeriodUsec: 100000},
			IO: IOLimitV1{
				ReadBytesPerSec:  100 << 20,
				WriteBytesPerSec: 50 << 20,
				ReadOpsPerSec:    10000,
				WriteOpsPerSec:   5000,
			},
		},
	}
}

const goldenPolicyJSON = `{"schema":"noisedeck.project-policy.v1","security_domain_id":"00112233445566778899aabbccddeeff","backing_digest":"1111111111111111111111111111111111111111111111111111111111111111","confinement_profile_digest":"2222222222222222222222222222222222222222222222222222222222222222","provider_profile_digest":"3333333333333333333333333333333333333333333333333333333333333333","memory_scope_digest":"4444444444444444444444444444444444444444444444444444444444444444","mapped_remote":{"repository":"github.com/noisefloor-oss/deck","permission_class":"contribute"},"destinations":[{"host":"api.github.com","port":443},{"host":"2001:db8::1","port":443}],"inputs":[{"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","bytes":4096}],"resources":{"processes":512,"memory_bytes":8589934592,"output_bytes":1073741824,"project_bytes":21474836480,"scratch_bytes":4294967296,"project_inodes":1048576,"scratch_inodes":262144,"exchange_bytes":1073741824,"cpu":{"quota_per_sec_usec":800000,"period_usec":100000},"io":{"read_bytes_per_sec":104857600,"write_bytes_per_sec":52428800,"read_ops_per_sec":10000,"write_ops_per_sec":5000}}}`
const goldenPolicyDigest = "23f9d78875ccfd22d491ef3ecda03f7e4d0d27c0c91568c88db81e8725b4cfa9"

func TestCanonicalizeGolden(t *testing.T) {
	revision, err := CanonicalizeProjectPolicy(validPolicy())
	if err != nil {
		t.Fatalf("CanonicalizeProjectPolicy: %v", err)
	}
	if revision.CanonicalJSON != goldenPolicyJSON {
		t.Fatalf("canonical JSON =\n%s\nwant\n%s", revision.CanonicalJSON, goldenPolicyJSON)
	}
	if revision.Digest != goldenPolicyDigest {
		t.Fatalf("digest = %s, want %s", revision.Digest, goldenPolicyDigest)
	}
	sum := sha256.Sum256([]byte(revision.CanonicalJSON))
	if revision.Digest != hex.EncodeToString(sum[:]) {
		t.Fatal("digest is not the sha256 of the canonical bytes")
	}
}

func TestVerifyGoldenRoundTrip(t *testing.T) {
	revision, err := VerifyProjectPolicy(goldenPolicyDigest, goldenPolicyJSON)
	if err != nil {
		t.Fatalf("VerifyProjectPolicy: %v", err)
	}
	want := validPolicy()
	if !reflect.DeepEqual(revision.Policy, want) {
		t.Fatalf("decoded policy = %+v, want %+v", revision.Policy, want)
	}
	again, err := VerifyProjectPolicy(revision.Digest, revision.CanonicalJSON)
	if err != nil {
		t.Fatalf("re-verify: %v", err)
	}
	if again.Digest != revision.Digest || again.CanonicalJSON != revision.CanonicalJSON {
		t.Fatal("verify is not idempotent")
	}
}

func TestVerifyRejectsNonCanonicalJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
		err  error
	}{
		{
			"reordered fields",
			strings.Replace(goldenPolicyJSON,
				`{"schema":"noisedeck.project-policy.v1","security_domain_id":"00112233445566778899aabbccddeeff"`,
				`{"security_domain_id":"00112233445566778899aabbccddeeff","schema":"noisedeck.project-policy.v1"`, 1),
			ErrPolicyEncoding,
		},
		{
			"duplicate field",
			strings.Replace(goldenPolicyJSON, `"schema":`,
				`"schema":"noisedeck.project-policy.v1","schema":`, 1),
			ErrPolicyEncoding,
		},
		{"trailing value", goldenPolicyJSON + `{}`, ErrPolicyEncoding},
		{"leading whitespace", " " + goldenPolicyJSON, ErrPolicyEncoding},
		{
			"null destinations",
			strings.Replace(goldenPolicyJSON, `"destinations":[`, `"destinations":null,"keep":[`, 1),
			ErrPolicyEncoding,
		},
		{
			"uppercase host bytes",
			strings.Replace(goldenPolicyJSON, "api.github.com", "API.GitHub.com", 1),
			ErrPolicyEncoding,
		},
		{
			"unknown field",
			strings.Replace(goldenPolicyJSON, `"inputs":[`, `"extra":1,"inputs":[`, 1),
			ErrPolicyEncoding,
		},
		{
			"port out of range",
			strings.Replace(goldenPolicyJSON, `"port":443`, `"port":70000`, 1),
			ErrPolicyEncoding,
		},
		{
			"port as string",
			strings.Replace(goldenPolicyJSON, `"port":443`, `"port":"443"`, 1),
			ErrPolicyEncoding,
		},
		{
			"unlimited processes",
			strings.Replace(goldenPolicyJSON, `"processes":512`,
				`"processes":18446744073709551615`, 1),
			ErrInvalidPolicy,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := VerifyProjectPolicy(goldenPolicyDigest, test.body)
			if !errors.Is(err, test.err) {
				t.Fatalf("VerifyProjectPolicy error = %v, want %v", err, test.err)
			}
		})
	}
}

func TestVerifyRejectsBadDigest(t *testing.T) {
	if _, err := VerifyProjectPolicy("not-a-digest", goldenPolicyJSON); !errors.Is(err, ErrPolicyDigest) {
		t.Fatalf("malformed digest error = %v, want %v", err, ErrPolicyDigest)
	}
	tampered := strings.Replace(goldenPolicyJSON, `"processes":512`, `"processes":513`, 1)
	sum := sha256.Sum256([]byte(tampered))
	if _, err := VerifyProjectPolicy(hex.EncodeToString(sum[:]), tampered); err != nil {
		t.Fatalf("self-consistent tampered policy should re-digest: %v", err)
	}
	if _, err := VerifyProjectPolicy(hex.EncodeToString(sum[:]), goldenPolicyJSON); !errors.Is(err, ErrPolicyDigest) {
		t.Fatalf("digest mismatch error = %v, want %v", err, ErrPolicyDigest)
	}
}

func TestResourceValuesMustBeExplicit(t *testing.T) {
	mutations := map[string]func(*ResourceLimitsV1){
		"processes":       func(r *ResourceLimitsV1) { r.Processes = 0 },
		"memory bytes":    func(r *ResourceLimitsV1) { r.MemoryBytes = math.MaxUint64 },
		"output bytes":    func(r *ResourceLimitsV1) { r.OutputBytes = 0 },
		"project bytes":   func(r *ResourceLimitsV1) { r.ProjectBytes = math.MaxUint64 },
		"scratch bytes":   func(r *ResourceLimitsV1) { r.ScratchBytes = 0 },
		"project inodes":  func(r *ResourceLimitsV1) { r.ProjectInodes = math.MaxUint64 },
		"scratch inodes":  func(r *ResourceLimitsV1) { r.ScratchInodes = 0 },
		"exchange bytes":  func(r *ResourceLimitsV1) { r.ExchangeBytes = math.MaxUint64 },
		"cpu quota":       func(r *ResourceLimitsV1) { r.CPU.QuotaPerSecUsec = 0 },
		"cpu period":      func(r *ResourceLimitsV1) { r.CPU.PeriodUsec = 0 },
		"io read bytes":   func(r *ResourceLimitsV1) { r.IO.ReadBytesPerSec = 0 },
		"io write bytes":  func(r *ResourceLimitsV1) { r.IO.WriteBytesPerSec = math.MaxUint64 },
		"io read ops":     func(r *ResourceLimitsV1) { r.IO.ReadOpsPerSec = 0 },
		"io write ops":    func(r *ResourceLimitsV1) { r.IO.WriteOpsPerSec = math.MaxUint64 },
		"cpu period low":  func(r *ResourceLimitsV1) { r.CPU.PeriodUsec = 999 },
		"cpu period high": func(r *ResourceLimitsV1) { r.CPU.PeriodUsec = 1000001 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			policy := validPolicy()
			mutate(&policy.Resources)
			if _, err := CanonicalizeProjectPolicy(policy); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidPolicy)
			}
		})
	}
	pairs := []struct {
		name       string
		quota      uint64
		period     uint64
		acceptable bool
	}{
		{"floor below kernel minimum", 999999, 1000, false},
		{"exact kernel minimum", 1000000, 1000, true},
		{"fraction floors to minimum", 1000999, 1000, true},
		{"minimum rate at longest period", 1000, 1000000, true},
	}
	for _, pair := range pairs {
		t.Run(pair.name, func(t *testing.T) {
			policy := validPolicy()
			policy.Resources.CPU = CPULimitV1{QuotaPerSecUsec: pair.quota, PeriodUsec: pair.period}
			_, err := CanonicalizeProjectPolicy(policy)
			if pair.acceptable && err != nil {
				t.Fatalf("valid CPU pair rejected: %v", err)
			}
			if !pair.acceptable && !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("invalid CPU pair error = %v, want %v", err, ErrInvalidPolicy)
			}
		})
	}
}

func TestDestinationHostCanonicalization(t *testing.T) {
	rejected := []string{
		"",
		"api.github.com.",
		"0100.0.0.1",
		"127.1",
		"0x7f.0.0.1",
		"0177.0.0.1",
		"1.2.3",
		"12345",
		"::0001",
		"1:02:3:4:5:6:7:8", // leading zeros are not RFC 5952 canonical
		"fe80::1%eth0",
		"api.git_hub.com",
		"-api.github.com",
		"api-.github.com",
		strings.Repeat("a", 64) + ".com",
		strings.Repeat("a", 254) + ".com",
		"exämple.com",
	}
	for _, host := range rejected {
		policy := validPolicy()
		policy.Destinations = []DestinationV1{{Host: host, Port: 443}}
		if _, err := CanonicalizeProjectPolicy(policy); !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("host %q accepted", host)
		}
	}
	validDigitLeading := validPolicy()
	validDigitLeading.Destinations = []DestinationV1{{Host: "0example.com", Port: 443}}
	if _, err := CanonicalizeProjectPolicy(validDigitLeading); err != nil {
		t.Fatalf("valid digit-leading DNS label rejected: %v", err)
	}
	hexDNSLabel := validPolicy()
	hexDNSLabel.Destinations = []DestinationV1{{Host: "0xdead.example.com", Port: 443}}
	if _, err := CanonicalizeProjectPolicy(hexDNSLabel); err != nil {
		t.Fatalf("valid hexadecimal-looking DNS label rejected: %v", err)
	}

	foldedOnly := validPolicy()
	foldedOnly.Destinations = []DestinationV1{{Host: "API.GitHub.com", Port: 443}}
	upper, err := CanonicalizeProjectPolicy(foldedOnly)
	if err != nil {
		t.Fatalf("uppercase DNS host rejected: %v", err)
	}
	lowerOnly := validPolicy()
	lowerOnly.Destinations = []DestinationV1{{Host: "api.github.com", Port: 443}}
	lower, err := CanonicalizeProjectPolicy(lowerOnly)
	if err != nil {
		t.Fatalf("lowercase baseline: %v", err)
	}
	if upper.Digest != lower.Digest {
		t.Fatal("case-folded destination did not converge on one identity")
	}

	mixedCaseIP := validPolicy()
	mixedCaseIP.Destinations = []DestinationV1{{Host: "2001:DB8::1", Port: 443}}
	folded, err := CanonicalizeProjectPolicy(mixedCaseIP)
	if err != nil {
		t.Fatalf("uppercase IPv6 hex rejected: %v", err)
	}
	if folded.Policy.Destinations[0].Host != "2001:db8::1" {
		t.Fatalf("folded IPv6 = %q", folded.Policy.Destinations[0].Host)
	}

	zeroPort := validPolicy()
	zeroPort.Destinations = []DestinationV1{{Host: "api.github.com", Port: 0}}
	if _, err := CanonicalizeProjectPolicy(zeroPort); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal("port 0 accepted")
	}

	foldedDuplicate := validPolicy()
	foldedDuplicate.Destinations = []DestinationV1{
		{Host: "EXAMPLE.com", Port: 443},
		{Host: "example.com", Port: 443},
	}
	if _, err := CanonicalizeProjectPolicy(foldedDuplicate); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal("destination duplicate across case accepted")
	}

	atLimit := validPolicy()
	atLimit.Destinations = make([]DestinationV1, MaxDestinations)
	for i := range atLimit.Destinations {
		atLimit.Destinations[i] = DestinationV1{Host: "api.github.com", Port: uint16(1000 + i)}
	}
	if _, err := CanonicalizeProjectPolicy(atLimit); err != nil {
		t.Fatalf("policy at the destination bound rejected: %v", err)
	}
	overLimit := validPolicy()
	overLimit.Destinations = append(
		append([]DestinationV1(nil), atLimit.Destinations...),
		DestinationV1{Host: "api.github.com", Port: 2000},
	)
	if _, err := CanonicalizeProjectPolicy(overLimit); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal("policy above the destination bound accepted")
	}
}

func TestInputsAreImmutableSnapshots(t *testing.T) {
	rejected := map[string]func(*InputV1){
		"zero bytes":     func(in *InputV1) { in.Bytes = 0 },
		"unlimited size": func(in *InputV1) { in.Bytes = math.MaxUint64 },
		"short digest":   func(in *InputV1) { in.SHA256 = strings.Repeat("a", 63) },
		"upper digest":   func(in *InputV1) { in.SHA256 = strings.ToUpper(in.SHA256) },
	}
	for name, mutate := range rejected {
		t.Run(name, func(t *testing.T) {
			policy := validPolicy()
			mutate(&policy.Inputs[0])
			if _, err := CanonicalizeProjectPolicy(policy); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidPolicy)
			}
		})
	}

	duplicate := validPolicy()
	duplicate.Inputs = []InputV1{
		{SHA256: strings.Repeat("a", 64), Bytes: 1},
		{SHA256: strings.Repeat("a", 64), Bytes: 2},
	}
	if _, err := CanonicalizeProjectPolicy(duplicate); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal("duplicate input digest accepted")
	}

	atLimit := validPolicy()
	atLimit.Inputs = make([]InputV1, MaxInputs)
	for i := range atLimit.Inputs {
		atLimit.Inputs[i] = InputV1{SHA256: fmt.Sprintf("%064x", i), Bytes: 1}
	}
	if _, err := CanonicalizeProjectPolicy(atLimit); err != nil {
		t.Fatalf("policy at the input bound rejected: %v", err)
	}
	overLimit := validPolicy()
	overLimit.Inputs = append(
		append([]InputV1(nil), atLimit.Inputs...),
		InputV1{SHA256: strings.Repeat("b", 64), Bytes: 1},
	)
	if _, err := CanonicalizeProjectPolicy(overLimit); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal("policy above the input bound accepted")
	}
}

func TestMappedRemoteRejectsAdministration(t *testing.T) {
	for _, class := range []RemotePermissionClass{"admin", "write", "PermissionAdmin", ""} {
		policy := validPolicy()
		policy.MappedRemote.PermissionClass = class
		if _, err := CanonicalizeProjectPolicy(policy); !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("permission class %q accepted", class)
		}
	}
	for _, class := range []RemotePermissionClass{RemotePermissionRead, RemotePermissionContribute} {
		policy := validPolicy()
		policy.MappedRemote.PermissionClass = class
		if _, err := CanonicalizeProjectPolicy(policy); err != nil {
			t.Fatalf("permission class %q rejected: %v", class, err)
		}
	}

	rejected := []string{
		"",
		"noisefloor-oss/deck",
		"github.com/noisefloor-oss/deck/",
		"github.com//deck",
		"github.com/noisefloor-oss/../deck",
		"github.com/noisefloor-oss/.",
		"github.com/noisefloor-oss/déck",
		strings.Repeat("g", 245) + ".com/owner/repo",
	}
	for _, repository := range rejected {
		policy := validPolicy()
		policy.MappedRemote.Repository = repository
		if _, err := CanonicalizeProjectPolicy(policy); !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("repository %q accepted", repository)
		}
	}

	upper := validPolicy()
	upper.MappedRemote.Repository = "GitHub.com/Noisefloor-OSS/Deck"
	upperRevision, err := CanonicalizeProjectPolicy(upper)
	if err != nil {
		t.Fatalf("uppercase repository rejected: %v", err)
	}
	if upperRevision.Policy.MappedRemote.Repository != "github.com/Noisefloor-OSS/Deck" {
		t.Fatalf("folded repository = %q", upperRevision.Policy.MappedRemote.Repository)
	}
	samePath := validPolicy()
	samePath.MappedRemote.Repository = "github.com/Noisefloor-OSS/Deck"
	lowerRevision, err := CanonicalizeProjectPolicy(samePath)
	if err != nil {
		t.Fatalf("lowercase baseline: %v", err)
	}
	if upperRevision.Digest != lowerRevision.Digest {
		t.Fatal("repository host case-fold did not converge on one identity")
	}
	differentPath := validPolicy()
	differentPath.MappedRemote.Repository = "github.com/noisefloor-oss/deck"
	differentRevision, err := CanonicalizeProjectPolicy(differentPath)
	if err != nil {
		t.Fatalf("different path baseline: %v", err)
	}
	if upperRevision.Digest == differentRevision.Digest {
		t.Fatal("case-sensitive repository paths collapsed to one identity")
	}
}

func TestIdentityFieldsAreClosed(t *testing.T) {
	rejected := map[string]func(*ProjectPolicyV1){
		"schema":          func(p *ProjectPolicyV1) { p.Schema = "noisedeck.project-policy.v2" },
		"empty schema":    func(p *ProjectPolicyV1) { p.Schema = "" },
		"upper domain id": func(p *ProjectPolicyV1) { p.SecurityDomainID = "00112233445566778899AABBCCDDEEFF" },
		"short domain id": func(p *ProjectPolicyV1) { p.SecurityDomainID = strings.Repeat("a", 31) },
		"long domain id":  func(p *ProjectPolicyV1) { p.SecurityDomainID = strings.Repeat("a", 33) },
		"non-hex domain":  func(p *ProjectPolicyV1) { p.SecurityDomainID = strings.Repeat("z", 32) },
		"backing digest":  func(p *ProjectPolicyV1) { p.BackingDigest = strings.Repeat("1", 63) },
		"profile digest":  func(p *ProjectPolicyV1) { p.ConfinementProfileDigest = strings.ToUpper(strings.Repeat("b", 64)) },
		"provider digest": func(p *ProjectPolicyV1) { p.ProviderProfileDigest = "sha256:wrong" },
		"memory digest":   func(p *ProjectPolicyV1) { p.MemoryScopeDigest = strings.Repeat("A", 64) },
	}
	for name, mutate := range rejected {
		t.Run(name, func(t *testing.T) {
			policy := validPolicy()
			mutate(&policy)
			if _, err := CanonicalizeProjectPolicy(policy); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidPolicy)
			}
		})
	}
}

func TestEmptyCollectionsAreCanonical(t *testing.T) {
	policy := validPolicy()
	policy.Destinations = nil
	policy.Inputs = nil
	revision, err := CanonicalizeProjectPolicy(policy)
	if err != nil {
		t.Fatalf("CanonicalizeProjectPolicy: %v", err)
	}
	if !strings.Contains(revision.CanonicalJSON, `"destinations":[]`) ||
		!strings.Contains(revision.CanonicalJSON, `"inputs":[]`) {
		t.Fatalf("empty collections are not canonical arrays: %s", revision.CanonicalJSON)
	}
	verified, err := VerifyProjectPolicy(revision.Digest, revision.CanonicalJSON)
	if err != nil {
		t.Fatalf("VerifyProjectPolicy: %v", err)
	}
	if verified.Policy.Destinations == nil || verified.Policy.Inputs == nil {
		t.Fatal("canonical form of an empty collection is nil")
	}
}

func TestCanonicalizeDoesNotMutateInput(t *testing.T) {
	policy := validPolicy()
	policy.MappedRemote.Repository = "GitHub.com/Noisefloor-OSS/Deck"
	policy.Destinations = []DestinationV1{{Host: "API.GitHub.com", Port: 443}}
	clone := ProjectPolicyV1{
		Schema:                   policy.Schema,
		SecurityDomainID:         policy.SecurityDomainID,
		BackingDigest:            policy.BackingDigest,
		ConfinementProfileDigest: policy.ConfinementProfileDigest,
		ProviderProfileDigest:    policy.ProviderProfileDigest,
		MemoryScopeDigest:        policy.MemoryScopeDigest,
		MappedRemote:             policy.MappedRemote,
		Resources:                policy.Resources,
	}
	clone.Destinations = append([]DestinationV1(nil), policy.Destinations...)
	clone.Inputs = append([]InputV1(nil), policy.Inputs...)

	if _, err := CanonicalizeProjectPolicy(policy); err != nil {
		t.Fatalf("CanonicalizeProjectPolicy: %v", err)
	}
	if !reflect.DeepEqual(policy, clone) {
		t.Fatalf("canonicalization mutated its input: %+v", policy)
	}
}
