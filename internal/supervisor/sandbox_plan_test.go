package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/noisefloor-oss/deck/internal/projectpolicy"
)

const (
	goldenSandboxPlanPolicyDigest  = "0f0e823fd2b533e9f21ad3fec336933089888bb56b05259e011b4d8376cf5ef7"
	goldenSandboxPlanInputDigest   = "b16ab2b6b9d441692ca7ea18485ce965e543f48c5e3f2fd65fca9af456ec99cc"
	goldenSandboxPlanEmptyImports  = "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"
	goldenSandboxPlanWithInputHash = "8734ddb327a436435d5a38952b5ae966fa22e33dfb0e728cdb9fe9fbbc5c8161"
	goldenSandboxPlanJSON          = `{"schema":"noisedeck.sandbox-plan.v1","profile":"confined-linux-v1","run_id":"223e4567-e89b-42d3-a456-426614174000","sandbox_generation":"323e4567-e89b-42d3-a456-426614174000","launch_request_digest":"1111111111111111111111111111111111111111111111111111111111111111","project_policy_revision":"0f0e823fd2b533e9f21ad3fec336933089888bb56b05259e011b4d8376cf5ef7","confinement_profile_digest":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","provider_profile_digest":"9999999999999999999999999999999999999999999999999999999999999999","resource_policy_digest":"741f49da1c0f24fcfec8152ddd6fcd9a6ae3d64433b75257771ca3eb3d6384c9","network_policy_digest":"4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945","layout":[{"role":"runtime","destination":"/usr","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"role":"project","destination":"/project","sha256":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},{"role":"git_common","destination":"/project.git","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},{"role":"session_state","destination":"/home/agent","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},{"role":"scratch","destination":"/tmp","sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},{"role":"imports","destination":"/imports","sha256":"4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"}]}`
	goldenSandboxPlanHash          = "49902810d8eaf0a7607af7d52385c8089ebd13a5a0c4e2bfc60f35bc1be0c2b1"
)

func TestSandboxPlanGoldenVector(t *testing.T) {
	facts := sandboxPlanFactsForTest(t)
	revision, err := DeriveSandboxPlan(facts)
	if err != nil {
		t.Fatalf("DeriveSandboxPlan: %v", err)
	}
	if revision.CanonicalJSON != goldenSandboxPlanJSON {
		t.Fatalf("canonical JSON = %q, want %q", revision.CanonicalJSON, goldenSandboxPlanJSON)
	}
	if revision.Digest != goldenSandboxPlanHash {
		t.Fatalf("digest = %q, want %q", revision.Digest, goldenSandboxPlanHash)
	}
	verified, err := VerifySandboxPlan(revision.Digest, revision.CanonicalJSON, facts)
	if err != nil {
		t.Fatalf("VerifySandboxPlan: %v", err)
	}
	if !reflect.DeepEqual(verified, revision) {
		t.Fatalf("verified = %#v, want %#v", verified, revision)
	}
}

func TestSandboxPlanHasOnlyClosedFields(t *testing.T) {
	wantPlan := []string{
		"schema",
		"profile",
		"run_id",
		"sandbox_generation",
		"launch_request_digest",
		"project_policy_revision",
		"confinement_profile_digest",
		"provider_profile_digest",
		"resource_policy_digest",
		"network_policy_digest",
		"layout",
	}
	typeOf := reflect.TypeOf(SandboxPlanV1{})
	if typeOf.NumField() != len(wantPlan) {
		t.Fatalf("field count = %d, want %d", typeOf.NumField(), len(wantPlan))
	}
	for i, tag := range wantPlan {
		if got := typeOf.Field(i).Tag.Get("json"); got != tag {
			t.Errorf("field %d JSON tag = %q, want %q", i, got, tag)
		}
	}

	wantBinding := []string{"role", "destination", "sha256"}
	bindingType := reflect.TypeOf(SandboxPlanBinding{})
	if bindingType.NumField() != len(wantBinding) {
		t.Fatalf("binding field count = %d, want %d", bindingType.NumField(), len(wantBinding))
	}
	for i, tag := range wantBinding {
		if got := bindingType.Field(i).Tag.Get("json"); got != tag {
			t.Errorf("binding field %d JSON tag = %q, want %q", i, got, tag)
		}
	}
}

func TestSandboxPlanLayoutIsClosedAndDisjoint(t *testing.T) {
	if len(sandboxPlanRoleOrder) != len(sandboxPlanDestinations) {
		t.Fatalf("destination table covers %d roles, want %d",
			len(sandboxPlanDestinations), len(sandboxPlanRoleOrder))
	}
	for i, role := range sandboxPlanRoleOrder {
		destination, ok := sandboxPlanDestinations[role]
		if !ok {
			t.Fatalf("role %q has no fixed destination", role)
		}
		if !strings.HasPrefix(destination, "/") || strings.HasSuffix(destination, "/") {
			t.Fatalf("role %q destination %q is not a clean absolute path", role, destination)
		}
		if slices.Index(sandboxPlanRoleOrder[i+1:], role) >= 0 {
			t.Fatalf("role %q appears more than once in the canonical order", role)
		}
	}
	// The fixed destinations must be pairwise disjoint at path-component
	// boundaries: no duplicate, and no destination that is an ancestor of
	// another, so one role can never overlap or shadow another.
	for _, a := range sandboxPlanRoleOrder {
		for _, b := range sandboxPlanRoleOrder {
			if a == b {
				continue
			}
			first, second := sandboxPlanDestinations[a], sandboxPlanDestinations[b]
			if first == second || strings.HasPrefix(second+"/", first+"/") {
				t.Fatalf("destination %q for %q overlaps or shadows %q for %q",
					first, a, second, b)
			}
		}
	}
}

func TestDeriveSandboxPlanRejectsInvalidFacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SandboxPlanFacts)
		want   error
	}{
		{"malformed run", func(f *SandboxPlanFacts) { f.RunID = "not-a-uuid" }, ErrInvalidSandboxPlan},
		{"non-v4 run", func(f *SandboxPlanFacts) { f.RunID = "223e4567-e89b-32d3-a456-426614174000" }, ErrInvalidSandboxPlan},
		{"malformed sandbox", func(f *SandboxPlanFacts) { f.SandboxGeneration = "not-a-uuid" }, ErrInvalidSandboxPlan},
		{"malformed request digest", func(f *SandboxPlanFacts) {
			f.LaunchRequestDigest = strings.Repeat("A", 64)
		}, ErrInvalidSandboxPlan},
		{"short runtime digest", func(f *SandboxPlanFacts) { f.RuntimeRootSHA256 = f.RuntimeRootSHA256[:63] }, ErrInvalidSandboxPlan},
		{"non-hex git common digest", func(f *SandboxPlanFacts) { f.GitCommonSHA256 = strings.Repeat("g", 64) }, ErrInvalidSandboxPlan},
		{"uppercase session digest", func(f *SandboxPlanFacts) {
			f.SessionStateSHA256 = strings.ToUpper(f.SessionStateSHA256)
		}, ErrInvalidSandboxPlan},
		{"short scratch digest", func(f *SandboxPlanFacts) { f.ScratchSHA256 = f.ScratchSHA256[:63] }, ErrInvalidSandboxPlan},
		{"absent policy", func(f *SandboxPlanFacts) { f.Policy = nil }, ErrInvalidSandboxPlan},
		{"tampered policy digest", func(f *SandboxPlanFacts) {
			f.Policy = &projectpolicy.ProjectPolicyRevision{
				Digest:        strings.Repeat("0", 64),
				CanonicalJSON: f.Policy.CanonicalJSON,
				Policy:        f.Policy.Policy,
			}
		}, ErrSandboxPlanPolicy},
		{"mutated policy value", func(f *SandboxPlanFacts) {
			policy := f.Policy.Policy
			policy.Resources.Processes = 1
			f.Policy = &projectpolicy.ProjectPolicyRevision{
				Digest:        f.Policy.Digest,
				CanonicalJSON: f.Policy.CanonicalJSON,
				Policy:        policy,
			}
		}, ErrSandboxPlanPolicy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := sandboxPlanFactsForTest(t)
			test.mutate(&facts)
			_, err := DeriveSandboxPlan(facts)
			assertSandboxPlanError(t, err, test.want)
		})
	}
}

func TestVerifySandboxPlanRejectsHostileAuthorityBytes(t *testing.T) {
	facts := sandboxPlanFactsForTest(t)
	tests := []struct {
		name   string
		digest string
		body   string
		want   error
	}{
		{"uppercase digest", strings.ToUpper(goldenSandboxPlanHash), goldenSandboxPlanJSON, ErrSandboxPlanDigest},
		{"short digest", goldenSandboxPlanHash[:63], goldenSandboxPlanJSON, ErrSandboxPlanDigest},
		{"non-hex digest", strings.Repeat("g", 64), goldenSandboxPlanJSON, ErrSandboxPlanDigest},
		{"wrong digest", strings.Repeat("0", 64), goldenSandboxPlanJSON, ErrSandboxPlanDigest},
		{"oversized body", goldenSandboxPlanHash, strings.Repeat(" ", MaxSandboxPlanCanonicalBytes+1), ErrSandboxPlanEncoding},
		{"malformed JSON", goldenSandboxPlanHash, `{`, ErrSandboxPlanEncoding},
		{"unknown field", goldenSandboxPlanHash, strings.TrimSuffix(goldenSandboxPlanJSON, "}") +
			`,"source":"/workspace"}`, ErrSandboxPlanEncoding},
		{"duplicate field", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			`"schema":`, `"schema":"noisedeck.sandbox-plan.v1","schema":`, 1), ErrSandboxPlanEncoding},
		{"reordered fields", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			`{"schema":"noisedeck.sandbox-plan.v1","profile":"confined-linux-v1"`,
			`{"profile":"confined-linux-v1","schema":"noisedeck.sandbox-plan.v1"`, 1), ErrSandboxPlanEncoding},
		{"trailing value", goldenSandboxPlanHash, goldenSandboxPlanJSON + `{}`, ErrSandboxPlanEncoding},
		{"reserialized whitespace", goldenSandboxPlanHash, "\n" + goldenSandboxPlanJSON, ErrSandboxPlanEncoding},
		{"reordered inner keys", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			`{"role":"runtime","destination":"/usr","sha256":`,
			`{"destination":"/usr","role":"runtime","sha256":`, 1), ErrSandboxPlanEncoding},
		{"unknown inner field", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			`"destination":"/usr","sha256":`, `"destination":"/usr","source":"/","sha256":`, 1), ErrSandboxPlanEncoding},
		{"wrong schema", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			SandboxPlanSchemaV1, SandboxPlanSchemaV1+".next", 1), ErrInvalidSandboxPlan},
		{"wrong profile", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			"confined-linux-v1", "confined-linux-v2", 1), ErrInvalidSandboxPlan},
		{"malformed run", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			"223e4567-e89b-42d3-a456-426614174000", "not-a-uuid", 1), ErrInvalidSandboxPlan},
		{"malformed inner digest", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			strings.Repeat("a", 64), "not-a-digest", 1), ErrInvalidSandboxPlan},
		{"uppercase inner digest", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			strings.Repeat("b", 64), strings.Repeat("B", 64), 1), ErrInvalidSandboxPlan},
		{"unknown role", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			`"role":"git_common"`, `"role":"host_root"`, 1), ErrInvalidSandboxPlan},
		{"caller-selected destination", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			`"destination":"/project.git"`, `"destination":"/etc"`, 1), ErrInvalidSandboxPlan},
		{"nested destination", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			`"destination":"/tmp"`, `"destination":"/project/tmp"`, 1), ErrInvalidSandboxPlan},
		{"absolute host destination", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			`"destination":"/imports"`, `"destination":"/workspace"`, 1), ErrInvalidSandboxPlan},
		{"duplicate layout entry", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			`{"role":"git_common"`, `{"role":"runtime","destination":"/usr","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"role":"git_common"`, 1),
			ErrInvalidSandboxPlan},
		{"reordered layout", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			`{"role":"runtime","destination":"/usr","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"role":"project"`,
			`{"role":"project","destination":"/project","sha256":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},{"role":"runtime"`, 1),
			ErrInvalidSandboxPlan},
		{"missing layout entry", goldenSandboxPlanHash, strings.Replace(goldenSandboxPlanJSON,
			`,{"role":"imports","destination":"/imports","sha256":"4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"}`, "", 1),
			ErrInvalidSandboxPlan},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := VerifySandboxPlan(test.digest, test.body, facts)
			assertSandboxPlanError(t, err, test.want)
		})
	}
}

func TestVerifySandboxPlanRejectsMalformedFacts(t *testing.T) {
	facts := sandboxPlanFactsForTest(t)
	revision, err := DeriveSandboxPlan(facts)
	if err != nil {
		t.Fatalf("DeriveSandboxPlan: %v", err)
	}
	facts.RunID = "not-a-uuid"
	_, err = VerifySandboxPlan(revision.Digest, revision.CanonicalJSON, facts)
	assertSandboxPlanError(t, err, ErrInvalidSandboxPlan)

	facts = sandboxPlanFactsForTest(t)
	facts.Policy = nil
	_, err = VerifySandboxPlan(revision.Digest, revision.CanonicalJSON, facts)
	assertSandboxPlanError(t, err, ErrInvalidSandboxPlan)
}

func TestVerifySandboxPlanRejectsEveryBindingMutation(t *testing.T) {
	facts := sandboxPlanFactsForTest(t)
	revision, err := DeriveSandboxPlan(facts)
	if err != nil {
		t.Fatalf("DeriveSandboxPlan: %v", err)
	}
	bindingMutations := []struct {
		name   string
		mutate func(*SandboxPlanV1)
	}{
		{"run", func(plan *SandboxPlanV1) { plan.RunID = "423e4567-e89b-42d3-a456-426614174000" }},
		{"sandbox", func(plan *SandboxPlanV1) { plan.SandboxGeneration = "523e4567-e89b-42d3-a456-426614174000" }},
		{"request", func(plan *SandboxPlanV1) { plan.LaunchRequestDigest = strings.Repeat("4", 64) }},
	}
	for _, test := range bindingMutations {
		t.Run(test.name, func(t *testing.T) {
			_, err := recanonicalizedSandboxPlan(t, revision.Plan, test.mutate, facts)
			assertSandboxPlanError(t, err, ErrSandboxPlanBinding)
		})
	}

	policyMutations := []struct {
		name   string
		mutate func(*SandboxPlanV1)
	}{
		{"policy revision", func(plan *SandboxPlanV1) { plan.ProjectPolicyRevision = strings.Repeat("0", 64) }},
		{"confinement profile digest", func(plan *SandboxPlanV1) { plan.ConfinementProfileDigest = strings.Repeat("1", 64) }},
		{"provider profile digest", func(plan *SandboxPlanV1) { plan.ProviderProfileDigest = strings.Repeat("2", 64) }},
		{"resource policy digest", func(plan *SandboxPlanV1) { plan.ResourcePolicyDigest = strings.Repeat("3", 64) }},
		{"network policy digest", func(plan *SandboxPlanV1) { plan.NetworkPolicyDigest = strings.Repeat("4", 64) }},
		{"runtime identity", func(plan *SandboxPlanV1) { plan.Layout[0].SHA256 = strings.Repeat("5", 64) }},
		{"backing identity", func(plan *SandboxPlanV1) { plan.Layout[1].SHA256 = strings.Repeat("4", 64) }},
		{"git common identity", func(plan *SandboxPlanV1) { plan.Layout[2].SHA256 = strings.Repeat("5", 64) }},
		{"session identity", func(plan *SandboxPlanV1) { plan.Layout[3].SHA256 = strings.Repeat("6", 64) }},
		{"scratch identity", func(plan *SandboxPlanV1) { plan.Layout[4].SHA256 = strings.Repeat("7", 64) }},
		{"imports identity", func(plan *SandboxPlanV1) { plan.Layout[5].SHA256 = strings.Repeat("8", 64) }},
	}
	for _, test := range policyMutations {
		t.Run(test.name, func(t *testing.T) {
			_, err := recanonicalizedSandboxPlan(t, revision.Plan, test.mutate, facts)
			assertSandboxPlanError(t, err, ErrSandboxPlanPolicy)
		})
	}

	t.Run("cross-policy facts", func(t *testing.T) {
		otherFacts := sandboxPlanFactsWithInputForTest(t)
		_, err := VerifySandboxPlan(revision.Digest, revision.CanonicalJSON, otherFacts)
		assertSandboxPlanError(t, err, ErrSandboxPlanPolicy)
	})
}

func TestSandboxPlanDigestCoversEveryIndependentFact(t *testing.T) {
	base := sandboxPlanFactsForTest(t)
	revision, err := DeriveSandboxPlan(base)
	if err != nil {
		t.Fatalf("DeriveSandboxPlan: %v", err)
	}
	mutations := []struct {
		name   string
		mutate func(*SandboxPlanFacts)
	}{
		{"run", func(f *SandboxPlanFacts) { f.RunID = "423e4567-e89b-42d3-a456-426614174000" }},
		{"sandbox", func(f *SandboxPlanFacts) { f.SandboxGeneration = "523e4567-e89b-42d3-a456-426614174000" }},
		{"request digest", func(f *SandboxPlanFacts) { f.LaunchRequestDigest = strings.Repeat("4", 64) }},
		{"runtime identity", func(f *SandboxPlanFacts) { f.RuntimeRootSHA256 = strings.Repeat("f", 64) }},
		{"git common identity", func(f *SandboxPlanFacts) { f.GitCommonSHA256 = strings.Repeat("e", 64) }},
		{"session identity", func(f *SandboxPlanFacts) { f.SessionStateSHA256 = strings.Repeat("d", 64) }},
		{"scratch identity", func(f *SandboxPlanFacts) { f.ScratchSHA256 = strings.Repeat("c", 64) }},
		{"policy revision", func(f *SandboxPlanFacts) {
			t := sandboxPlanFactsWithInputForTest(t)
			f.Policy = t.Policy
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			facts := sandboxPlanFactsForTest(t)
			test.mutate(&facts)
			changed, err := DeriveSandboxPlan(facts)
			if err != nil {
				t.Fatalf("DeriveSandboxPlan: %v", err)
			}
			if changed.Digest == revision.Digest {
				t.Fatal("mutation did not change plan digest")
			}
		})
	}
}

func TestSandboxPlanBindsPolicyImportsExactly(t *testing.T) {
	facts := sandboxPlanFactsForTest(t)
	revision, err := DeriveSandboxPlan(facts)
	if err != nil {
		t.Fatalf("DeriveSandboxPlan: %v", err)
	}
	empty := sha256.Sum256([]byte("[]"))
	if got := revision.Plan.Layout[5].SHA256; got != hex.EncodeToString(empty[:]) {
		t.Fatalf("empty imports identity = %q, want sha256 of the empty canonical set", got)
	}
	if got := revision.Plan.Layout[5].SHA256; got != goldenSandboxPlanEmptyImports {
		t.Fatalf("empty imports identity = %q, want %q", got, goldenSandboxPlanEmptyImports)
	}

	inputFacts := sandboxPlanFactsWithInputForTest(t)
	inputRevision, err := DeriveSandboxPlan(inputFacts)
	if err != nil {
		t.Fatalf("DeriveSandboxPlan: %v", err)
	}
	if inputFacts.Policy.Digest != goldenSandboxPlanInputDigest {
		t.Fatalf("input policy digest = %q, want %q", inputFacts.Policy.Digest, goldenSandboxPlanInputDigest)
	}
	if inputRevision.Digest != goldenSandboxPlanWithInputHash {
		t.Fatalf("plan digest = %q, want %q", inputRevision.Digest, goldenSandboxPlanWithInputHash)
	}
	raw, err := json.Marshal(inputFacts.Policy.Policy.Inputs)
	if err != nil {
		t.Fatalf("marshal inputs: %v", err)
	}
	inputs := sha256.Sum256(raw)
	if got := inputRevision.Plan.Layout[5].SHA256; got != hex.EncodeToString(inputs[:]) {
		t.Fatalf("imports identity = %q, want sha256 of the canonical input set", got)
	}
	if inputRevision.Digest == revision.Digest {
		t.Fatal("admitted input did not change plan digest")
	}
}

func TestSandboxPlanBindsResourceAndNetworkPolicy(t *testing.T) {
	base := sandboxPlanPolicyValueForTest()
	baseRevision, err := projectpolicy.CanonicalizeProjectPolicy(base)
	if err != nil {
		t.Fatalf("canonicalize base policy: %v", err)
	}
	basePlan, err := DeriveSandboxPlan(sandboxPlanFactsForPolicy(t, baseRevision))
	if err != nil {
		t.Fatalf("derive base plan: %v", err)
	}

	resources := base
	resources.Resources.Processes++
	resourceRevision, err := projectpolicy.CanonicalizeProjectPolicy(resources)
	if err != nil {
		t.Fatalf("canonicalize resource policy: %v", err)
	}
	resourcePlan, err := DeriveSandboxPlan(sandboxPlanFactsForPolicy(t, resourceRevision))
	if err != nil {
		t.Fatalf("derive resource plan: %v", err)
	}
	if resourcePlan.Plan.ResourcePolicyDigest == basePlan.Plan.ResourcePolicyDigest ||
		resourcePlan.Plan.NetworkPolicyDigest != basePlan.Plan.NetworkPolicyDigest {
		t.Fatal("resource-only policy edit did not change only the resource component digest")
	}

	network := base
	network.Destinations = []projectpolicy.DestinationV1{{Host: "api.example.com", Port: 443}}
	networkRevision, err := projectpolicy.CanonicalizeProjectPolicy(network)
	if err != nil {
		t.Fatalf("canonicalize network policy: %v", err)
	}
	networkPlan, err := DeriveSandboxPlan(sandboxPlanFactsForPolicy(t, networkRevision))
	if err != nil {
		t.Fatalf("derive network plan: %v", err)
	}
	if networkPlan.Plan.NetworkPolicyDigest == basePlan.Plan.NetworkPolicyDigest ||
		networkPlan.Plan.ResourcePolicyDigest != basePlan.Plan.ResourcePolicyDigest {
		t.Fatal("network-only policy edit did not change only the network component digest")
	}
}

func TestDeriveSandboxPlanPreservesInputs(t *testing.T) {
	facts := sandboxPlanFactsWithInputForTest(t)
	beforeJSON, beforeInputs := facts.Policy.CanonicalJSON, slices.Clone(facts.Policy.Policy.Inputs)
	beforeDestinations := slices.Clone(facts.Policy.Policy.Destinations)

	first, err := DeriveSandboxPlan(facts)
	if err != nil {
		t.Fatalf("DeriveSandboxPlan: %v", err)
	}
	second, err := DeriveSandboxPlan(facts)
	if err != nil {
		t.Fatalf("DeriveSandboxPlan: %v", err)
	}
	if first.Digest != second.Digest || first.CanonicalJSON != second.CanonicalJSON {
		t.Fatal("derivation is not deterministic for identical facts")
	}
	if _, err := VerifySandboxPlan(first.Digest, first.CanonicalJSON, facts); err != nil {
		t.Fatalf("VerifySandboxPlan: %v", err)
	}

	if facts.Policy.CanonicalJSON != beforeJSON {
		t.Fatal("derive mutated the retained policy canonical bytes")
	}
	if !slices.Equal(facts.Policy.Policy.Inputs, beforeInputs) {
		t.Fatal("derive mutated the policy input set")
	}
	if !slices.Equal(facts.Policy.Policy.Destinations, beforeDestinations) {
		t.Fatal("derive mutated the policy destination set")
	}

	first.Plan.Layout[5].SHA256 = strings.Repeat("0", 64)
	if !slices.Equal(facts.Policy.Policy.Inputs, beforeInputs) {
		t.Fatal("mutating a derived plan reached the policy input set")
	}
	reverified, err := VerifySandboxPlan(first.Digest, first.CanonicalJSON, facts)
	if err != nil {
		t.Fatalf("VerifySandboxPlan after caller mutation of a derived plan value: %v", err)
	}
	raw, err := json.Marshal(facts.Policy.Policy.Inputs)
	if err != nil {
		t.Fatalf("marshal inputs: %v", err)
	}
	bound := sha256.Sum256(raw)
	if reverified.Plan.Layout[5].SHA256 != hex.EncodeToString(bound[:]) {
		t.Fatal("verify returned a plan inconsistent with the bound facts")
	}
}

func sandboxPlanPolicyValueForTest() projectpolicy.ProjectPolicyV1 {
	return projectpolicy.ProjectPolicyV1{
		Schema:                   projectpolicy.ProjectPolicySchemaV1,
		SecurityDomainID:         "00112233445566778899aabbccddeeff",
		BackingDigest:            strings.Repeat("e", 64),
		ConfinementProfileDigest: strings.Repeat("f", 64),
		ProviderProfileDigest:    strings.Repeat("9", 64),
		MemoryScopeDigest:        strings.Repeat("5", 64),
		MappedRemote: projectpolicy.MappedRemoteV1{
			Repository:      "github.com/noisefloor-oss/deck",
			PermissionClass: projectpolicy.RemotePermissionContribute,
		},
		Destinations: []projectpolicy.DestinationV1{},
		Inputs:       []projectpolicy.InputV1{},
		Resources: projectpolicy.ResourceLimitsV1{
			Processes:     512,
			MemoryBytes:   8 << 30,
			OutputBytes:   1 << 30,
			ProjectBytes:  10 << 30,
			ScratchBytes:  2 << 30,
			ProjectInodes: 1 << 20,
			ScratchInodes: 1 << 20,
			ExchangeBytes: 1 << 30,
			CPU:           projectpolicy.CPULimitV1{QuotaPerSecUsec: 2000000, PeriodUsec: 100000},
			IO: projectpolicy.IOLimitV1{
				ReadBytesPerSec:  64 << 20,
				WriteBytesPerSec: 64 << 20,
				ReadOpsPerSec:    4096,
				WriteOpsPerSec:   4096,
			},
		},
	}
}

func sandboxPlanFactsForTest(t *testing.T) SandboxPlanFacts {
	t.Helper()
	policy, err := projectpolicy.CanonicalizeProjectPolicy(sandboxPlanPolicyValueForTest())
	if err != nil {
		t.Fatalf("CanonicalizeProjectPolicy: %v", err)
	}
	if policy.Digest != goldenSandboxPlanPolicyDigest {
		t.Fatalf("policy fixture digest = %q, want %q", policy.Digest, goldenSandboxPlanPolicyDigest)
	}
	return sandboxPlanFactsForPolicy(t, policy)
}

func sandboxPlanFactsWithInputForTest(t *testing.T) SandboxPlanFacts {
	t.Helper()
	policyValue := sandboxPlanPolicyValueForTest()
	policyValue.Inputs = []projectpolicy.InputV1{{SHA256: strings.Repeat("3", 64), Bytes: 1024}}
	policy, err := projectpolicy.CanonicalizeProjectPolicy(policyValue)
	if err != nil {
		t.Fatalf("CanonicalizeProjectPolicy: %v", err)
	}
	return sandboxPlanFactsForPolicy(t, policy)
}

func sandboxPlanFactsForPolicy(t *testing.T, policy *projectpolicy.ProjectPolicyRevision) SandboxPlanFacts {
	t.Helper()
	return SandboxPlanFacts{
		RunID:               "223e4567-e89b-42d3-a456-426614174000",
		SandboxGeneration:   "323e4567-e89b-42d3-a456-426614174000",
		LaunchRequestDigest: strings.Repeat("1", 64),
		Policy:              policy,
		RuntimeRootSHA256:   strings.Repeat("a", 64),
		GitCommonSHA256:     strings.Repeat("b", 64),
		SessionStateSHA256:  strings.Repeat("c", 64),
		ScratchSHA256:       strings.Repeat("d", 64),
	}
}

// recanonicalizedSandboxPlan applies a mutation to a derived plan, recanonicalizes
// the mutated value so only the binding comparison can fail, and verifies it
// against the original facts.
func recanonicalizedSandboxPlan(
	t *testing.T,
	plan SandboxPlanV1,
	mutate func(*SandboxPlanV1),
	facts SandboxPlanFacts,
) (*SandboxPlanRevision, error) {
	t.Helper()
	plan.Layout = slices.Clone(plan.Layout)
	mutate(&plan)
	mutated, err := canonicalizeSandboxPlanForTest(plan)
	if err != nil {
		t.Fatalf("canonicalize mutated plan: %v", err)
	}
	return VerifySandboxPlan(mutated.Digest, mutated.CanonicalJSON, facts)
}

// canonicalizeSandboxPlanForTest digests a well-formed plan value without the
// facts binding, so tests can present self-consistent hostile bytes.
func canonicalizeSandboxPlanForTest(plan SandboxPlanV1) (*SandboxPlanRevision, error) {
	if !validSandboxPlan(plan) {
		return nil, ErrInvalidSandboxPlan
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return &SandboxPlanRevision{
		Digest:        hex.EncodeToString(digest[:]),
		CanonicalJSON: string(raw),
		Plan:          plan,
	}, nil
}

func assertSandboxPlanError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if err.Error() != want.Error() {
		t.Fatalf("error text = %q, want content-free %q", err, want)
	}
}
