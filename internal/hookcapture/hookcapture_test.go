package hookcapture

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordingStore struct {
	grant      Grant
	acceptHash string
	accept     Event
	acceptErr  error
}

func (s *recordingStore) CreateIdentityCapture(_ context.Context, grant Grant) error {
	s.grant = grant
	return nil
}

func (s *recordingStore) AcceptIdentityCapture(_ context.Context, tokenHash string,
	event Event, _ time.Time) error {
	s.acceptHash, s.accept = tokenHash, event
	return s.acceptErr
}

func (*recordingStore) RevokeIdentityCapture(context.Context, string, int, string,
	time.Time) (GrantState, error) {
	return GrantRevoked, nil
}

func (*recordingStore) IdentityCapturePending(context.Context, string, int, string,
	time.Time) (bool, error) {
	return true, nil
}

func (*recordingStore) ExpireIdentityCaptures(context.Context, time.Time) error { return nil }

func TestRegisterPersistsOnlyHashAndCanonicalDirectory(t *testing.T) {
	realDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "work")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	store := &recordingStore{}
	service, err := NewService(store, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.RegisterFenced(context.Background(), "row", "codex", link,
		2, "marker")
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || store.grant.TokenHash != hashToken(token) ||
		store.grant.TokenHash == token {
		t.Fatalf("bearer/hash separation failed: token length=%d hash=%q",
			len(token), store.grant.TokenHash)
	}
	if store.grant.CanonicalWorkDir != realDir {
		t.Fatalf("canonical work directory = %q, want %q",
			store.grant.CanonicalWorkDir, realDir)
	}
	if err := ValidateEvent("codex", store.grant.CanonicalWorkDir, Event{
		HookEventName: "SessionStart",
		SessionID:     "12345678-1234-4abc-8abc-123456789abc",
		CWD:           link, Source: ExpectedSourceStartup,
	}); err != nil {
		t.Fatalf("documented event through equivalent canonical cwd: %v", err)
	}
}

func TestHookHTTPEnvelopeAndStoreOutcomes(t *testing.T) {
	store := &recordingStore{}
	service, err := NewService(store, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"session_id":"12345678-1234-4abc-8abc-123456789abc","cwd":"/work","source":"startup"}`
	post := func(token, payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/hooks/capture", strings.NewReader(payload))
		if token != "" {
			req.Header.Set("X-Noisedeck-Hook-Token", token)
		}
		rec := httptest.NewRecorder()
		service.Handle().ServeHTTP(rec, req)
		return rec
	}

	if got := post("", body).Code; got != http.StatusBadRequest {
		t.Fatalf("missing bearer = %d, want 400", got)
	}
	if got := post("token", body+` {}`).Code; got != http.StatusBadRequest {
		t.Fatalf("multiple JSON values = %d, want 400", got)
	}
	if got := post("token", strings.Repeat("x", maxEventBytes+1)).Code; got != http.StatusBadRequest {
		t.Fatalf("oversized body = %d, want 400", got)
	}

	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{name: "accepted", want: http.StatusNoContent},
		{name: "unavailable", err: ErrUnknownToken, want: http.StatusGone},
		{name: "conflict", err: ErrConflict, want: http.StatusConflict},
		{name: "storage", err: errors.New("injected"), want: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.acceptErr = tc.err
			if got := post("token", body).Code; got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
	if store.acceptHash != hashToken("token") || store.accept.SessionID == "" {
		t.Fatalf("store received hash=%q event=%+v", store.acceptHash, store.accept)
	}
}

// Well-formedness is the whole of the check: a session id of the provider's
// known shape, in the working directory the grant was minted for. The hook
// name and the entry source the provider used to deliver it are provenance,
// not permission — a rule about which of them may carry which id would model
// internals of a CLI Deck does not control.
func TestValidationAcceptsAnyHookNameAndSource(t *testing.T) {
	workDir := t.TempDir()
	const id = "12345678-1234-4abc-8abc-123456789abc"
	for _, hookEvent := range []string{"SessionStart", "Stop", "UserPromptSubmit", ""} {
		for _, source := range []string{ExpectedSourceStartup, ExpectedSourceResume,
			"fork", "clear", "compact", "current", ""} {
			ev := Event{HookEventName: hookEvent, SessionID: id, CWD: workDir, Source: source}
			for _, provider := range []string{"claude", "codex", "command-code"} {
				if err := ValidateEvent(provider, workDir, ev); err != nil {
					t.Fatalf("%s event hook=%q source=%q refused: %v",
						provider, hookEvent, source, err)
				}
			}
		}
	}
}

// The two sanity checks that do refuse: an id that is not the provider's
// shape, and an event from a directory the grant was never minted for. Both
// catch a misconfigured hook posting somebody else's payload, and neither
// refuses anything a correctly installed provider sends.
func TestValidationRefusesForeignShapeAndDirectory(t *testing.T) {
	workDir := t.TempDir()
	const id = "12345678-1234-4abc-8abc-123456789abc"
	for name, ev := range map[string]Event{
		"empty id":       {HookEventName: "SessionStart", CWD: workDir, Source: ExpectedSourceStartup},
		"foreign shape":  {HookEventName: "SessionStart", SessionID: "not-a-uuid", CWD: workDir, Source: ExpectedSourceStartup},
		"foreign cwd":    {HookEventName: "SessionStart", SessionID: id, CWD: t.TempDir(), Source: ExpectedSourceStartup},
		"cwd unreported": {HookEventName: "SessionStart", SessionID: id, Source: ExpectedSourceStartup},
	} {
		if err := ValidateEvent("codex", workDir, ev); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s returned %v, want conflict", name, err)
		}
	}
}

// Devin's documented SessionStart payload omits cwd, and its project
// directory never crosses the capture endpoint; the pre-spawn grant carries
// the binding instead. Its id shape is still checked.
func TestDevinEventIsExemptFromTheDirectoryCheck(t *testing.T) {
	workDir := t.TempDir()
	if err := ValidateEvent("devin", workDir, Event{
		HookEventName: "SessionStart", SessionID: "sample-session",
		Source: ExpectedSourceResume,
	}); err != nil {
		t.Fatalf("documented Devin event rejected: %v", err)
	}
	if err := ValidateEvent("devin", workDir, Event{
		HookEventName: "SessionStart", SessionID: "", Source: ExpectedSourceResume,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("empty Devin id returned %v, want conflict", err)
	}
}

func TestPiStartupEventRequiresMeasuredShapeAndCwd(t *testing.T) {
	workDir := t.TempDir()
	// The id shape is pi's measured session_start payload (UUIDv7).
	const id = "01a02c59-f6d2-7886-be70-f6d4173068d9"
	valid := Event{HookEventName: "SessionStart", SessionID: id, CWD: workDir, Source: ExpectedSourceStartup}
	if err := ValidateEvent("pi", workDir, valid); err != nil {
		t.Fatalf("documented pi startup event rejected: %v", err)
	}
	for _, event := range []Event{
		{HookEventName: "SessionStart", SessionID: id, CWD: t.TempDir(), Source: ExpectedSourceStartup},
		// A v4 id was never minted by pi's session manager: an assigned-id
		// echo would be evidence the launch bypassed the hook.
		{HookEventName: "SessionStart", SessionID: "0dc08a87-0ca6-4d9e-be31-6e9c8e7c816b", CWD: workDir, Source: ExpectedSourceStartup},
	} {
		if err := ValidateEvent("pi", workDir, event); !errors.Is(err, ErrConflict) {
			t.Fatalf("conflicting event %+v returned %v", event, err)
		}
	}
}

// Replay equivalence covers the fact an event carries — which conversation,
// in which ledger — so a duplicate delivery is a no-op however the provider
// labelled it, and a same-session event naming a different ledger is not.
func TestAcceptedEventHashCoversTheFactNotItsLabel(t *testing.T) {
	const (
		dir    = "/workspace/app"
		id     = "12345678-1234-4abc-8abc-123456789abc"
		ledger = "/home/op/.claude/projects/-workspace-app/" + id + ".jsonl"
	)
	base := Event{HookEventName: "SessionStart", SessionID: id, CWD: dir,
		Source: ExpectedSourceStartup, TranscriptPath: ledger}
	relabelled := base
	relabelled.HookEventName, relabelled.Source = "Stop", "current"
	if AcceptedEventHash("claude", dir, base) != AcceptedEventHash("claude", dir, relabelled) {
		t.Fatal("a relabelled duplicate delivery is not treated as a replay")
	}
	moved := base
	moved.TranscriptPath = "/srv/store/projects/-workspace-app/" + id + ".jsonl"
	if AcceptedEventHash("claude", dir, base) == AcceptedEventHash("claude", dir, moved) {
		t.Fatal("the same session in a different ledger hashed as a replay")
	}
	other := base
	other.SessionID = "22345678-1234-4abc-8abc-123456789abc"
	if AcceptedEventHash("claude", dir, base) == AcceptedEventHash("claude", dir, other) {
		t.Fatal("a different conversation hashed as a replay")
	}
}
