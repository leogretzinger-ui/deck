package workspacegateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
)

type plainOperations struct {
	keys         *workspacekeys.Registry
	executeErr   error
	executions   int
	preparations int
	observations int
	forged       bool
}

func (o *plainOperations) Prepare(ctx context.Context, login auth.Login) (workspacekeys.PlainStartPermit, error) {
	o.preparations++
	if o.forged {
		return workspacekeys.PlainStartPermit{}, nil
	}
	intent, err := o.keys.CurrentPlainStart(ctx)
	if err != nil {
		return workspacekeys.PlainStartPermit{}, err
	}
	return o.keys.AuthorizePlainStart(ctx, login, intent.ID)
}

func (o *plainOperations) Execute(ctx context.Context, login auth.Login, permit workspacekeys.PlainStartPermit) error {
	if err := o.keys.ValidatePlainStart(ctx, login, permit); err != nil {
		return err
	}
	o.executions++
	if o.executeErr != nil {
		return o.executeErr
	}
	return o.keys.CompletePlainStart(ctx, permit)
}

func (o *plainOperations) Status(ctx context.Context, login auth.Login, id string) (OperationState, error) {
	o.observations++
	intent, err := o.keys.PlainOperation(ctx, login, id)
	return OperationState{ID: id, Status: intent.Status, RuntimeState: RuntimeState{Storage: "unencrypted", Runtime: "unknown"}}, err
}

type onboardingAccess struct{}

func (onboardingAccess) Require(context.Context, auth.Login) error { return nil }
func (onboardingAccess) Logout(context.Context, auth.Login) error  { return nil }

func TestPublicOnboardingContainsOnlyCoarseInstallationState(t *testing.T) {
	f := newFixtureWithPolicy(t, Options{}, workspacekeys.EncryptionOptional)
	assertPublic := func(protection string, started bool) {
		t.Helper()
		w := f.serve(httptest.NewRequest("GET", testOrigin+apiPrefix+"/onboarding", nil))
		var state map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || len(state) != 4 || state["policy"] != "optional" || state["protection"] != protection || state["setupComplete"] != false || state["started"] != started {
			t.Fatalf("unexpected public metadata: %d %s", w.Code, w.Body)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("public onboarding metadata was cacheable")
		}
	}
	assertPublic("undecided", false)
	for _, protection := range []string{"none", "encrypted"} {
		w := f.serve(f.signedRequest(t, "POST", apiPrefix+"/onboarding/protection", map[string]string{"protection": protection}, f.login))
		if w.Code != 403 {
			t.Fatalf("forged Google session chose protection before receipt: %d", w.Code)
		}
	}
	receipt := f.enroll(t)
	assertPublic("undecided", true)
	w := f.serve(f.signedRequest(t, "POST", apiPrefix+"/onboarding/protection", map[string]string{"protection": "none"}, f.login))
	if w.Code != 403 {
		t.Fatal("bound Google session chose protection without setup receipt", w.Code)
	}
	w = f.serve(f.signedRequest(t, "POST", apiPrefix+"/onboarding/protection", map[string]string{"protection": "none"}, f.login, receipt))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	assertPublic("none", true)
	if w = f.serve(f.signedRequest(t, "POST", apiPrefix+"/methods/prepare", map[string]string{"kind": "password", "label": "Password"}, f.login, receipt)); w.Code != 403 {
		t.Fatal("unencrypted setup retained encryption preparation authority", w.Code)
	}
}

func TestPlainOnboardingStartDoesNotInventEncryptedGrant(t *testing.T) {
	o := &plainOperations{}
	f := newFixtureWithPolicy(t, Options{PlainOperations: o, Access: onboardingAccess{}}, workspacekeys.EncryptionOptional)
	o.keys = f.keys
	receipt := f.enroll(t)
	if w := f.serve(f.signedRequest(t, "POST", apiPrefix+"/onboarding/start", map[string]string{}, f.login)); w.Code != 403 || o.preparations != 0 {
		t.Fatal("start dispatched before explicit selection", w.Code)
	}
	if w := f.serve(f.signedRequest(t, "POST", apiPrefix+"/onboarding/protection", map[string]string{"protection": "none"}, f.login, receipt)); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	readState := func() map[string]any {
		t.Helper()
		w := f.serve(f.signedRequest(t, "GET", apiPrefix+"/state", nil, f.login))
		var state map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || w.Code != 200 {
			t.Fatal(w.Code, err)
		}
		return state
	}
	state := readState()
	id, ok := state["plainOperationId"].(string)
	if !ok || id == "" || state["phase"] != "awaiting-start" || state["browserGranted"] != false || state["workspaceAuthorized"] != false || state["ownerBound"] != true {
		t.Fatalf("selection implied access or omitted operation receipt: %v", state)
	}
	newLogin := f.login
	newLogin.ID = strings.Repeat("b", 64)
	o.forged = true
	w := f.serve(f.signedRequest(t, "POST", apiPrefix+"/onboarding/start", map[string]string{}, newLogin))
	if w.Code != 403 || o.executions != 0 {
		t.Fatal("service supplied an unbound permit", w.Code)
	}
	o.forged = false
	w = f.serve(f.signedRequest(t, "POST", apiPrefix+"/onboarding/start", map[string]string{"operationId": id}, newLogin))
	if w.Code != 200 || o.executions != 1 {
		t.Fatal("pinned owner could not resume without old receipt", w.Code, w.Body)
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "__Host-deck_workspace" {
			t.Fatal("plain start minted an encrypted browser grant")
		}
	}
	state = readState()
	if state["phase"] != "ready" || state["browserGranted"] != false || state["workspaceAuthorized"] != true || state["runtime"] != "unknown" || state["storage"] != "unknown" {
		t.Fatalf("completed setup fabricated encryption/runtime state: %v", state)
	}
	w = f.serve(f.signedRequest(t, "POST", apiPrefix+"/onboarding/start", map[string]string{"operationId": id}, newLogin))
	if w.Code != 409 || o.executions != 1 {
		t.Fatal("completed intent replayed", w.Code)
	}
}

func TestUncertainPlainStartOnlyObservesReservedIntent(t *testing.T) {
	o := &plainOperations{executeErr: errors.New("uncertain native result")}
	f := newFixtureWithPolicy(t, Options{PlainOperations: o}, workspacekeys.EncryptionUnavailable)
	o.keys = f.keys
	receipt := f.enroll(t)
	if w := f.serve(f.signedRequest(t, "POST", apiPrefix+"/onboarding/protection", map[string]string{"protection": "none"}, f.login, receipt)); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w := f.serve(f.signedRequest(t, "POST", apiPrefix+"/onboarding/start", map[string]string{}, f.login))
	var result struct{ ID, Status string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 202 || result.ID == "" || result.Status != "unconfirmed" {
		t.Fatal("uncertain start lost public receipt", w.Code, w.Body, err)
	}
	for range 2 {
		w = f.serve(f.signedRequest(t, "GET", apiPrefix+"/onboarding/operations/"+result.ID, nil, f.login))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"pending"`) || !strings.Contains(w.Body.String(), `"storage":"unencrypted"`) {
			t.Fatal(w.Code, w.Body)
		}
	}
	if o.executions != 1 || o.observations != 2 {
		t.Fatal("observation replayed native work")
	}
	other := f.login
	other.Subject = "other-owner"
	w = f.serve(f.signedRequest(t, "GET", apiPrefix+"/onboarding/operations/"+result.ID, nil, other))
	if w.Code != 404 || o.observations != 2 {
		t.Fatal("another identity observed private operation", w.Code)
	}
	if state, err := f.keys.Onboarding(context.Background()); err != nil || state.SetupComplete {
		t.Fatal("uncertain start completed setup", err)
	}
}

func TestOnboardingMutationUsesExistingOriginAndCSRFGates(t *testing.T) {
	f := newFixtureWithPolicy(t, Options{}, workspacekeys.EncryptionOptional)
	receipt := f.enroll(t)
	for _, path := range []string{"/onboarding/protection", "/onboarding/start"} {
		for _, header := range []string{"Origin", auth.CSRFHeader} {
			r := f.signedRequest(t, "POST", apiPrefix+path, map[string]string{"protection": "none"}, f.login, receipt)
			r.Header.Del(header)
			if w := f.serve(r); w.Code != http.StatusForbidden {
				t.Fatalf("%s accepted absent %s: %d", path, header, w.Code)
			}
		}
	}
}
