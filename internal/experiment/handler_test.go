package experiment

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestExperimentIsDisabledByDefault(t *testing.T) {
	h := New(false, "", nil, nil, nil, nil, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/experimental/harnesses", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ListHarnesses(rec, req)

	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"experiment_disabled"`) {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestExperimentRejectsNonLoopbackCallers(t *testing.T) {
	h := New(true, "test-capability", nil, nil, nil, nil, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/experimental/harnesses", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	rec := httptest.NewRecorder()
	h.ListHarnesses(rec, req)

	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"loopback_required"`) {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestExperimentRejectsLoopbackWithoutProcessCapability(t *testing.T) {
	h := New(true, "test-capability", nil, nil, nil, nil, zerolog.Nop())
	for _, provided := range []string{"", "wrong-capability"} {
		req := httptest.NewRequest(http.MethodGet, "/api/experimental/harnesses", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set(CapabilityHeader, provided)
		rec := httptest.NewRecorder()
		h.ListHarnesses(rec, req)

		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"capability_required"`) {
			t.Fatalf("capability %q response = %d %s", provided, rec.Code, rec.Body.String())
		}
	}
}

func TestControlCapabilitiesAreFresh(t *testing.T) {
	a, err := NewControlCapability()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewControlCapability()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || len(a) != 64 || len(b) != 64 {
		t.Fatalf("capability lengths = %d and %d, equal = %v", len(a), len(b), a == b)
	}
}

func TestBootstrapPromptCarriesOnlyBoundedReferencesAndFreshAcceptance(t *testing.T) {
	request := handoffRequest{Role: "reviewer", TaskID: "295", CorrelationID: "operator-route"}
	prompt := bootstrapPrompt(request, "project/deck", "implementation",
		"delegate-reviewer-claude", "source", "handoff-fresh", "handoff.accepted", "handoff.completed")

	for _, want := range []string{
		"delegated reviewer", "task 295", "noise tasks show 295",
		"--type handoff.accepted", "--stream project/deck", "--room implementation",
		"--from-agent delegate-reviewer-claude", "--to-agent source",
		"--correlation-id handoff-fresh", "--status claimed",
		"close the handoff exactly once", "--type handoff.completed",
		"--status applied", "--status failed", "Never attach a body, metadata, or artifacts",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not contain %q: %s", want, prompt)
		}
	}
	// The completion closure must ride the same fresh acceptance identity,
	// never a caller-supplied correlation.
	if strings.Count(prompt, "handoff-fresh") != 2 {
		t.Fatalf("prompt did not reuse the fresh correlation for both events: %s", prompt)
	}
	if strings.Contains(prompt, request.CorrelationID) {
		t.Fatalf("prompt reused caller correlation instead of fresh acceptance identity: %s", prompt)
	}
}

func TestFreshCorrelationIDsCannotMatchStaleAcceptance(t *testing.T) {
	a, err := freshCorrelationID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := freshCorrelationID()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !strings.HasPrefix(a, "handoff-") || !strings.HasPrefix(b, "handoff-") {
		t.Fatalf("fresh correlations = %q and %q", a, b)
	}
}

func TestProjectSlugIsStableAndNeverEmpty(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{"/workspace/Deck", "deck"},
		{"/workspace/My  Project!", "my-project"},
		{"/workspace/!!!", "project"},
	} {
		if got := projectSlug(tc.path); got != tc.want {
			t.Errorf("projectSlug(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestRoleSlugDoesNotDefineALifecycleVocabulary(t *testing.T) {
	for role, want := range map[string]string{
		"reviewer":                "reviewer",
		"review.formal":           "review-formal",
		"implementation.standard": "implementation-standard",
	} {
		if got := roleSlug(role); got != want {
			t.Errorf("roleSlug(%q) = %q, want %q", role, got, want)
		}
	}
}
