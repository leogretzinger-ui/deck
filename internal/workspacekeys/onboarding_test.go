package workspacekeys

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProtectionSelectionRequiresReceiptAndCannotChange(t *testing.T) {
	f := newFixtureWithPolicy(t, EncryptionOptional)
	for _, actor := range []Actor{NewActor(f.actor.Login, ""), NewActor(f.actor.Login, randomToken())} {
		_, err := f.r.SelectProtection(f.ctx, actor, ProtectionNone)
		refused(t, err)
	}
	state, err := f.r.Onboarding(f.ctx)
	if err != nil || state != (OnboardingState{Policy: EncryptionOptional, Protection: ProtectionUndecided, Started: true}) {
		t.Fatalf("unauthorized selection changed state: %+v %v", state, err)
	}
	state, err = f.r.SelectProtection(f.ctx, f.actor, ProtectionNone)
	if err != nil || state.SetupComplete {
		t.Fatalf("selection claimed completion: %+v %v", state, err)
	}
	_, err = f.r.SelectProtection(f.ctx, f.actor, ProtectionEncrypted)
	refused(t, err)
	_, err = f.r.PrepareMethod(f.ctx, f.actor, Password, "Password")
	refused(t, err)
	var receipts, methods int
	if err := f.db.QueryRow(`SELECT count(*) FROM workspacekeys_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT count(*) FROM workspacekeys_methods`).Scan(&methods); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 || methods != 0 {
		t.Fatal("unencrypted choice retained bootstrap authority or created a method")
	}
}

func TestProtectionPolicyAndUpgradeNeverDowngrade(t *testing.T) {
	for _, policy := range []EncryptionPolicy{EncryptionRequired, EncryptionOptional, EncryptionUnavailable} {
		t.Run(string(policy), func(t *testing.T) {
			f := newFixtureWithPolicy(t, policy)
			cfg := f.r.cfg
			cfg.EncryptionPolicy = EncryptionOptional
			if policy == EncryptionOptional {
				cfg.EncryptionPolicy = EncryptionRequired
			}
			_, err := New(f.ctx, f.db, cfg)
			refused(t, err)
			_, err = OpenExisting(f.ctx, f.db, cfg)
			refused(t, err)
			if policy == EncryptionRequired {
				_, err = f.r.SelectProtection(f.ctx, f.actor, ProtectionNone)
				refused(t, err)
			}
			if policy == EncryptionUnavailable {
				_, err = f.r.SelectProtection(f.ctx, f.actor, ProtectionEncrypted)
				refused(t, err)
				_, err = f.r.PrepareMethod(f.ctx, f.actor, Password, "Password")
				refused(t, err)
				if _, err = f.r.SelectProtection(f.ctx, f.actor, ProtectionNone); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	for _, initialized := range []bool{false, true} {
		f := newFixture(t)
		if initialized {
			f.initialize()
		}
		if _, err := f.db.Exec(`DROP TABLE workspacekeys_onboarding; DROP TABLE workspacekeys_plain_operations`); err != nil {
			t.Fatal(err)
		}
		cfg := f.r.cfg
		cfg.EncryptionPolicy = EncryptionOptional
		_, err := New(f.ctx, f.db, cfg)
		refused(t, err)
		cfg.EncryptionPolicy = ""
		upgraded, err := New(f.ctx, f.db, cfg)
		if err != nil {
			t.Fatal(err)
		}
		state, err := upgraded.Onboarding(f.ctx)
		if err != nil || state.Policy != EncryptionRequired || state.SetupComplete != initialized || initialized && state.Protection != ProtectionEncrypted {
			t.Fatalf("upgrade reinterpreted existing installation: %+v %v", state, err)
		}
	}
}

func TestFirstMethodAndUnencryptedSelectionAreAtomic(t *testing.T) {
	f := newFixtureWithPolicy(t, EncryptionOptional)
	var prepareErr, selectErr error
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, prepareErr = f.r.PrepareMethod(f.ctx, f.actor, Password, "Password")
	}()
	go func() {
		defer wg.Done()
		<-start
		_, selectErr = f.r.SelectProtection(f.ctx, f.actor, ProtectionNone)
	}()
	close(start)
	wg.Wait()
	if (prepareErr == nil) == (selectErr == nil) {
		t.Fatalf("selection and preparation did not exclude one another: %v %v", prepareErr, selectErr)
	}
	state, err := f.r.Onboarding(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if prepareErr == nil && state.Protection != ProtectionEncrypted || selectErr == nil && state.Protection != ProtectionNone {
		t.Fatalf("committed operation and protection disagree: %+v", state)
	}
	_, err = f.r.SelectProtection(f.ctx, f.actor, ProtectionNone)
	refused(t, err)
}

func TestFailedMethodStorageDoesNotCommitProtectionChoice(t *testing.T) {
	f := newFixtureWithPolicy(t, EncryptionOptional)
	if _, err := f.db.Exec(`CREATE TRIGGER refuse_method BEFORE INSERT ON workspacekeys_methods BEGIN SELECT RAISE(ABORT,'test storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.r.PrepareMethod(f.ctx, f.actor, Password, "Password")
	refused(t, err)
	state, err := f.r.Onboarding(f.ctx)
	if err != nil || state.Protection != ProtectionUndecided {
		t.Fatal("failed preparation left an irreversible selection", state, err)
	}
	if _, err := f.r.SelectProtection(f.ctx, f.actor, ProtectionNone); err != nil {
		t.Fatal("rolled back preparation blocked the other available choice", err)
	}
}

func TestPlainStartPermitsResumeWithoutReopeningSetup(t *testing.T) {
	f := newFixtureWithPolicy(t, EncryptionOptional)
	_, err := f.r.AuthorizePlainStart(f.ctx, f.actor.Login, "invented")
	refused(t, err)
	if _, err = f.r.SelectProtection(f.ctx, f.actor, ProtectionNone); err != nil {
		t.Fatal(err)
	}
	intent, err := f.r.CurrentPlainStart(f.ctx)
	if err != nil || intent.Status != "pending" {
		t.Fatal(intent, err)
	}
	newLogin := f.actor.Login
	newLogin.ID = strings.Repeat("02", 32)
	permit, err := f.r.AuthorizePlainStart(f.ctx, newLogin, intent.ID)
	if err != nil {
		t.Fatal("pinned owner could not resume without setup receipt", err)
	}
	refused(t, f.r.ValidatePlainStart(f.ctx, f.actor.Login, permit))
	wrongOwner := newLogin
	wrongOwner.Subject = "other"
	_, err = f.r.AuthorizePlainStart(f.ctx, wrongOwner, intent.ID)
	refused(t, err)
	_, err = f.r.PlainOperation(f.ctx, wrongOwner, intent.ID)
	refused(t, err)
	_, err = f.r.ReservePlainStart(f.ctx, newLogin, intent.ID)
	refused(t, err)
	encoded, err := json.Marshal(permit) //nolint:staticcheck // Verify private permit authority cannot be serialized.
	if err != nil || string(encoded) != "{}" {
		t.Fatalf("permit serialized authority: %s %v", encoded, err)
	}
	var decoded PlainStartPermit
	if err = json.Unmarshal(encoded, &decoded); err != nil { //nolint:staticcheck // A decoded public record must confer no authority.
		t.Fatal(err)
	}
	refused(t, f.r.CompletePlainStart(f.ctx, decoded))
	f.now = f.now.Add(6 * time.Minute)
	refused(t, f.r.CompletePlainStart(f.ctx, permit))
	oldRegistry := f.r
	f.r, err = New(f.ctx, f.db, f.r.cfg)
	if err != nil {
		t.Fatal(err)
	}
	refused(t, f.r.CompletePlainStart(f.ctx, permit))
	permit, err = f.r.AuthorizePlainStart(f.ctx, newLogin, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	refused(t, oldRegistry.CompletePlainStart(f.ctx, permit))
	if err = f.r.CompletePlainStart(f.ctx, permit); err != nil {
		t.Fatal(err)
	}
	refused(t, f.r.CompletePlainStart(f.ctx, permit))
	state, err := f.r.State(f.ctx, newLogin)
	if err != nil || !state.Onboarding.SetupComplete || state.Initialized || state.BootstrapClosed || state.Commitment != "" {
		t.Fatalf("plain completion impersonated encrypted initialization: %+v %v", state, err)
	}
	next, err := f.r.ReservePlainStart(f.ctx, newLogin, intent.ID)
	if err != nil || next.ID() == intent.ID {
		t.Fatalf("later start did not receive distinct durable intent: %v", err)
	}
	_, err = f.r.ReservePlainStart(f.ctx, newLogin, intent.ID)
	refused(t, err)
	old, err := f.r.PlainOperation(f.ctx, newLogin, intent.ID)
	if err != nil || old.Status != "succeeded" {
		t.Fatal("old receipt not observable", err)
	}
	if err = f.r.FailPlainStart(f.ctx, next); err != nil {
		t.Fatal(err)
	}
	state, err = f.r.State(f.ctx, newLogin)
	if err != nil || !state.Onboarding.SetupComplete {
		t.Fatal("later runtime failure erased completed setup", err)
	}
}
