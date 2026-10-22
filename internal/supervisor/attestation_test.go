package supervisor

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestAttestationGolden(t *testing.T) {
	revision := canonicalFixtureRevision(t, validAttestationExample())
	if revision == nil {
		return
	}
	var wantJSON, wantDigest string
	switch runtime.GOARCH {
	case "amd64":
		wantJSON = `{"schema":"noisedeck.supervisor-attestation.v1","profile_name":"confined-linux-v1","profile_revision":"0000000000000000000000000000000000000000000000000000000000000000","application_session_id":"123e4567-e89b-42d3-a456-426614174000","application_session_generation":7,"supervisor_run_id":"223e4567-e89b-42d3-a456-426614174000","sandbox_generation":"323e4567-e89b-42d3-a456-426614174000","boot_id":"423e4567-e89b-42d3-a456-426614174000","pty_binding_digest":"1111111111111111111111111111111111111111111111111111111111111111","project_policy_revision":"2222222222222222222222222222222222222222222222222222222222222222","provider_profile_revision":"3333333333333333333333333333333333333333333333333333333333333333","supervisor_build_digest":"4444444444444444444444444444444444444444444444444444444444444444","kernel_release":"6.6.1-deck","namespace_result":"effective","seccomp_result":"effective","bubblewrap_archive_digest":"69abc30005d2186baf7737feacd8da35633b93cf5af38838ecff17c5f8e924f6","bubblewrap_executable_digest":"7710b19d89bedbdd3b1dd8ac46643f188f8fdc92917fe3093abd827eb783cf7f","cgroup_v2_hierarchy":"unified","cgroup_v2_controllers":"cpu,io,memory,pids","network_policy_revision":"6666666666666666666666666666666666666666666666666666666666666666","credential_class":"provider-subscription","lifecycle_state":"running","unavailable_reason":"","launch_time":"2026-08-12T07:00:00.000Z","last_reconciliation_time":"2026-08-12T07:01:00.000Z"}`
		wantDigest = "baaba01f523d504de4de630d6f2e0d94b377401955b5697c26f26caaf0cdd826"
	default:
		t.Fatalf("no golden attestation fixture exists for architecture %s", runtime.GOARCH)
	}
	if revision.CanonicalJSON != wantJSON {
		t.Fatalf("canonical JSON = %s, want %s", revision.CanonicalJSON, wantJSON)
	}
	if revision.Digest != wantDigest {
		t.Fatalf("digest = %s, want %s", revision.Digest, wantDigest)
	}
	verified, err := VerifyAttestation(revision.Digest, revision.CanonicalJSON, validAttestationBinding())
	if err != nil {
		t.Fatalf("VerifyAttestation: %v", err)
	}
	if !reflect.DeepEqual(verified, revision) {
		t.Fatalf("verified = %#v, want %#v", verified, revision)
	}
}

func TestAttestationHasOnlyProfileEvidenceFields(t *testing.T) {
	typeOf := reflect.TypeOf(AttestationV1{})
	want := []string{
		"schema", "profile_name", "profile_revision", "application_session_id",
		"application_session_generation", "supervisor_run_id", "sandbox_generation",
		"boot_id", "pty_binding_digest", "project_policy_revision",
		"provider_profile_revision", "supervisor_build_digest", "kernel_release",
		"namespace_result", "seccomp_result", "bubblewrap_archive_digest",
		"bubblewrap_executable_digest", "cgroup_v2_hierarchy",
		"cgroup_v2_controllers", "network_policy_revision", "credential_class",
		"lifecycle_state", "unavailable_reason", "launch_time",
		"last_reconciliation_time",
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

func TestAttestationRejectsNonCanonicalAuthority(t *testing.T) {
	revision := canonicalFixtureRevision(t, validAttestationExample())
	if revision == nil {
		return
	}
	tests := []struct {
		name   string
		digest string
		body   string
		want   error
	}{
		{"uppercase digest", strings.ToUpper(revision.Digest), revision.CanonicalJSON, ErrAttestationDigest},
		{"wrong digest", strings.Repeat("f", 64), revision.CanonicalJSON, ErrAttestationDigest},
		{"malformed JSON", revision.Digest, `{`, ErrAttestationEncoding},
		{"unknown field", revision.Digest, strings.TrimSuffix(revision.CanonicalJSON, "}") + `,"host_path":"/"}`, ErrAttestationEncoding},
		{"leading whitespace", revision.Digest, " " + revision.CanonicalJSON, ErrAttestationEncoding},
		{"trailing value", revision.Digest, revision.CanonicalJSON + `{}`, ErrAttestationEncoding},
		{"duplicate field", revision.Digest, strings.Replace(revision.CanonicalJSON, `"schema":`, `"schema":"duplicate","schema":`, 1), ErrAttestationEncoding},
		{"reordered fields", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`{"schema":"noisedeck.supervisor-attestation.v1","profile_name":"confined-linux-v1"`,
			`{"profile_name":"confined-linux-v1","schema":"noisedeck.supervisor-attestation.v1"`, 1), ErrAttestationEncoding},
		{"unknown enum", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`"namespace_result":"effective"`, `"namespace_result":"configured"`, 1), ErrInvalidAttestation},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := VerifyAttestation(test.digest, test.body, validAttestationBinding())
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err.Error() != test.want.Error() {
				t.Fatalf("error text = %q, want content-free %q", err, test.want)
			}
		})
	}
}

func TestAttestationRejectsInvalidEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AttestationV1)
	}{
		{"unsupported schema", func(a *AttestationV1) { a.Schema += ".next" }},
		{"unknown profile", func(a *AttestationV1) { a.ProfileName = "other" }},
		{"malformed profile revision", func(a *AttestationV1) { a.ProfileRevision = "current" }},
		{"non-v4 session", func(a *AttestationV1) { a.ApplicationSessionID = "123e4567-e89b-12d3-a456-426614174000" }},
		{"zero session generation", func(a *AttestationV1) { a.ApplicationSessionGeneration = 0 }},
		{"non-v4 run", func(a *AttestationV1) { a.SupervisorRunID = "223e4567-e89b-32d3-a456-426614174000" }},
		{"non-v4 sandbox", func(a *AttestationV1) { a.SandboxGeneration = "323e4567-e89b-32d3-a456-426614174000" }},
		{"malformed boot", func(a *AttestationV1) { a.BootID = strings.ToUpper(a.BootID) }},
		{"malformed PTY digest", func(a *AttestationV1) { a.PTYBindingDigest = "pty" }},
		{"non-ASCII kernel release", func(a *AttestationV1) { a.KernelRelease = "6.6.1-λ" }},
		{"unknown namespace result", func(a *AttestationV1) { a.NamespaceResult = "configured" }},
		{"wrong Bubblewrap archive", func(a *AttestationV1) { a.BubblewrapArchiveDigest = strings.Repeat("9", 64) }},
		{"wrong Bubblewrap executable", func(a *AttestationV1) { a.BubblewrapExecutableDigest = strings.Repeat("9", 64) }},
		{"archive digest as executable", func(a *AttestationV1) { a.BubblewrapExecutableDigest = PinnedBubblewrapArchiveSHA256 }},
		{"malformed executable digest", func(a *AttestationV1) { a.BubblewrapExecutableDigest = "bwrap" }},
		{"uppercase executable digest", func(a *AttestationV1) { a.BubblewrapExecutableDigest = strings.ToUpper(a.BubblewrapExecutableDigest) }},
		{"unknown cgroup hierarchy", func(a *AttestationV1) { a.CgroupV2Hierarchy = "legacy" }},
		{"reordered controllers", func(a *AttestationV1) { a.CgroupV2Controllers = "memory,cpu,io,pids" }},
		{"unknown credential class", func(a *AttestationV1) { a.CredentialClass = "host-global" }},
		{"unknown lifecycle", func(a *AttestationV1) { a.LifecycleState = "active" }},
		{"free-text reason", func(a *AttestationV1) { a.UnavailableReason = "probe failed on host" }},
		{"noncanonical launch time", func(a *AttestationV1) { a.LaunchTime = "2026-08-12T07:00:00Z" }},
		{"reconciliation before launch", func(a *AttestationV1) { a.LastReconciliationTime = "2026-08-12T06:59:59.999Z" }},
		{"unavailable without reason", func(a *AttestationV1) { a.NamespaceResult = ProbeUnavailable }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attestation := validAttestationExample()
			test.mutate(&attestation)
			_, err := CanonicalizeAttestation(attestation)
			if !errors.Is(err, ErrInvalidAttestation) {
				t.Fatalf("error = %v, want content-free ErrInvalidAttestation", err)
			}
			if err.Error() != ErrInvalidAttestation.Error() {
				t.Fatalf("error text = %q, want content-free %q", err, ErrInvalidAttestation)
			}
		})
	}
}

func TestAttestationRejectsCrossRunBinding(t *testing.T) {
	revision := canonicalFixtureRevision(t, validAttestationExample())
	if revision == nil {
		return
	}
	mutations := []struct {
		name   string
		mutate func(*AttestationBinding)
	}{
		{"profile revision", func(b *AttestationBinding) { b.ProfileRevision = strings.Repeat("a", 64) }},
		{"session id", func(b *AttestationBinding) { b.ApplicationSessionID = "523e4567-e89b-42d3-a456-426614174000" }},
		{"session generation", func(b *AttestationBinding) { b.ApplicationSessionGeneration++ }},
		{"run id", func(b *AttestationBinding) { b.SupervisorRunID = "623e4567-e89b-42d3-a456-426614174000" }},
		{"sandbox generation", func(b *AttestationBinding) { b.SandboxGeneration = "723e4567-e89b-42d3-a456-426614174000" }},
		{"boot id", func(b *AttestationBinding) { b.BootID = "823e4567-e89b-42d3-a456-426614174000" }},
		{"PTY binding", func(b *AttestationBinding) { b.PTYBindingDigest = strings.Repeat("a", 64) }},
		{"project policy", func(b *AttestationBinding) { b.ProjectPolicyRevision = strings.Repeat("b", 64) }},
		{"provider profile", func(b *AttestationBinding) { b.ProviderProfileRevision = strings.Repeat("c", 64) }},
		{"network policy", func(b *AttestationBinding) { b.NetworkPolicyRevision = strings.Repeat("d", 64) }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			binding := validAttestationBinding()
			test.mutate(&binding)
			_, err := VerifyAttestation(revision.Digest, revision.CanonicalJSON, binding)
			if !errors.Is(err, ErrAttestationBinding) {
				t.Fatalf("error = %v, want content-free ErrAttestationBinding", err)
			}
			if err.Error() != ErrAttestationBinding.Error() {
				t.Fatalf("error text = %q, want content-free %q", err, ErrAttestationBinding)
			}
		})
	}
}

// TestAttestationAcceptsAuthorizedLifecycle confirms the effective-state
// attestation admits the authorized lifecycle phase the run store now
// records, and only the phases the authoritative lifecycle permits.
func TestAttestationAcceptsAuthorizedLifecycle(t *testing.T) {
	if !validLifecycleState(PhaseAuthorized) {
		t.Fatalf("validLifecycleState(PhaseAuthorized) = false, want true")
	}
	attestation := validAttestationExample()
	attestation.LifecycleState = PhaseAuthorized
	revision := canonicalFixtureRevision(t, attestation)
	if revision == nil {
		return
	}
	if _, err := VerifyAttestation(revision.Digest, revision.CanonicalJSON, validAttestationBinding()); err != nil {
		t.Fatalf("VerifyAttestation: %v", err)
	}
}

// canonicalFixtureRevision canonicalizes the valid fixture on a target
// inside the closed executable-pin map. On every other target no valid
// attestation can exist: the helper asserts the fixture fails closed with
// the exact ErrInvalidAttestation sentinel and returns nil, and callers
// stop at nil so an unsupported architecture never constructs one.
func canonicalFixtureRevision(t *testing.T, attestation AttestationV1) *AttestationRevision {
	t.Helper()
	revision, err := CanonicalizeAttestation(attestation)
	if err == nil {
		return revision
	}
	if !errors.Is(err, ErrInvalidAttestation) || err.Error() != ErrInvalidAttestation.Error() {
		t.Fatalf("fixture rejected with %v, want the content-free %v sentinel", err, ErrInvalidAttestation)
	}
	if _, pinErr := CurrentBubblewrapExecutableSHA256(); pinErr == nil {
		t.Fatalf("supported target rejected the valid fixture: %v", err)
	}
	return nil
}

func validAttestationExample() AttestationV1 {
	return AttestationV1{
		Schema:                       AttestationSchemaV1,
		ProfileName:                  ProfileConfinedLinuxV1,
		ProfileRevision:              strings.Repeat("0", 64),
		ApplicationSessionID:         "123e4567-e89b-42d3-a456-426614174000",
		ApplicationSessionGeneration: 7,
		SupervisorRunID:              "223e4567-e89b-42d3-a456-426614174000",
		SandboxGeneration:            "323e4567-e89b-42d3-a456-426614174000",
		BootID:                       "423e4567-e89b-42d3-a456-426614174000",
		PTYBindingDigest:             strings.Repeat("1", 64),
		ProjectPolicyRevision:        strings.Repeat("2", 64),
		ProviderProfileRevision:      strings.Repeat("3", 64),
		SupervisorBuildDigest:        strings.Repeat("4", 64),
		KernelRelease:                "6.6.1-deck",
		NamespaceResult:              ProbeEffective,
		SeccompResult:                ProbeEffective,
		BubblewrapArchiveDigest:      PinnedBubblewrapArchiveSHA256,
		BubblewrapExecutableDigest:   pinnedRuntimeBubblewrapDigest(),
		CgroupV2Hierarchy:            CgroupV2Unified,
		CgroupV2Controllers:          CgroupV2RequiredControllers,
		NetworkPolicyRevision:        strings.Repeat("6", 64),
		CredentialClass:              CredentialProviderSubscription,
		LifecycleState:               PhaseRunning,
		UnavailableReason:            AttestationReasonNone,
		LaunchTime:                   "2026-08-12T07:00:00.000Z",
		LastReconciliationTime:       "2026-08-12T07:01:00.000Z",
	}
}

// pinnedRuntimeBubblewrapDigest carries the compiled executable pin for the
// running target into the fixture; on a target outside the closed map the
// fixture stays invalid and the attestation tests fail closed.
func pinnedRuntimeBubblewrapDigest() string {
	digest, err := CurrentBubblewrapExecutableSHA256()
	if err != nil {
		return strings.Repeat("5", 64)
	}
	return digest
}

func validAttestationBinding() AttestationBinding {
	attestation := validAttestationExample()
	return AttestationBinding{
		ProfileRevision:              attestation.ProfileRevision,
		ApplicationSessionID:         attestation.ApplicationSessionID,
		ApplicationSessionGeneration: attestation.ApplicationSessionGeneration,
		SupervisorRunID:              attestation.SupervisorRunID,
		SandboxGeneration:            attestation.SandboxGeneration,
		BootID:                       attestation.BootID,
		PTYBindingDigest:             attestation.PTYBindingDigest,
		ProjectPolicyRevision:        attestation.ProjectPolicyRevision,
		ProviderProfileRevision:      attestation.ProviderProfileRevision,
		NetworkPolicyRevision:        attestation.NetworkPolicyRevision,
	}
}
