package workspacekeys

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/noisefloor-oss/deck/internal/auth"
)

type preparation struct {
	ref MethodRef
	err error
}

func (p *preparation) RequirePreparationTx(context.Context, *sql.Tx, auth.Login, string, string) (MethodRef, error) {
	return p.ref, p.err
}

type fixture struct {
	t          *testing.T
	ctx        context.Context
	db         *sql.DB
	r          *Registry
	actor      Actor
	receipt    EnrollmentReceipt
	now        time.Time
	grant      *preparation
	commitment string
}

func newFixture(t *testing.T) *fixture {
	return newFixtureWithPolicy(t, "")
}

func newFixtureWithPolicy(t *testing.T, policy EncryptionPolicy) *fixture {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "public.db")+"?_txlock=immediate&_journal_mode=WAL&_synchronous=FULL")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &fixture{t: t, ctx: context.Background(), db: db, now: time.Now(), grant: &preparation{err: ErrDenied}, commitment: digest([]byte("public test commitment"))}
	f.r, err = New(f.ctx, db, Config{InstanceID: "instance", RPID: "deck.example", Origin: "https://deck.example", Now: func() time.Time { return f.now }, Preparation: f.grant, EncryptionPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	f.actor = NewActor(auth.Login{ID: strings.Repeat("01", 32), Issuer: "https://accounts.google.com", Subject: "test-owner", Email: "owner@example.test"}, "")
	tx, err := db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.r.CanEnrollTx(f.ctx, tx); err != nil {
		t.Fatal(err)
	}
	f.receipt, err = f.r.CreateEnrollmentTx(f.ctx, tx, f.actor.Login)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.actor = NewActor(f.actor.Login, f.receipt.Token())
	return f
}

type methodFixture struct {
	method   Method
	envelope Envelope
	key      *ecdsa.PrivateKey
	encoded  []byte
}

func (f *fixture) prepare(kind Kind) methodFixture {
	f.t.Helper()
	p, err := f.r.PrepareMethod(f.ctx, f.actor, kind, string(kind))
	if err != nil {
		f.t.Fatal(err)
	}
	return f.envelopeFor(p.Method)
}
func (f *fixture) envelopeFor(method Method) methodFixture {
	f.t.Helper()
	e, k := testEnvelope(f.t, method.Kind)
	e.MethodID = method.ID
	e.Revision = method.Revision
	e.Salt = method.Salt
	data, _ := json.Marshal(e)
	return methodFixture{method: method, envelope: e, key: k, encoded: data}
}
func (f *fixture) store(kind Kind) methodFixture {
	f.t.Helper()
	m := f.prepare(kind)
	if err := f.r.StoreEnvelope(f.ctx, f.actor, m.method.ID, m.encoded, nil, f.commitment); err != nil {
		f.t.Fatal(err)
	}
	return m
}
func sign(t *testing.T, key *ecdsa.PrivateKey, ch Challenge) string {
	t.Helper()
	b, err := decodeBytes(ch.Challenge, 32, 32)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	r, s, err := ecdsa.Sign(rand.Reader, key, h[:])
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 64)
	r.FillBytes(raw[:32])
	s.FillBytes(raw[32:])
	return base64.RawURLEncoding.EncodeToString(raw)
}
func (f *fixture) prove(m methodFixture, action Action) AuthorizedOperation {
	f.t.Helper()
	ch, err := f.r.BeginChallenge(f.ctx, f.actor, action)
	if err != nil {
		f.t.Fatal(err)
	}
	op, err := f.r.ConsumeProof(f.ctx, f.actor, ch.ID, sign(f.t, m.key, ch), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	if ch.OperationID == "" || ch.OperationID != op.ID {
		f.t.Fatal("consumed operation did not preserve the ID reserved before submission")
	}
	return op
}
func (f *fixture) verify(m methodFixture) {
	f.t.Helper()
	f.prove(m, Action{Operation: VerifyMethod, Method: m.method.MethodRef})
}
func (f *fixture) initialize() (methodFixture, methodFixture) {
	f.t.Helper()
	day, rec := f.store(Password), f.store(Recovery)
	f.verify(day)
	f.verify(rec)
	permit := f.prove(day, Action{Operation: Initialize, Method: day.method.MethodRef, Recovery: &rec.method.MethodRef})
	if err := f.r.FinalizeInitialization(f.ctx, permit); err != nil {
		f.t.Fatal(err)
	}
	f.grant.ref = day.method.MethodRef
	f.grant.err = nil
	return day, rec
}
func refused(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("operation unexpectedly authorized")
	}
}

func TestBootstrapNeedsIndependentReceipt(t *testing.T) {
	f := newFixture(t)
	forged := NewActor(f.actor.Login, "")
	_, err := f.r.PrepareMethod(f.ctx, forged, Password, "Password")
	refused(t, err)
	var storedHash string
	if err = f.db.QueryRow(`SELECT hash FROM workspacekeys_receipts`).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash == f.receipt.Token() {
		t.Fatal("raw receipt persisted")
	}
	_, err = f.r.PrepareMethod(f.ctx, NewActor(f.actor.Login, storedHash), Password, "Password")
	refused(t, err)
	otherLogin := f.actor.Login
	otherLogin.ID = strings.Repeat("02", 32)
	_, err = f.r.PrepareMethod(f.ctx, NewActor(otherLogin, f.receipt.Token()), Password, "Password")
	refused(t, err)
	otherOwner := f.actor.Login
	otherOwner.Subject = "different-subject"
	_, err = f.r.State(f.ctx, otherOwner)
	refused(t, err)
	sameOwner := f.actor.Login
	sameOwner.Email = "new-owner-email@example.test"
	matched, err := f.r.OwnerMatches(f.ctx, sameOwner)
	if err != nil || !matched {
		t.Fatal("stable owner incorrectly follows email", err)
	}
	m := f.prepare(Password)
	refused(t, f.r.StoreEnvelope(f.ctx, forged, m.method.ID, m.encoded, nil, f.commitment))
	if err = f.r.StoreEnvelope(f.ctx, f.actor, m.method.ID, m.encoded, nil, f.commitment); err != nil {
		t.Fatal(err)
	}
	_, err = f.r.BeginChallenge(f.ctx, forged, Action{Operation: VerifyMethod, Method: m.method.MethodRef})
	refused(t, err)
	ch, err := f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: VerifyMethod, Method: m.method.MethodRef})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.r.ConsumeProof(f.ctx, forged, ch.ID, sign(t, m.key, ch), nil)
	refused(t, err)
	_, err = f.r.ChallengeAction(f.ctx, forged, ch.ID)
	refused(t, err)
	if _, err = f.r.ConsumeProof(f.ctx, f.actor, ch.ID, sign(t, m.key, ch), nil); err != nil {
		t.Fatal(err)
	}
	tx, _ := f.db.BeginTx(f.ctx, nil)
	refused(t, f.r.CanEnrollTx(f.ctx, tx))
	_, err = f.r.CreateEnrollmentTx(f.ctx, tx, f.actor.Login)
	refused(t, err)
	tx.Rollback()
	encoded, _ := json.Marshal(f.receipt)
	if bytes.Contains(encoded, []byte(f.receipt.Token())) {
		t.Fatal("receipt serialized implicitly")
	}
	if strings.Contains(fmt.Sprint(f.actor), f.receipt.Token()) || strings.Contains(fmt.Sprint(f.receipt), f.receipt.Token()) || strings.Contains(fmt.Sprintf("%#v", f.actor), f.receipt.Token()) || strings.Contains(fmt.Sprintf("%#v", f.receipt), f.receipt.Token()) {
		t.Fatal("receipt formatted implicitly")
	}
}

func TestBootstrapProofAndAtomicInitialization(t *testing.T) {
	f := newFixture(t)
	day, rec := f.store(Password), f.store(Recovery)
	_, err := f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: Initialize, Method: day.method.MethodRef, Recovery: &rec.method.MethodRef})
	refused(t, err)
	_, err = f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: AuthorizeBrowser, Method: day.method.MethodRef})
	refused(t, err)
	f.verify(day)
	f.verify(rec)
	for _, m := range []methodFixture{day, rec} {
		stored, err := f.r.Method(f.ctx, f.actor, m.method.ID)
		if err != nil || stored.Status != "pending" || stored.VerifiedAt == nil {
			t.Fatal("verification activated a method", err)
		}
	}
	action := Action{Operation: Initialize, Method: day.method.MethodRef, Recovery: &rec.method.MethodRef}
	ch, err := f.r.BeginChallenge(f.ctx, f.actor, action)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := f.r.ChallengeAction(f.ctx, f.actor, ch.ID)
	if err != nil || observed.Operation != Initialize {
		t.Fatal(err)
	}
	permit, err := f.r.ConsumeProof(f.ctx, f.actor, ch.ID, sign(t, day.key, ch), nil)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := f.r.State(f.ctx, f.actor.Login)
	if state.Initialized || state.InitializingOperation != permit.ID {
		t.Fatal("proof claimed native success")
	}
	refused(t, f.r.FinalizeInitialization(f.ctx, AuthorizedOperation{ID: permit.ID}))
	if err = f.r.FinalizeInitialization(f.ctx, permit); err != nil {
		t.Fatal(err)
	}
	refused(t, f.r.FinalizeInitialization(f.ctx, permit))
	state, _ = f.r.State(f.ctx, f.actor.Login)
	if !state.Initialized || !state.BootstrapClosed || state.InitializingOperation != "" {
		t.Fatal("initialization not committed")
	}
	for _, m := range []methodFixture{day, rec} {
		stored, _ := f.r.Method(f.ctx, f.actor, m.method.ID)
		if stored.Status != "active" {
			t.Fatal("initialization not atomic")
		}
	}
	var count int
	f.db.QueryRow(`SELECT count(*) FROM workspacekeys_receipts`).Scan(&count)
	if count != 0 {
		t.Fatal("bootstrap receipts survived")
	}
	_, err = f.r.PrepareMethod(f.ctx, f.actor, Password, "Another password")
	refused(t, err)
	f.grant.ref = day.method.MethodRef
	f.grant.err = nil
	if _, err = f.r.PrepareMethod(f.ctx, f.actor, Password, "Another password"); err != nil {
		t.Fatal(err)
	}
	f.r.cfg.Preparation = nil
	_, err = f.r.PrepareMethod(f.ctx, f.actor, Password, "Default deny")
	refused(t, err)
}

func TestResumeRequiresPersistedProofAndRevokesOldReceipt(t *testing.T) {
	f := newFixture(t)
	m := f.store(Password)
	oldActor := f.actor
	otherLogin := f.actor.Login
	otherLogin.ID = strings.Repeat("02", 32)
	f.actor = NewActor(otherLogin, "")
	f.now = f.now.Add(13 * time.Hour)
	ch, err := f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: ResumeEnrollment, Method: m.method.MethodRef})
	if err != nil {
		t.Fatal(err)
	}
	_, wrongKey := testEnvelope(t, Password)
	_, err = f.r.ConsumeProof(f.ctx, f.actor, ch.ID, sign(t, wrongKey, ch), nil)
	refused(t, err)
	second, err := f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: ResumeEnrollment, Method: m.method.MethodRef})
	if err != nil {
		t.Fatal(err)
	}
	op, err := f.r.ConsumeProof(f.ctx, f.actor, ch.ID, sign(t, m.key, ch), nil)
	if err != nil || op.Receipt == nil || op.Receipt.Revision != 2 {
		t.Fatal("resume failed", err)
	}
	_, err = f.r.ConsumeProof(f.ctx, f.actor, second.ID, sign(t, m.key, second), nil)
	refused(t, err)
	f.actor = NewActor(otherLogin, op.Receipt.Token())
	if _, err = f.r.PrepareMethod(f.ctx, f.actor, Recovery, "Recovery"); err != nil {
		t.Fatal(err)
	}
	_, err = f.r.PrepareMethod(f.ctx, oldActor, Password, "Old receipt")
	refused(t, err)
	if err = f.r.RevokeEnrollment(f.ctx, otherLogin); err != nil {
		t.Fatal(err)
	}
	_, err = f.r.PrepareMethod(f.ctx, f.actor, Recovery, "Logged out")
	refused(t, err)
}

func TestChallengesBindLoginPayloadEpochAndSingleUse(t *testing.T) {
	for _, mode := range []string{"login", "owner", "expiry", "epoch", "payload", "revision", "envelope"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			m := f.store(Password)
			ch, err := f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: VerifyMethod, Method: m.method.MethodRef})
			if err != nil {
				t.Fatal(err)
			}
			actor := f.actor
			switch mode {
			case "login":
				actor.Login.ID = strings.Repeat("04", 32)
			case "owner":
				actor.Login.Subject = "someone-else"
			case "expiry":
				f.now = f.now.Add(6 * time.Minute)
			case "epoch":
				if err = f.r.SetRuntimeEpoch(f.ctx, "changed_epoch"); err != nil {
					t.Fatal(err)
				}
			case "payload":
				_, err = f.db.Exec(`UPDATE workspacekeys_challenges SET action=?`, []byte(`{"operation":"unlock"}`))
			case "revision":
				_, err = f.db.Exec(`UPDATE workspacekeys_methods SET revision=2`)
			case "envelope":
				_, err = f.db.Exec(`UPDATE workspacekeys_methods SET envelope_digest=?`, digest([]byte("different candidate")))
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.r.ConsumeProof(f.ctx, actor, ch.ID, sign(t, m.key, ch), nil)
			refused(t, err)
		})
	}
	f := newFixture(t)
	m := f.store(Password)
	ch, err := f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: VerifyMethod, Method: m.method.MethodRef})
	if err != nil {
		t.Fatal(err)
	}
	sig := sign(t, m.key, ch)
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.r.ConsumeProof(f.ctx, f.actor, ch.ID, sig, nil); out <- err }()
	}
	wg.Wait()
	close(out)
	successes := 0
	for err := range out {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("challenge consumed %d times", successes)
	}
}

func TestMethodReplacementAndRevocation(t *testing.T) {
	f := newFixture(t)
	day, rec := f.initialize()
	removal := Action{Operation: RemoveMethod, Method: rec.method.MethodRef, Target: &day.method.MethodRef}
	ch, err := f.r.BeginChallenge(f.ctx, f.actor, removal)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.r.ConsumeProof(f.ctx, f.actor, ch.ID, sign(t, rec.key, ch), nil)
	refused(t, err)
	oldPending, err := f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: AuthorizeBrowser, Method: day.method.MethodRef})
	if err != nil {
		t.Fatal(err)
	}
	candidate := f.store(Password)
	_, err = f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: AddMethod, Method: day.method.MethodRef, Target: &candidate.method.MethodRef})
	refused(t, err)
	f.verify(candidate)
	replacement := Action{Operation: ReplaceMethod, Method: day.method.MethodRef, Target: &candidate.method.MethodRef, Replacement: &day.method.MethodRef}
	f.prove(day, replacement)
	_, err = f.r.Method(f.ctx, f.actor, day.method.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("old method still visible", err)
	}
	_, err = f.r.ConsumeProof(f.ctx, f.actor, oldPending.ID, sign(t, day.key, oldPending), nil)
	refused(t, err)
	_, err = f.r.PrepareMethod(f.ctx, f.actor, Password, "Stale grant")
	refused(t, err)
	f.grant.ref = candidate.method.MethodRef
	// Recovery remains usable across independent challenges.
	for range 2 {
		f.prove(rec, Action{Operation: AuthorizeBrowser, Method: rec.method.MethodRef})
	}
	f.prove(candidate, Action{Operation: RenameMethod, Method: candidate.method.MethodRef, Target: &rec.method.MethodRef, Label: "Printed recovery key"})
	stored, err := f.r.Method(f.ctx, f.actor, rec.method.ID)
	if err != nil || stored.Label != "Printed recovery key" {
		t.Fatal(err)
	}
}

func TestEnvelopeUploadIsImmutableAndCommitmentBound(t *testing.T) {
	f := newFixture(t)
	m := f.store(Password)
	if err := f.r.StoreEnvelope(f.ctx, f.actor, m.method.ID, m.encoded, nil, f.commitment); err != nil {
		t.Fatal("exact retry failed", err)
	}
	changed := m.envelope
	changed.Nonce = randomToken()
	data, _ := json.Marshal(changed)
	refused(t, f.r.StoreEnvelope(f.ctx, f.actor, m.method.ID, data, nil, f.commitment))
	refused(t, f.r.StoreEnvelope(f.ctx, f.actor, m.method.ID, m.encoded, nil, digest([]byte("new volume"))))
	other := f.prepare(Recovery)
	other.envelope.ProofPublicKey = m.envelope.ProofPublicKey
	other.encoded, _ = json.Marshal(other.envelope)
	refused(t, f.r.StoreEnvelope(f.ctx, f.actor, other.method.ID, other.encoded, nil, f.commitment))
	other = f.prepare(Recovery)
	other.envelope.Salt = randomToken()
	other.encoded, _ = json.Marshal(other.envelope)
	refused(t, f.r.StoreEnvelope(f.ctx, f.actor, other.method.ID, other.encoded, nil, f.commitment))
	other = f.prepare(Recovery)
	other.envelope.Revision++
	other.encoded, _ = json.Marshal(other.envelope)
	refused(t, f.r.StoreEnvelope(f.ctx, f.actor, other.method.ID, other.encoded, nil, f.commitment))
}

func TestPendingLabelPreservesTestedEnvelopeAndPreparationAuthority(t *testing.T) {
	f := newFixture(t)
	day, rec := f.store(Password), f.store(Recovery)
	f.verify(day)
	f.verify(rec)
	before, err := f.r.Method(f.ctx, f.actor, day.method.ID)
	if err != nil {
		t.Fatal(err)
	}
	refused(t, f.r.LabelPending(f.ctx, NewActor(f.actor.Login, ""), day.method.ID, "Unauthorized"))
	refused(t, f.r.LabelPending(f.ctx, f.actor, day.method.ID, "bad\nlabel"))
	if err := f.r.LabelPending(f.ctx, f.actor, day.method.ID, "My password"); err != nil {
		t.Fatal(err)
	}
	after, err := f.r.Method(f.ctx, f.actor, day.method.ID)
	if err != nil || after.Label != "My password" || after.MethodRef != before.MethodRef || after.EnvelopeDigest != before.EnvelopeDigest || after.VerifiedAt == nil || !after.VerifiedAt.Equal(*before.VerifiedAt) {
		t.Fatalf("label change altered tested method: %v", err)
	}
	permit := f.prove(day, Action{Operation: Initialize, Method: day.method.MethodRef, Recovery: &rec.method.MethodRef})
	refused(t, f.r.LabelPending(f.ctx, f.actor, day.method.ID, "Changed during initialization"))
	if err := f.r.FinalizeInitialization(f.ctx, permit); err != nil {
		t.Fatal(err)
	}
	f.grant.ref, f.grant.err = day.method.MethodRef, nil
	refused(t, f.r.LabelPending(f.ctx, f.actor, day.method.ID, "Active method requires proof"))
	candidate := f.store(Password)
	if err := f.r.LabelPending(f.ctx, f.actor, candidate.method.ID, "Candidate"); err != nil {
		t.Fatal(err)
	}
	f.grant.err = ErrDenied
	refused(t, f.r.LabelPending(f.ctx, f.actor, candidate.method.ID, "Expired grant"))
}

func TestRestartPreservesEnrollmentButDiscardsChallenges(t *testing.T) {
	f := newFixture(t)
	m := f.store(Password)
	ch, err := f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: VerifyMethod, Method: m.method.MethodRef})
	if err != nil {
		t.Fatal(err)
	}
	f.r, err = New(f.ctx, f.db, f.r.cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.r.ConsumeProof(f.ctx, f.actor, ch.ID, sign(t, m.key, ch), nil)
	refused(t, err)
	f.verify(m)
	if !f.r.CheckConfiguration("instance", "deck.example", "https://deck.example") || f.r.CheckConfiguration("instance", "evil.example", "https://evil.example") {
		t.Fatal("configuration binding failed")
	}
	config := f.r.cfg
	config.RPID = "evil.example"
	config.Origin = "https://evil.example"
	_, err = New(f.ctx, f.db, config)
	refused(t, err)
}

func TestInterruptedInitializationResumesPinnedIntent(t *testing.T) {
	f := newFixture(t)
	day, rec := f.store(Password), f.store(Recovery)
	other := f.store(Password)
	f.verify(day)
	f.verify(rec)
	f.verify(other)
	original := f.prove(day, Action{Operation: Initialize, Method: day.method.MethodRef, Recovery: &rec.method.MethodRef})
	var err error
	f.r, err = New(f.ctx, f.db, f.r.cfg)
	if err != nil {
		t.Fatal(err)
	}
	refused(t, f.r.FinalizeInitialization(f.ctx, original))
	pending, err := f.r.PendingInitialization(f.ctx)
	if err != nil || pending.ID != original.ID {
		t.Fatal("lost durable intent", err)
	}
	if err = f.r.SetRuntimeEpoch(f.ctx, "after_restart"); err != nil {
		t.Fatal(err)
	}
	refused(t, f.r.FinalizeInitialization(f.ctx, pending))
	f.now = f.now.Add(13 * time.Hour)
	login := f.actor.Login
	login.ID = strings.Repeat("06", 32)
	f.actor = NewActor(login, "")
	_, err = f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: ResumeInitialization, Method: other.method.MethodRef})
	refused(t, err)
	_, err = f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: ResumeEnrollment, Method: day.method.MethodRef})
	refused(t, err)
	resumed := f.prove(day, Action{Operation: ResumeInitialization, Method: day.method.MethodRef})
	if resumed.ID != original.ID || resumed.Action.Operation != Initialize || resumed.Action.Recovery == nil || *resumed.Action.Recovery != rec.method.MethodRef || resumed.RuntimeEpoch != "after_restart" {
		t.Fatal("resume changed native intent")
	}
	refused(t, f.r.FinalizeInitialization(f.ctx, pending))
	if err = f.r.FinalizeInitialization(f.ctx, resumed); err != nil {
		t.Fatal(err)
	}
	state, _ := f.r.State(f.ctx, f.actor.Login)
	if !state.Initialized || state.Commitment != f.commitment {
		t.Fatal("resumed intent not finalized")
	}
	_, err = f.r.PendingInitialization(f.ctx)
	refused(t, err)
	_, err = f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: ResumeInitialization, Method: day.method.MethodRef})
	refused(t, err)
}

func TestCandidateDigestAndPreparedPayloadCannotBeSubstituted(t *testing.T) {
	f := newFixture(t)
	day, _ := f.initialize()
	candidate := f.store(Password)
	other := f.store(Password)
	f.verify(candidate)
	f.verify(other)
	target := candidate.method.MethodRef
	action := Action{Operation: AddMethod, Method: day.method.MethodRef, Target: &target}
	ch, err := f.r.BeginChallenge(f.ctx, f.actor, action)
	if err != nil {
		t.Fatal(err)
	}
	target = other.method.MethodRef
	stored, err := f.r.ChallengeAction(f.ctx, f.actor, ch.ID)
	if err != nil || stored.Target == nil || *stored.Target != candidate.method.MethodRef {
		t.Fatal("caller pointer changed stored decision", err)
	}
	if _, err = f.db.Exec(`UPDATE workspacekeys_methods SET envelope_digest=? WHERE id=?`, digest([]byte("changed candidate")), candidate.method.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.r.ConsumeProof(f.ctx, f.actor, ch.ID, sign(t, day.key, ch), nil)
	refused(t, err)
	untouched, err := f.r.Method(f.ctx, f.actor, other.method.ID)
	if err != nil || untouched.Status != "pending" {
		t.Fatal("substitute candidate activated", err)
	}
}

func TestReceiptExpiryIsRecheckedAtConsumption(t *testing.T) {
	f := newFixture(t)
	m := f.store(Password)
	f.now = f.receipt.ExpiresAt.Add(-time.Minute)
	ch, err := f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: VerifyMethod, Method: m.method.MethodRef})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.receipt.ExpiresAt
	_, err = f.r.ConsumeProof(f.ctx, f.actor, ch.ID, sign(t, m.key, ch), nil)
	refused(t, err)
}
