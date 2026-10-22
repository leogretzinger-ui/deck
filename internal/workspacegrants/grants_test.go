package workspacegrants

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
)

type fixture struct {
	t             *testing.T
	ctx           context.Context
	db            *sql.DB
	store         *Store
	keys          *workspacekeys.Registry
	actor         workspacekeys.Actor
	offset        atomic.Int64
	day, recovery method
	commitment    string
	init          workspacekeys.AuthorizedOperation
}
type method struct {
	ref workspacekeys.MethodRef
	key *ecdsa.PrivateKey
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func denied(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("unexpected authorization")
	}
}
func newFixture(t *testing.T, initialize bool) *fixture {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "control.db")+"?_txlock=immediate&_journal_mode=WAL&_synchronous=FULL")
	must(t, err)
	f := &fixture{t: t, ctx: context.Background(), db: db, commitment: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	now := func() time.Time { return time.Now().Add(time.Duration(f.offset.Load())) }
	f.store, err = New(f.ctx, db, Config{InstanceID: "instance", Now: now})
	must(t, err)
	f.keys, err = workspacekeys.New(f.ctx, db, workspacekeys.Config{InstanceID: "instance", RPID: "deck.example", Origin: "https://deck.example", Now: now, Preparation: f.store})
	must(t, err)
	must(t, f.store.BindRegistry(f.keys))
	t.Cleanup(func() { _ = f.store.Close(); _ = db.Close() })
	login := auth.Login{ID: strings.Repeat("01", 32), Issuer: "https://accounts.google.com", Subject: "owner", Email: "owner@example.test"}
	tx, err := db.BeginTx(f.ctx, nil)
	must(t, err)
	receipt, err := f.keys.CreateEnrollmentTx(f.ctx, tx, login)
	must(t, err)
	must(t, tx.Commit())
	f.actor = workspacekeys.NewActor(login, receipt.Token())
	f.day = f.add(workspacekeys.Password)
	f.recovery = f.add(workspacekeys.Recovery)
	f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.VerifyMethod, Method: f.day.ref})
	f.prove(f.recovery, workspacekeys.Action{Operation: workspacekeys.VerifyMethod, Method: f.recovery.ref})
	if initialize {
		f.init = f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
		must(t, f.keys.FinalizeInitialization(f.ctx, f.init))
	}
	return f
}
func (f *fixture) add(kind workspacekeys.Kind) method {
	f.t.Helper()
	p, err := f.keys.PrepareMethod(f.ctx, f.actor, kind, string(kind))
	must(f.t, err)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(f.t, err)
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	must(f.t, err)
	envelope := workspacekeys.Envelope{Version: 1, InstanceID: "instance", MethodID: p.Method.ID, Revision: p.Method.Revision, Kind: kind, Salt: p.Method.Salt, Nonce: base64.RawURLEncoding.EncodeToString(make([]byte, 12)), Ciphertext: base64.RawURLEncoding.EncodeToString(make([]byte, 64)), ProofPublicKey: base64.RawURLEncoding.EncodeToString(public)}
	if kind == workspacekeys.Password {
		iterations := uint32(600000)
		envelope.Iterations = &iterations
	}
	raw, err := json.Marshal(envelope)
	must(f.t, err)
	must(f.t, f.keys.StoreEnvelope(f.ctx, f.actor, p.Method.ID, raw, nil, f.commitment))
	return method{ref: p.Method.MethodRef, key: key}
}
func sign(t *testing.T, key *ecdsa.PrivateKey, challenge string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(challenge)
	must(t, err)
	hash := sha256.Sum256(raw)
	r, s, err := ecdsa.Sign(rand.Reader, key, hash[:])
	must(t, err)
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return base64.RawURLEncoding.EncodeToString(signature)
}
func (f *fixture) prove(m method, action workspacekeys.Action) workspacekeys.AuthorizedOperation {
	f.t.Helper()
	ch, err := f.keys.BeginChallenge(f.ctx, f.actor, action)
	must(f.t, err)
	op, err := f.keys.ConsumeProof(f.ctx, f.actor, ch.ID, sign(f.t, m.key, ch.Challenge), nil)
	must(f.t, err)
	return op
}
func (f *fixture) grant(m method) Delivery {
	f.t.Helper()
	op := f.prove(m, workspacekeys.Action{Operation: workspacekeys.AuthorizeBrowser, Method: m.ref})
	delivery, err := f.store.Issue(f.ctx, f.actor.Login, op)
	must(f.t, err)
	return delivery
}
func browser(login auth.Login, id string) auth.Login { login.ID = strings.Repeat(id, 32); return login }
func attach(t *testing.T, f *fixture, login auth.Login, delivery Delivery) (*atomic.Int32, func()) {
	t.Helper()
	closed := &atomic.Int32{}
	release, err := f.store.Attach(WithToken(f.ctx, delivery.Token()), login, func() { closed.Add(1) })
	must(t, err)
	return closed, release
}

func TestProofBoundOneUseIssuanceAndSecretBoundary(t *testing.T) {
	f := newFixture(t, true)
	login := f.actor.Login
	// A caller with every unencrypted record and a forged Google identity still
	// lacks both the browser bearer and the process-local fresh-proof receipt.
	fake := workspacekeys.AuthorizedOperation{ID: f.init.ID, Action: f.init.Action, Payload: f.init.Payload, RuntimeEpoch: f.init.RuntimeEpoch}
	_, err := f.store.Issue(f.ctx, login, fake)
	denied(t, err)
	d, err := f.store.Issue(f.ctx, login, f.init)
	must(t, err)
	if len(d.Token()) != 43 || d.ExpiresAt.Sub(f.store.cfg.Now()) > Lifetime {
		t.Fatal("invalid bearer lifetime or entropy")
	}
	must(t, f.store.Require(WithToken(f.ctx, d.Token()), login))
	denied(t, f.store.Require(f.ctx, login))
	var persisted string
	must(t, f.db.QueryRow(`SELECT hash FROM workspacegrants_grants`).Scan(&persisted))
	if persisted == d.Token() {
		t.Fatal("raw bearer persisted")
	}
	denied(t, f.store.Require(WithToken(f.ctx, persisted), login))
	_, err = f.store.Issue(f.ctx, login, f.init)
	denied(t, err)
	if _, err = json.Marshal(d); err == nil {
		t.Fatal("secret delivery serialized")
	}
	for _, formatted := range []string{fmt.Sprint(d), fmt.Sprintf("%#v", d), fmt.Sprint(WithToken(f.ctx, d.Token()))} {
		if strings.Contains(formatted, d.Token()) {
			t.Fatal("secret formatted")
		}
	}
	cookie := d.Cookie()
	if cookie.Name != CookieName || cookie.Domain != "" || cookie.Path != "/" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("cookie protection missing")
	}
	// Public operation fields cannot substitute a different known operation.
	op := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.AuthorizeBrowser, Method: f.day.ref})
	changed := op
	changed.ID = f.init.ID
	_, err = f.store.Issue(f.ctx, login, changed)
	denied(t, err)
	changed = op
	changed.Action.Method = f.recovery.ref
	_, err = f.store.Issue(f.ctx, login, changed)
	denied(t, err)
	changed = op
	changed.Payload = []byte(`{}`)
	_, err = f.store.Issue(f.ctx, login, changed)
	denied(t, err)
	_, err = f.store.Issue(f.ctx, browser(login, "02"), op)
	denied(t, err)
	otherOwner := login
	otherOwner.Subject = "someone-else"
	_, err = f.store.Issue(f.ctx, otherOwner, op)
	denied(t, err)
	_, err = f.store.Issue(f.ctx, login, op)
	must(t, err)
}

func TestPendingAndUnconfirmedNativeOperationsCannotGrant(t *testing.T) {
	f := newFixture(t, false)
	permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
	_, err := f.store.Issue(f.ctx, f.actor.Login, permit)
	denied(t, err)
	observed, err := f.keys.PendingInitialization(f.ctx)
	must(t, err)
	must(t, f.keys.FinalizeInitialization(f.ctx, observed))
	_, err = f.store.Issue(f.ctx, f.actor.Login, observed)
	denied(t, err)
	_, err = f.store.Issue(f.ctx, f.actor.Login, permit)
	must(t, err)
	unlock := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Unlock, Method: f.day.ref})
	_, err = f.store.Issue(f.ctx, f.actor.Login, unlock)
	denied(t, err)
	must(t, f.keys.ConfirmUnlock(f.ctx, f.actor.Login, unlock))
	_, err = f.store.Issue(f.ctx, f.actor.Login, unlock)
	must(t, err)
	denied(t, f.keys.ConfirmUnlock(f.ctx, f.actor.Login, unlock))
	rename := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.RenameMethod, Method: f.day.ref, Target: &f.day.ref, Label: "Password"})
	_, err = f.store.Issue(f.ctx, f.actor.Login, rename)
	denied(t, err)
}

func TestTwoBrowsersLogoutClosesOnlyItsStreams(t *testing.T) {
	f := newFixture(t, true)
	first := f.actor.Login
	firstDelivery := f.grant(f.day)
	firstClosed, firstRelease := attach(t, f, first, firstDelivery)
	defer firstRelease()
	f.actor = workspacekeys.NewActor(browser(first, "02"), "")
	second := f.actor.Login
	secondDelivery := f.grant(f.day)
	secondClosed, secondRelease := attach(t, f, second, secondDelivery)
	defer secondRelease()
	denied(t, f.store.Require(WithToken(f.ctx, firstDelivery.Token()), second))
	pending := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.AuthorizeBrowser, Method: f.day.ref})
	must(t, f.store.Logout(f.ctx, first))
	if firstClosed.Load() != 1 || secondClosed.Load() != 0 {
		t.Fatal("logout closed wrong browser")
	}
	denied(t, f.store.Require(WithToken(f.ctx, firstDelivery.Token()), first))
	must(t, f.store.Require(WithToken(f.ctx, secondDelivery.Token()), second))
	must(t, f.store.Logout(f.ctx, second))
	_, err := f.store.Issue(f.ctx, second, pending)
	denied(t, err)
	if secondClosed.Load() != 1 {
		t.Fatal("second stream not closed")
	}
	firstRelease()
	secondRelease()
	if firstClosed.Load() != 1 || secondClosed.Load() != 1 {
		t.Fatal("release repeated transport closure")
	}
}

func TestMethodRemovalAndEpochCloseExistingStreams(t *testing.T) {
	for _, event := range []string{"remove", "epoch", "lock"} {
		t.Run(event, func(t *testing.T) {
			f := newFixture(t, true)
			login := f.actor.Login
			dayDelivery := f.grant(f.day)
			dayClosed, releaseDay := attach(t, f, login, dayDelivery)
			defer releaseDay()
			f.actor = workspacekeys.NewActor(browser(login, "02"), "")
			recoveryDelivery := f.grant(f.recovery)
			recoveryClosed, releaseRecovery := attach(t, f, f.actor.Login, recoveryDelivery)
			defer releaseRecovery()
			unused := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.AuthorizeBrowser, Method: f.day.ref})
			switch event {
			case "remove":
				f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.RemoveMethod, Method: f.day.ref, Target: &f.recovery.ref})
			case "epoch":
				must(t, f.keys.SetRuntimeEpoch(f.ctx, "new_runtime"))
			case "lock":
				f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Lock, Method: f.day.ref})
			}
			if recoveryClosed.Load() != 1 {
				t.Fatal("revocation returned before stream closed")
			}
			denied(t, f.store.Require(WithToken(f.ctx, recoveryDelivery.Token()), f.actor.Login))
			if event == "remove" {
				if dayClosed.Load() != 0 {
					t.Fatal("removed different method's stream")
				}
				must(t, f.store.Require(WithToken(f.ctx, dayDelivery.Token()), login))
			} else {
				if dayClosed.Load() != 1 {
					t.Fatal("global revocation left stream open")
				}
				denied(t, f.store.Require(WithToken(f.ctx, dayDelivery.Token()), login))
				_, err := f.store.Issue(f.ctx, f.actor.Login, unused)
				denied(t, err)
			}
		})
	}
}

func TestExpiryClosesIdleStreamWithoutAnotherRequest(t *testing.T) {
	f := newFixture(t, true)
	d := f.grant(f.day)
	// Move the test clock near the fixed expiry while keeping real elapsed time.
	f.offset.Store(int64(time.Until(d.ExpiresAt) - 100*time.Millisecond))
	closed := make(chan struct{})
	release, err := f.store.Attach(WithToken(f.ctx, d.Token()), f.actor.Login, func() { close(closed) })
	must(t, err)
	defer release()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("idle stream survived absolute expiry")
	}
	denied(t, f.store.Require(WithToken(f.ctx, d.Token()), f.actor.Login))
}

func TestSessionRenewalDoesNotExtendGrantAndPreparationRechecksIt(t *testing.T) {
	f := newFixture(t, true)
	d := f.grant(f.day)
	ctx := WithToken(f.ctx, d.Token())
	f.ctx = ctx
	renewed := f.actor.Login
	renewed.Email = "renamed@example.test"
	must(t, f.store.Require(ctx, renewed))
	var expiry int64
	must(t, f.db.QueryRow(`SELECT expires FROM workspacegrants_grants`).Scan(&expiry))
	if expiry != d.ExpiresAt.UnixMilli() {
		t.Fatal("login renewal extended grant")
	}
	f.add(workspacekeys.Password)
	f.ctx = context.Background()
	_, err := f.keys.PrepareMethod(f.ctx, f.actor, workspacekeys.Password, "No cookie")
	denied(t, err)
	f.offset.Store(int64(13 * time.Hour))
	must(t, f.store.Sweep(f.ctx))
	denied(t, f.store.Require(ctx, renewed))
	_, err = f.keys.PrepareMethod(ctx, f.actor, workspacekeys.Password, "Expired")
	denied(t, err)
}

func TestCookieContextRejectsAmbiguousAndMalformedBearers(t *testing.T) {
	f := newFixture(t, true)
	d := f.grant(f.day)
	handler := CookieContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.store.Require(r.Context(), f.actor.Login) != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, test := range []struct {
		cookies []string
		status  int
	}{{[]string{d.Token()}, 204}, {nil, 401}, {[]string{d.Token(), d.Token()}, 401}, {[]string{d.Token() + "="}, 401}, {[]string{strings.Repeat("a", 43)}, 401}} {
		request := httptest.NewRequest(http.MethodGet, "https://deck.example/", nil)
		for _, token := range test.cookies {
			request.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("status %d want %d", response.Code, test.status)
		}
	}
}

func TestRestartDiscardsGrantsAndProofReceipts(t *testing.T) {
	f := newFixture(t, true)
	d := f.grant(f.day)
	closed, release := attach(t, f, f.actor.Login, d)
	defer release()
	unused := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.AuthorizeBrowser, Method: f.day.ref})
	must(t, f.store.Close())
	if closed.Load() != 1 {
		t.Fatal("shutdown left stream open")
	}
	var err error
	f.store, err = New(f.ctx, f.db, f.store.cfg)
	must(t, err)
	f.keys, err = workspacekeys.New(f.ctx, f.db, workspacekeys.Config{InstanceID: "instance", RPID: "deck.example", Origin: "https://deck.example", Preparation: f.store})
	must(t, err)
	must(t, f.store.BindRegistry(f.keys))
	denied(t, f.store.Require(WithToken(f.ctx, d.Token()), f.actor.Login))
	_, err = f.store.Issue(f.ctx, f.actor.Login, unused)
	denied(t, err)
	newer := f.grant(f.day)
	must(t, f.store.Require(WithToken(f.ctx, newer.Token()), f.actor.Login))
}

func TestConcurrentProofClaimAndAttachRevocation(t *testing.T) {
	f := newFixture(t, true)
	op := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.AuthorizeBrowser, Method: f.day.ref})
	results := make(chan Delivery, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := f.store.Issue(f.ctx, f.actor.Login, op)
			if err == nil {
				results <- d
			}
		}()
	}
	wg.Wait()
	close(results)
	var d Delivery
	count := 0
	for item := range results {
		d = item
		count++
	}
	if count != 1 {
		t.Fatalf("operation issued %d grants", count)
	}
	var admitted, closed atomic.Int32
	start := make(chan struct{})
	errors := make(chan error, 1)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := f.store.Attach(WithToken(f.ctx, d.Token()), f.actor.Login, func() { closed.Add(1) }); err == nil {
				admitted.Add(1)
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); <-start; errors <- f.store.Logout(f.ctx, f.actor.Login) }()
	close(start)
	wg.Wait()
	must(t, <-errors)
	if admitted.Load() != closed.Load() {
		t.Fatalf("%d admitted but %d closed", admitted.Load(), closed.Load())
	}
	denied(t, f.store.Require(WithToken(f.ctx, d.Token()), f.actor.Login))
}

func TestResumeInitializationGrantsOnlyFreshRecipient(t *testing.T) {
	f := newFixture(t, false)
	first := f.actor.Login
	original := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
	must(t, f.keys.SetRuntimeEpoch(f.ctx, "resume_epoch"))
	f.actor = workspacekeys.NewActor(browser(first, "02"), "")
	resumed := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.ResumeInitialization, Method: f.day.ref})
	resumed, err := f.store.CompleteNative(f.ctx, f.actor.Login, resumed, "resumed_running")
	must(t, err)
	_, err = f.store.Issue(f.ctx, first, original)
	denied(t, err)
	_, err = f.store.Issue(f.ctx, first, resumed)
	denied(t, err)
	d, err := f.store.Issue(f.ctx, f.actor.Login, resumed)
	must(t, err)
	must(t, f.store.Require(WithToken(f.ctx, d.Token()), f.actor.Login))
	denied(t, f.store.Require(WithToken(f.ctx, d.Token()), first))
}

func TestSnapshotAndHashCannotRecreateBrowserGrant(t *testing.T) {
	f := newFixture(t, true)
	d := f.grant(f.day)
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	_, err := f.db.Exec(`VACUUM INTO ?`, copyPath)
	must(t, err)
	db, err := sql.Open("sqlite3", copyPath)
	must(t, err)
	defer db.Close()
	var hash string
	must(t, db.QueryRow(`SELECT hash FROM workspacegrants_grants`).Scan(&hash))
	if bytes.Equal([]byte(hash), []byte(d.Token())) {
		t.Fatal("copy contained raw bearer")
	}
	copied, err := New(f.ctx, db, Config{InstanceID: "instance"})
	must(t, err)
	defer copied.Close()
	keys, err := workspacekeys.New(f.ctx, db, workspacekeys.Config{InstanceID: "instance", RPID: "deck.example", Origin: "https://deck.example", Preparation: copied})
	must(t, err)
	must(t, copied.BindRegistry(keys))
	denied(t, copied.Require(WithToken(f.ctx, hash), f.actor.Login))
	denied(t, copied.Require(WithToken(f.ctx, d.Token()), f.actor.Login))
	fake := workspacekeys.AuthorizedOperation{ID: f.init.ID, Action: f.init.Action, Payload: f.init.Payload, RuntimeEpoch: f.init.RuntimeEpoch}
	_, err = copied.Issue(f.ctx, f.actor.Login, fake)
	denied(t, err)
}

func TestRevocationWriteFailureClosesStreamsAndFailsClosed(t *testing.T) {
	for _, event := range []string{"logout", "lock"} {
		t.Run(event, func(t *testing.T) {
			f := newFixture(t, true)
			d := f.grant(f.day)
			closed, release := attach(t, f, f.actor.Login, d)
			defer release()
			_, err := f.db.Exec(`CREATE TRIGGER refuse_grant_delete BEFORE DELETE ON workspacegrants_grants BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END`)
			must(t, err)
			if event == "logout" {
				denied(t, f.store.Logout(f.ctx, f.actor.Login))
			} else {
				f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Lock, Method: f.day.ref})
			}
			if closed.Load() != 1 {
				t.Fatal("failed revocation left stream open")
			}
			denied(t, f.store.Require(WithToken(f.ctx, d.Token()), f.actor.Login))
			var count int
			must(t, f.db.QueryRow(`SELECT count(*) FROM workspacegrants_grants`).Scan(&count))
			if count != 1 {
				t.Fatal("fault fixture did not retain grant")
			}
			op := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.AuthorizeBrowser, Method: f.day.ref})
			_, err = f.store.Issue(f.ctx, f.actor.Login, op)
			denied(t, err)
		})
	}
}

func TestFreshProofReceiptHasItsOwnExpiry(t *testing.T) {
	f := newFixture(t, true)
	d := f.grant(f.day)
	unused := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.AuthorizeBrowser, Method: f.day.ref})
	f.offset.Store(int64(6 * time.Minute))
	_, err := f.store.Issue(f.ctx, f.actor.Login, unused)
	denied(t, err)
	must(t, f.store.Require(WithToken(f.ctx, d.Token()), f.actor.Login))
}

func TestReleaseAndReauthorizationCloseExactlyOnce(t *testing.T) {
	f := newFixture(t, true)
	d := f.grant(f.day)
	closed, release := attach(t, f, f.actor.Login, d)
	release()
	release()
	replacement := f.grant(f.day)
	if closed.Load() != 0 {
		t.Fatal("ordinary detach closed transport")
	}
	secondClosed, secondRelease := attach(t, f, f.actor.Login, replacement)
	defer secondRelease()
	_ = f.grant(f.day)
	if secondClosed.Load() != 1 {
		t.Fatal("superseded browser grant kept stream")
	}
	denied(t, f.store.Require(WithToken(f.ctx, replacement.Token()), f.actor.Login))
}

func TestExpiryDoesNotWaitForBusyDatabase(t *testing.T) {
	f := newFixture(t, true)
	d := f.grant(f.day)
	f.offset.Store(int64(time.Until(d.ExpiresAt) - 150*time.Millisecond))
	closed := make(chan struct{})
	release, err := f.store.Attach(WithToken(f.ctx, d.Token()), f.actor.Login, func() { close(closed) })
	must(t, err)
	defer release()
	tx, err := f.db.BeginTx(f.ctx, nil)
	must(t, err)
	defer tx.Rollback()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("busy database extended an expired stream")
	}
	must(t, tx.Rollback())
	denied(t, f.store.Require(WithToken(f.ctx, d.Token()), f.actor.Login))
}
