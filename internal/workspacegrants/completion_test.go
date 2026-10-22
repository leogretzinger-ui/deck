package workspacegrants

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/workspacekeys"
)

func TestNativeInitializationCarriesFreshProofIntoObservedEpoch(t *testing.T) {
	f := newFixture(t, false)
	permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
	before := permit.RuntimeEpoch
	pending, err := f.keys.PendingInitialization(f.ctx)
	must(t, err)
	_, err = f.store.CompleteNative(f.ctx, f.actor.Login, pending, "running")
	denied(t, err)
	_, err = f.store.CompleteNative(f.ctx, browser(f.actor.Login, "02"), permit, "running")
	denied(t, err)
	changed := permit
	changed.Action.Label = "different action"
	_, err = f.store.CompleteNative(f.ctx, f.actor.Login, changed, "running")
	denied(t, err)
	changed = permit
	changed.Payload = []byte(`{}`)
	_, err = f.store.CompleteNative(f.ctx, f.actor.Login, changed, "running")
	denied(t, err)
	changed = permit
	changed.RuntimeEpoch = "running"
	_, err = f.store.CompleteNative(f.ctx, f.actor.Login, changed, "running")
	denied(t, err)
	completed, err := f.store.CompleteNative(f.ctx, f.actor.Login, permit, "running")
	must(t, err)
	if completed.ID != permit.ID || completed.RuntimeEpoch != "running" || permit.RuntimeEpoch != before || !bytes.Equal(completed.Payload, permit.Payload) {
		t.Fatal("completion changed intent or mutated the original proof")
	}
	state, err := f.keys.State(f.ctx, f.actor.Login)
	must(t, err)
	if !state.Initialized || state.RuntimeEpoch != "running" {
		t.Fatal("native completion did not initialize at the observed epoch")
	}
	_, err = f.store.Issue(f.ctx, f.actor.Login, permit)
	denied(t, err)
	delivery, err := f.store.Issue(f.ctx, f.actor.Login, completed)
	must(t, err)
	must(t, f.store.Require(WithToken(f.ctx, delivery.Token()), f.actor.Login))
	_, err = f.store.Issue(f.ctx, f.actor.Login, completed)
	denied(t, err)
	_, err = f.store.CompleteNative(f.ctx, f.actor.Login, completed, "another_epoch")
	denied(t, err)
}

func TestNativeInitializationRechecksBothPinnedEnvelopeDigests(t *testing.T) {
	for _, kind := range []string{"everyday", "recovery"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, false)
			permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
			id := f.day.ref.ID
			if kind == "recovery" {
				id = f.recovery.ref.ID
			}
			_, err := f.db.Exec(`UPDATE workspacekeys_methods SET envelope_digest='changed' WHERE id=?`, id)
			must(t, err)
			_, err = f.store.CompleteNative(f.ctx, f.actor.Login, permit, "running")
			denied(t, err)
			state, err := f.keys.State(f.ctx, f.actor.Login)
			must(t, err)
			if state.Initialized || state.RuntimeEpoch != permit.RuntimeEpoch {
				t.Fatal("rejected completion changed runtime authority")
			}
		})
	}
}

func TestNativeUnlockClosesOldStreamsAndFencesOtherProofs(t *testing.T) {
	f := newFixture(t, true)
	first := f.actor.Login
	day := f.grant(f.day)
	dayClosed, releaseDay := attach(t, f, first, day)
	defer releaseDay()
	f.actor = workspacekeys.NewActor(browser(first, "02"), "")
	recovery := f.grant(f.recovery)
	recoveryClosed, releaseRecovery := attach(t, f, f.actor.Login, recovery)
	defer releaseRecovery()
	unused := f.prove(f.recovery, workspacekeys.Action{Operation: workspacekeys.AuthorizeBrowser, Method: f.recovery.ref})
	unlock := f.prove(f.recovery, workspacekeys.Action{Operation: workspacekeys.Unlock, Method: f.recovery.ref})
	completed, err := f.store.CompleteNative(f.ctx, f.actor.Login, unlock, "unlocked")
	must(t, err)
	if dayClosed.Load() != 1 || recoveryClosed.Load() != 1 {
		t.Fatal("native completion returned before closing old streams")
	}
	denied(t, f.store.Require(WithToken(f.ctx, day.Token()), first))
	denied(t, f.store.Require(WithToken(f.ctx, recovery.Token()), f.actor.Login))
	_, err = f.store.Issue(f.ctx, f.actor.Login, unused)
	denied(t, err)
	_, err = f.store.Issue(f.ctx, f.actor.Login, unlock)
	denied(t, err)
	_, err = f.store.Issue(f.ctx, f.actor.Login, completed)
	must(t, err)
}

func TestNativeCompletionPreservesFreshnessDeadline(t *testing.T) {
	f := newFixture(t, true)
	unlock := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Unlock, Method: f.day.ref})
	f.offset.Store(int64(4 * time.Minute))
	completed, err := f.store.CompleteNative(f.ctx, f.actor.Login, unlock, "unlocked")
	must(t, err)
	f.offset.Store(int64(6 * time.Minute))
	_, err = f.store.Issue(f.ctx, f.actor.Login, completed)
	denied(t, err)
}

func TestNativeCompletionCannotUsePreRestartProofOrDurableLookup(t *testing.T) {
	f := newFixture(t, false)
	permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
	must(t, f.store.Close())
	restarted, err := New(f.ctx, f.db, Config{InstanceID: "instance"})
	must(t, err)
	defer restarted.Close()
	keys, err := workspacekeys.New(f.ctx, f.db, workspacekeys.Config{InstanceID: "instance", RPID: "deck.example", Origin: "https://deck.example", Preparation: restarted})
	must(t, err)
	must(t, restarted.BindRegistry(keys))
	_, err = restarted.CompleteNative(f.ctx, f.actor.Login, permit, "running")
	denied(t, err)
	pending, err := keys.PendingInitialization(f.ctx)
	must(t, err)
	_, err = restarted.CompleteNative(f.ctx, f.actor.Login, pending, "running")
	denied(t, err)
}

func TestNativeCompletionRacesRevocation(t *testing.T) {
	for _, mutation := range []string{"logout", "lock", "remove"} {
		t.Run(mutation, func(t *testing.T) {
			for range 5 {
				f := newFixture(t, true)
				unlock := f.prove(f.recovery, workspacekeys.Action{Operation: workspacekeys.Unlock, Method: f.recovery.ref})
				action := workspacekeys.Action{Operation: workspacekeys.Lock, Method: f.day.ref}
				if mutation == "remove" {
					action.Operation = workspacekeys.RemoveMethod
					action.Target = &f.recovery.ref
				}
				challenge, err := f.keys.BeginChallenge(f.ctx, f.actor, action)
				must(t, err)
				signature := sign(t, f.day.key, challenge.Challenge)
				var completed workspacekeys.AuthorizedOperation
				var completeErr, revokeErr error
				start := make(chan struct{})
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-start
					completed, completeErr = f.store.CompleteNative(f.ctx, f.actor.Login, unlock, "unlocked")
				}()
				go func() {
					defer wg.Done()
					<-start
					if mutation == "logout" {
						revokeErr = f.store.Logout(f.ctx, f.actor.Login)
					} else {
						_, revokeErr = f.keys.ConsumeProof(f.ctx, f.actor, challenge.ID, signature, nil)
					}
				}()
				close(start)
				wg.Wait()
				if completeErr != nil && revokeErr != nil {
					t.Fatalf("neither operation completed: %v; %v", completeErr, revokeErr)
				}
				if revokeErr == nil {
					_, err := f.store.Issue(f.ctx, f.actor.Login, completed)
					denied(t, err)
				} else {
					_, err := f.store.Issue(f.ctx, f.actor.Login, completed)
					must(t, err)
				}
			}
		})
	}
}
