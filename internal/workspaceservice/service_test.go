package workspaceservice

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacegrants"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
	"github.com/noisefloor-oss/deck/internal/workspaceruntime"
)

// Synthetic native observations exercise service authority and ordering. They
// do not qualify gocryptfs, FUSE, rootless Podman or a deployment's host boundary.
type syntheticNative struct {
	state      workspaceruntime.State
	ops        map[string]workspaceruntime.Operation
	calls      []string
	observeErr error
	startErr   error
	lockErr    error
	startWait  bool
	onMount    func()
	onObserve  func()
	reopenID   string
	reopenErr  error
}

func (n *syntheticNative) Observe(context.Context) (workspaceruntime.State, error) {
	if n.onObserve != nil {
		n.onObserve()
	}
	return n.state, n.observeErr
}
func (n *syntheticNative) mounted(id, kind string) (workspaceruntime.Operation, error) {
	if old, ok := n.ops[id]; ok {
		old.State = n.state
		return old, nil
	}
	n.calls = append(n.calls, kind+":"+id)
	n.state = workspaceruntime.State{Storage: "open", Runtime: "stopped", Configured: true, Epoch: "mount-epoch"}
	op := workspaceruntime.Operation{ID: id, Kind: kind, Status: "succeeded", State: n.state}
	n.ops[id] = op
	if n.onMount != nil {
		n.onMount()
	}
	return op, nil
}
func (n *syntheticNative) Initialize(_ context.Context, id string, secret []byte) (workspaceruntime.Operation, error) {
	if len(secret) != 32 {
		return workspaceruntime.Operation{}, errors.New("wrong test handoff")
	}
	return n.mounted(id, "initialize")
}
func (n *syntheticNative) Initialization(ctx context.Context, id string) (workspaceruntime.Operation, error) {
	if n.reopenErr != nil {
		return workspaceruntime.Operation{}, n.reopenErr
	}
	if n.reopenID != "" {
		id = n.reopenID
	}
	return n.Operation(ctx, id)
}
func (n *syntheticNative) ReopenInitialization(ctx context.Context, id string, secret []byte) (workspaceruntime.Operation, error) {
	if n.reopenErr != nil || !n.state.Locked || len(secret) != 32 {
		return workspaceruntime.Operation{}, workspaceruntime.ErrUnsafeStorage
	}
	n.reopenID = "reopen_" + id + "_" + n.state.Epoch
	return n.mounted(n.reopenID, "unlock")
}
func (n *syntheticNative) Unlock(_ context.Context, id string, secret []byte) (workspaceruntime.Operation, error) {
	if len(secret) != 32 {
		return workspaceruntime.Operation{}, errors.New("wrong test handoff")
	}
	return n.mounted(id, "unlock")
}
func (n *syntheticNative) Start(_ context.Context, id string) (workspaceruntime.Operation, error) {
	if errors.Is(n.startErr, workspaceruntime.ErrMigrationRequired) {
		return workspaceruntime.Operation{}, n.startErr
	}
	if old, ok := n.ops[id]; ok {
		old.State = n.state
		return old, n.startErr
	}
	n.calls = append(n.calls, "start:"+id)
	status := "succeeded"
	if n.startErr != nil {
		status = "uncertain"
	} else if n.startWait {
		status = "running"
		n.state.Runtime = "starting"
	} else {
		n.state.Runtime, n.state.Epoch = "running", "running-epoch"
	}
	op := workspaceruntime.Operation{ID: id, Kind: "start", Status: status, State: n.state}
	n.ops[id] = op
	return op, n.startErr
}
func (n *syntheticNative) Lock(ctx context.Context, id string, prepare func(context.Context) error) (workspaceruntime.Operation, error) {
	n.calls = append(n.calls, "lock:"+id)
	if err := prepare(ctx); err != nil {
		op := workspaceruntime.Operation{ID: id, Kind: "lock", Status: "uncertain", Stage: "preparing-stop", State: n.state}
		n.ops[id] = op
		return op, err
	}
	if n.lockErr == nil {
		n.state = workspaceruntime.State{Locked: true, Storage: "closed", Runtime: "stopped", Configured: true, Epoch: "locked-epoch"}
	}
	status := "succeeded"
	if n.lockErr != nil {
		status = "uncertain"
	}
	op := workspaceruntime.Operation{ID: id, Kind: "lock", Status: status, State: n.state}
	n.ops[id] = op
	return op, n.lockErr
}
func (n *syntheticNative) Operation(_ context.Context, id string) (workspaceruntime.Operation, error) {
	op, ok := n.ops[id]
	if !ok {
		return workspaceruntime.Operation{}, workspaceruntime.ErrNotFound
	}
	op.State = n.state
	return op, n.observeErr
}

type method struct {
	ref workspacekeys.MethodRef
	key *ecdsa.PrivateKey
}
type fixture struct {
	t             *testing.T
	ctx           context.Context
	db            *sql.DB
	keys          *workspacekeys.Registry
	grants        *workspacegrants.Store
	service       *Service
	native        *syntheticNative
	actor         workspacekeys.Actor
	receiptToken  string
	secret        []byte
	day, recovery method
	offset        atomic.Int64
	prepared      int
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
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), secret: make([]byte, 32)}
	f.secret[0] = 17
	var err error
	f.db, err = sql.Open("sqlite3", filepath.Join(t.TempDir(), "control.sqlite")+"?_txlock=immediate&_journal_mode=WAL&_synchronous=FULL")
	must(t, err)
	f.openControl()
	t.Cleanup(func() { _ = f.grants.Close(); _ = f.db.Close() })
	f.actor = workspacekeys.NewActor(auth.Login{ID: strings.Repeat("01", 32), Issuer: "https://accounts.google.com", Subject: "test-owner", Email: "owner@example.test"}, "")
	tx, err := f.db.BeginTx(f.ctx, nil)
	must(t, err)
	receipt, err := f.keys.CreateEnrollmentTx(f.ctx, tx, f.actor.Login)
	must(t, err)
	must(t, tx.Commit())
	f.actor = workspacekeys.NewActor(f.actor.Login, receipt.Token())
	f.receiptToken = receipt.Token()
	f.native = &syntheticNative{state: workspaceruntime.State{Locked: true, Storage: "closed", Runtime: "stopped", Epoch: "initial-epoch"}, ops: map[string]workspaceruntime.Operation{}}
	f.openService()
	_, err = f.service.ObserveSecurity(f.ctx)
	must(t, err)
	f.day, f.recovery = f.add(workspacekeys.Password), f.add(workspacekeys.Recovery)
	f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.VerifyMethod, Method: f.day.ref})
	f.prove(f.recovery, workspacekeys.Action{Operation: workspacekeys.VerifyMethod, Method: f.recovery.ref})
	return f
}
func (f *fixture) openControl() {
	f.t.Helper()
	now := func() time.Time { return time.Now().Add(time.Duration(f.offset.Load())) }
	var err error
	f.grants, err = workspacegrants.New(f.ctx, f.db, workspacegrants.Config{InstanceID: "instance", Now: now})
	must(f.t, err)
	f.keys, err = workspacekeys.New(f.ctx, f.db, workspacekeys.Config{InstanceID: "instance", Origin: "https://deck.example", RPID: "deck.example", Now: now, Preparation: f.grants})
	must(f.t, err)
	must(f.t, f.grants.BindRegistry(f.keys))
}
func (f *fixture) openService() {
	f.t.Helper()
	var err error
	f.service, err = New(f.native, f.keys, f.grants, func(context.Context) error { f.prepared++; return nil })
	must(f.t, err)
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
	h := sha256.New()
	_, _ = h.Write([]byte("deck.volume.v1\x00"))
	_, _ = h.Write(f.secret)
	must(f.t, f.keys.StoreEnvelope(f.ctx, f.actor, p.Method.ID, raw, nil, base64.RawURLEncoding.EncodeToString(h.Sum(nil))))
	return method{ref: p.Method.MethodRef, key: key}
}
func signature(t *testing.T, key *ecdsa.PrivateKey, ch workspacekeys.Challenge) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(ch.Challenge)
	must(t, err)
	h := sha256.Sum256(raw)
	r, s, err := ecdsa.Sign(rand.Reader, key, h[:])
	must(t, err)
	encoded := make([]byte, 64)
	r.FillBytes(encoded[:32])
	s.FillBytes(encoded[32:])
	return base64.RawURLEncoding.EncodeToString(encoded)
}
func (f *fixture) prove(m method, action workspacekeys.Action) workspacekeys.AuthorizedOperation {
	f.t.Helper()
	ch, err := f.keys.BeginChallenge(f.ctx, f.actor, action)
	must(f.t, err)
	permit, err := f.keys.ConsumeProof(f.ctx, f.actor, ch.ID, signature(f.t, m.key, ch), nil)
	must(f.t, err)
	return permit
}
func (f *fixture) initialize() workspacekeys.AuthorizedOperation {
	f.t.Helper()
	op := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
	authorization, err := f.service.Execute(f.ctx, f.actor.Login, op, append([]byte(nil), f.secret...))
	must(f.t, err)
	if authorization == nil {
		f.t.Fatal("running initialization has no browser authorization")
	}
	return op
}
func (f *fixture) grant() workspacegrants.Delivery {
	f.t.Helper()
	op := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.AuthorizeBrowser, Method: f.day.ref})
	delivery, err := f.grants.Issue(f.ctx, f.actor.Login, op)
	must(f.t, err)
	return delivery
}
func (f *fixture) grantCount() int {
	f.t.Helper()
	var count int
	must(f.t, f.db.QueryRow(`SELECT count(*) FROM workspacegrants_grants`).Scan(&count))
	return count
}

func TestInitializeMountsThenStartsAndPublishesObservedEpoch(t *testing.T) {
	f := newFixture(t)
	op := f.initialize()
	want := []string{"initialize:" + op.ID, "start:" + startID(op.ID)}
	if !reflect.DeepEqual(f.native.calls, want) || startID(op.ID) == op.ID {
		t.Fatalf("native order %v", f.native.calls)
	}
	state, err := f.keys.State(f.ctx, f.actor.Login)
	must(t, err)
	if !state.Initialized || state.RuntimeEpoch != "running-epoch" || f.grantCount() != 1 {
		t.Fatal("running epoch was not atomically bound to initialized access")
	}
	status, err := f.service.Status(f.ctx, f.actor.Login, op.ID)
	must(t, err)
	if status.Status != "complete" || status.Runtime != "running" || status.Locked {
		t.Fatalf("wrong observed status: %+v", status)
	}
	_, err = f.service.Execute(f.ctx, f.actor.Login, op, append([]byte(nil), f.secret...))
	denied(t, err)
	if !reflect.DeepEqual(f.native.calls, want) {
		t.Fatal("completed proof replay dispatched native effects")
	}
}

func TestExecutionRejectsPublicCopiesWrongLoginAndSecretBeforeEffects(t *testing.T) {
	f := newFixture(t)
	permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
	raw, err := json.Marshal(permit)
	must(t, err)
	var serialized workspacekeys.AuthorizedOperation
	must(t, json.Unmarshal(raw, &serialized))
	pending, err := f.keys.PendingInitialization(f.ctx)
	must(t, err)
	for _, candidate := range []workspacekeys.AuthorizedOperation{serialized, pending} {
		_, err := f.service.Execute(f.ctx, f.actor.Login, candidate, append([]byte(nil), f.secret...))
		denied(t, err)
	}
	changed := permit
	changed.Action.Operation = workspacekeys.Lock
	_, err = f.service.Execute(f.ctx, f.actor.Login, changed, nil)
	denied(t, err)
	other := f.actor.Login
	other.ID = strings.Repeat("02", 32)
	_, err = f.service.Execute(f.ctx, other, permit, append([]byte(nil), f.secret...))
	denied(t, err)
	wrong := make([]byte, 32)
	_, err = f.service.Execute(f.ctx, f.actor.Login, permit, wrong)
	denied(t, err)
	if len(f.native.calls) != 0 || f.grantCount() != 0 {
		t.Fatal("unproven input reached native runtime")
	}
}

func TestBrowserAuthorizationDoesNotMountStartOrChangeEpoch(t *testing.T) {
	f := newFixture(t)
	f.initialize()
	calls := append([]string(nil), f.native.calls...)
	permit := f.prove(f.recovery, workspacekeys.Action{Operation: workspacekeys.AuthorizeBrowser, Method: f.recovery.ref})
	result, err := f.service.Execute(f.ctx, f.actor.Login, permit, nil)
	must(t, err)
	if result == nil || !reflect.DeepEqual(calls, f.native.calls) || f.native.state.Epoch != "running-epoch" {
		t.Fatal("browser authorization changed native runtime")
	}
	permit = f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.AuthorizeBrowser, Method: f.day.ref})
	f.native.state.Epoch = "different-runtime"
	_, err = f.service.Execute(f.ctx, f.actor.Login, permit, nil)
	denied(t, err)
}

func TestFreshUnlockStartsClosedOrAlreadyOpenStorage(t *testing.T) {
	for _, storage := range []string{"closed", "open"} {
		t.Run(storage, func(t *testing.T) {
			f := newFixture(t)
			f.initialize()
			f.native.state = workspaceruntime.State{Storage: storage, Runtime: "stopped", Configured: true, Epoch: "stopped-epoch"}
			_, err := f.service.ObserveSecurity(f.ctx)
			must(t, err)
			permit := f.prove(f.recovery, workspacekeys.Action{Operation: workspacekeys.Unlock, Method: f.recovery.ref})
			before := len(f.native.calls)
			result, err := f.service.Execute(f.ctx, f.actor.Login, permit, append([]byte(nil), f.secret...))
			must(t, err)
			want := []string{"start:" + startID(permit.ID)}
			if storage == "closed" {
				want = append([]string{"unlock:" + permit.ID}, want...)
			}
			if result == nil || !reflect.DeepEqual(f.native.calls[before:], want) || f.grantCount() != 1 {
				t.Fatal("fresh unlock did not use the observed storage state")
			}
		})
	}
}

func TestAnotherProofCannotSubstituteAllPublicLockFields(t *testing.T) {
	f := newFixture(t)
	f.initialize()
	otherProof := f.prove(f.recovery, workspacekeys.Action{Operation: workspacekeys.RenameMethod, Method: f.recovery.ref, Target: &f.recovery.ref, Label: "Recovery"})
	lock := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Lock, Method: f.day.ref})
	otherProof.ID, otherProof.Action, otherProof.Payload, otherProof.RuntimeEpoch = lock.ID, lock.Action, lock.Payload, lock.RuntimeEpoch
	before := len(f.native.calls)
	_, err := f.service.Execute(f.ctx, f.actor.Login, otherProof, nil)
	denied(t, err)
	if len(f.native.calls) != before || f.prepared != 0 {
		t.Fatal("public lock fields replaced the private execution receipt")
	}
	_, err = f.grants.Issue(f.ctx, f.actor.Login, lock)
	denied(t, err)
}

func TestRevokedExecutionCannotDispatchNativeEffects(t *testing.T) {
	for _, revoke := range []string{"logout", "lock", "epoch", "expired", "removed"} {
		t.Run(revoke, func(t *testing.T) {
			f := newFixture(t)
			f.initialize()
			permit := f.prove(f.recovery, workspacekeys.Action{Operation: workspacekeys.Unlock, Method: f.recovery.ref})
			switch revoke {
			case "logout":
				must(t, f.grants.Logout(f.ctx, f.actor.Login))
			case "lock":
				f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Lock, Method: f.day.ref})
			case "epoch":
				must(t, f.keys.SetRuntimeEpoch(f.ctx, "new-epoch"))
			case "expired":
				f.offset.Store(int64(6 * time.Minute))
			case "removed":
				f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.RemoveMethod, Method: f.day.ref, Target: &f.recovery.ref})
			}
			f.native.state = workspaceruntime.State{Storage: "closed", Runtime: "stopped", Configured: true, Epoch: permit.RuntimeEpoch}
			before := len(f.native.calls)
			_, err := f.service.Execute(f.ctx, f.actor.Login, permit, append([]byte(nil), f.secret...))
			denied(t, err)
			if len(f.native.calls) != before {
				t.Fatal("revoked proof dispatched native effect")
			}
		})
	}
}

func TestRestartObservesPendingInitializationWithoutGrantOrReplay(t *testing.T) {
	f := newFixture(t)
	permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
	f.native.startErr = errors.New("synthetic launch failure")
	_, err := f.service.Execute(f.ctx, f.actor.Login, permit, append([]byte(nil), f.secret...))
	denied(t, err)
	if f.grantCount() != 0 {
		t.Fatal("failed start issued browser access")
	}
	must(t, f.grants.Close())
	f.openControl()
	f.openService()
	before := len(f.native.calls)
	state, err := f.service.ObserveSecurity(f.ctx)
	must(t, err)
	if state.Storage != "open" || state.Locked || len(f.native.calls) != before {
		t.Fatal("restart replayed effects or lied about failed launch")
	}
	pending, err := f.keys.PendingInitialization(f.ctx)
	must(t, err)
	_, err = f.service.Execute(f.ctx, f.actor.Login, pending, append([]byte(nil), f.secret...))
	denied(t, err)
	_, err = f.service.Execute(f.ctx, f.actor.Login, permit, append([]byte(nil), f.secret...))
	denied(t, err)
	status, err := f.service.Status(f.ctx, f.actor.Login, permit.ID)
	must(t, err)
	if status.Status != "unconfirmed" || f.grantCount() != 0 || len(f.native.calls) != before {
		t.Fatal("durable lookup supplied fresh authorization")
	}
	// Positive observation may reconcile native journals, but still requires a
	// fresh proof of the pinned original method to complete enrollment.
	f.native.startErr = nil
	f.native.state.Runtime, f.native.state.Epoch = "running", "resumed-epoch"
	oldLogin := f.actor.Login
	newLogin := oldLogin
	newLogin.ID = strings.Repeat("02", 32)
	f.actor = workspacekeys.NewActor(newLogin, "")
	resume := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.ResumeInitialization, Method: f.day.ref})
	result, err := f.service.Execute(f.ctx, f.actor.Login, resume, append([]byte(nil), f.secret...))
	must(t, err)
	if result == nil || resume.ID != permit.ID || len(f.native.calls) != before || f.grantCount() != 1 {
		t.Fatal("fresh resume changed intent or replayed native effects")
	}
	_, err = f.service.Status(f.ctx, oldLogin, resume.ID)
	denied(t, err)
}

func TestRestartBeforeFirstNativeJournalWriteCanResumeSameInitialization(t *testing.T) {
	f := newFixture(t)
	permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
	must(t, f.grants.Close())
	f.openControl()
	f.openService()
	_, err := f.service.ObserveSecurity(f.ctx)
	must(t, err)
	if len(f.native.calls) != 0 {
		t.Fatal("gateway restart initialized storage without fresh proof")
	}
	resume := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.ResumeInitialization, Method: f.day.ref})
	result, err := f.service.Execute(f.ctx, f.actor.Login, resume, append([]byte(nil), f.secret...))
	must(t, err)
	if result == nil || resume.ID != permit.ID || !reflect.DeepEqual(f.native.calls, []string{"initialize:" + permit.ID, "start:" + startID(permit.ID)}) {
		t.Fatal("fresh resume replaced the pinned initialization")
	}
}

func TestEnrollmentRebootRequiresFreshResumeAndStartsTheRecordedReopen(t *testing.T) {
	for _, lostReopenResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed-after-reboot", true: "reopened-before-gateway-restart"}[lostReopenResponse], func(t *testing.T) {
			f := newFixture(t)
			permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
			_, err := f.native.Initialize(f.ctx, permit.ID, f.secret)
			must(t, err)
			_, err = f.native.Start(f.ctx, startID(permit.ID))
			must(t, err)
			// The host stops after native success but before registry finalization.
			f.native.state = workspaceruntime.State{Locked: true, Storage: "closed", Runtime: "stopped", Configured: true, Epoch: "second-boot"}
			before := len(f.native.calls)
			_, err = f.service.Execute(f.ctx, f.actor.Login, permit, append([]byte(nil), f.secret...))
			denied(t, err)
			if len(f.native.calls) != before {
				t.Fatal("original proof authorized a new mount after reboot")
			}
			if lostReopenResponse {
				_, err := f.native.ReopenInitialization(f.ctx, permit.ID, f.secret)
				must(t, err)
			}
			must(t, f.grants.Close())
			f.openControl()
			f.openService()
			before = len(f.native.calls)
			_, err = f.service.ObserveSecurity(f.ctx)
			must(t, err)
			if len(f.native.calls) != before || f.grantCount() != 0 {
				t.Fatal("gateway restart replayed reopening or granted access")
			}
			resume := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.ResumeInitialization, Method: f.day.ref})
			result, err := f.service.Execute(f.ctx, f.actor.Login, resume, append([]byte(nil), f.secret...))
			must(t, err)
			if result == nil || f.native.reopenID == "" || f.grantCount() != 1 || f.native.calls[len(f.native.calls)-1] != "start:"+startID(f.native.reopenID) {
				t.Fatal("fresh resume reused the old mount's start receipt")
			}
			initializations := 0
			for _, call := range f.native.calls {
				if strings.HasPrefix(call, "initialize:") {
					initializations++
				}
			}
			if initializations != 1 {
				t.Fatal("resume initialized replacement storage")
			}
		})
	}
}

func TestEnrollmentResumeRefusesUnverifiedBacking(t *testing.T) {
	f := newFixture(t)
	permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
	_, err := f.native.Initialize(f.ctx, permit.ID, f.secret)
	must(t, err)
	f.native.state = workspaceruntime.State{Locked: true, Storage: "closed", Runtime: "stopped", Configured: true, Epoch: "second-boot"}
	f.native.reopenErr = workspaceruntime.ErrUnsafeStorage
	resume := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.ResumeInitialization, Method: f.day.ref})
	before := len(f.native.calls)
	_, err = f.service.Execute(f.ctx, f.actor.Login, resume, append([]byte(nil), f.secret...))
	denied(t, err)
	if len(f.native.calls) != before || f.grantCount() != 0 {
		t.Fatal("unknown backing was started or granted access")
	}
}

func TestLockRevokesBeforePreparationAndRequiresNativeClosure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed", true: "unmount-failed"}[failure], func(t *testing.T) {
			f := newFixture(t)
			f.initialize()
			delivery := f.grant()
			permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Lock, Method: f.day.ref})
			denied(t, f.grants.Require(workspacegrants.WithToken(f.ctx, delivery.Token()), f.actor.Login))
			if failure {
				f.native.lockErr = errors.New("synthetic unmount failure")
			}
			result, err := f.service.Execute(f.ctx, f.actor.Login, permit, nil)
			if failure {
				denied(t, err)
			} else {
				must(t, err)
			}
			if result != nil || f.prepared != 1 || f.grantCount() != 0 {
				t.Fatal("lock changed grant authority or missed preparation")
			}
			status, err := f.service.Status(f.ctx, f.actor.Login, permit.ID)
			must(t, err)
			if status.Locked == failure || failure && status.Status != "unconfirmed" {
				t.Fatalf("false lock status %+v", status)
			}
		})
	}
}

func TestFailedLockPreparationStaysUnconfirmedAfterClosedObservation(t *testing.T) {
	f := newFixture(t)
	f.initialize()
	f.grant()
	permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Lock, Method: f.day.ref})
	prepare := func(context.Context) error {
		f.prepared++
		return errors.New("synthetic cohort commit failure")
	}
	f.service.prepareLock = prepare
	result, err := f.service.Execute(f.ctx, f.actor.Login, permit, nil)
	denied(t, err)
	if result != nil || f.prepared != 1 || f.grantCount() != 0 {
		t.Fatal("failed lock retained browser authority or skipped preparation")
	}
	// A later reboot can close storage without completing the failed cohort
	// transaction. Current closure does not certify that earlier lock operation.
	f.native.state = workspaceruntime.State{Locked: true, Storage: "closed", Runtime: "stopped", Configured: true, Epoch: "second-boot"}
	before := len(f.native.calls)
	for _, restarted := range []bool{false, true} {
		if restarted {
			f.service, err = New(f.native, f.keys, f.grants, prepare)
			must(t, err)
		}
		status, err := f.service.Status(f.ctx, f.actor.Login, permit.ID)
		must(t, err)
		if status.Status != "unconfirmed" || !status.Locked || status.Storage != "closed" || status.Runtime != "stopped" {
			t.Fatalf("current closure certified failed lock after restart=%v: %+v", restarted, status)
		}
		if len(f.native.calls) != before || f.prepared != 1 || f.grantCount() != 0 {
			t.Fatal("lock observation replayed effects or recreated browser authority")
		}
	}
}

func TestObservationRevokesStaleStreamsAndDoesNotInferLocked(t *testing.T) {
	f := newFixture(t)
	op := f.initialize()
	delivery := f.grant()
	var closed atomic.Int32
	release, err := f.grants.Attach(workspacegrants.WithToken(f.ctx, delivery.Token()), f.actor.Login, func() { closed.Add(1) })
	must(t, err)
	defer release()
	f.native.state = workspaceruntime.State{Storage: "closed", Runtime: "stopped", Configured: true, Epoch: "closed-but-not-qualified"}
	state, err := f.service.ObserveSecurity(f.ctx)
	must(t, err)
	if state.Locked || closed.Load() != 1 || f.grantCount() != 0 {
		t.Fatal("epoch transition failed to revoke or inferred full closure")
	}
	status, err := f.service.Status(f.ctx, f.actor.Login, op.ID)
	must(t, err)
	if status.Status != "unconfirmed" {
		t.Fatal("old completion certified a later runtime")
	}
	f.native.observeErr = errors.New("synthetic observer failure")
	_, err = f.service.ObserveSecurity(f.ctx)
	denied(t, err)
	registry, err := f.keys.State(f.ctx, f.actor.Login)
	must(t, err)
	if registry.RuntimeEpoch != f.service.unknown {
		t.Fatal("unknown observation retained previous epoch authority")
	}
}

func TestAbandonedObservationPreservesGrants(t *testing.T) {
	for _, failure := range []string{"canceled-lookup", "expired-lookup", "canceled-observe", "expired-observe", "canceled-publication"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			f.initialize()
			delivery := f.grant()
			var closed atomic.Int32
			release, err := f.grants.Attach(workspacegrants.WithToken(f.ctx, delivery.Token()), f.actor.Login, func() { closed.Add(1) })
			must(t, err)
			defer release()
			var ctx context.Context
			var cancel context.CancelFunc
			switch failure {
			case "canceled-lookup":
				ctx, cancel = context.WithCancel(f.ctx)
				cancel()
				_, err = f.service.observe(ctx)
			case "expired-lookup":
				ctx, cancel = context.WithDeadline(f.ctx, time.Now().Add(-time.Second))
				_, err = f.service.observe(ctx)
			case "canceled-observe":
				ctx, cancel = context.WithCancel(f.ctx)
				f.native.onObserve = cancel
				_, err = f.service.ObserveSecurity(ctx)
			case "expired-observe":
				ctx, cancel = context.WithTimeout(f.ctx, 20*time.Millisecond)
				f.native.onObserve = func() { <-ctx.Done() }
				_, err = f.service.ObserveSecurity(ctx)
			case "canceled-publication":
				// Hold the registry's only connection so publication waits for it,
				// then cancel the caller once that wait has begun.
				ctx, cancel = context.WithCancel(f.ctx)
				f.native.onObserve = func() {
					conn, err := f.db.Conn(f.ctx)
					must(t, err)
					waits := f.db.Stats().WaitCount
					go func() {
						for f.db.Stats().WaitCount == waits {
							time.Sleep(time.Millisecond)
						}
						cancel()
						_ = conn.Close()
					}()
				}
				_, err = f.service.ObserveSecurity(ctx)
				if ctx.Err() == nil {
					t.Fatal("caller was not canceled during publication")
				}
				must(t, err)
			}
			cancel()
			if failure != "canceled-publication" && !errors.Is(err, ErrUnconfirmed) {
				t.Fatalf("abandoned observation returned %v", err)
			}
			if closed.Load() != 0 {
				t.Fatal("abandoned observation closed a live stream")
			}
			must(t, f.grants.Require(workspacegrants.WithToken(f.ctx, delivery.Token()), f.actor.Login))
			state, err := f.keys.State(f.ctx, f.actor.Login)
			must(t, err)
			if state.RuntimeEpoch != "running-epoch" {
				t.Fatal("abandoned observation changed the runtime epoch")
			}
			fresh := f.grant()
			must(t, f.grants.Require(workspacegrants.WithToken(f.ctx, fresh.Token()), f.actor.Login))
		})
	}
}

func TestRegistryObservationFailureClosesStreamsEvenWhenGrantSweepSucceeds(t *testing.T) {
	f := newFixture(t)
	f.initialize()
	delivery := f.grant()
	var closed atomic.Int32
	release, err := f.grants.Attach(workspacegrants.WithToken(f.ctx, delivery.Token()), f.actor.Login, func() { closed.Add(1) })
	must(t, err)
	defer release()
	_, err = f.db.Exec(`ALTER TABLE workspacekeys_instance RENAME TO unavailable_instance`)
	must(t, err)
	_, err = f.service.ObserveSecurity(f.ctx)
	denied(t, err)
	must(t, f.grants.Sweep(f.ctx))
	if closed.Load() != 1 {
		t.Fatal("registry read failure left idle stream open")
	}
	_, err = f.db.Exec(`ALTER TABLE unavailable_instance RENAME TO workspacekeys_instance`)
	must(t, err)
	denied(t, f.grants.Require(workspacegrants.WithToken(f.ctx, delivery.Token()), f.actor.Login))
}

func TestStatusChecksOwnerAndExactLogin(t *testing.T) {
	f := newFixture(t)
	op := f.initialize()
	for _, field := range []string{"owner", "login"} {
		other := f.actor.Login
		if field == "owner" {
			other.Subject = "other-owner"
		} else {
			other.ID = strings.Repeat("02", 32)
		}
		_, err := f.service.Status(f.ctx, other, op.ID)
		denied(t, err)
	}
}

func TestAuthorizationStatusCannotTreatConsumedProofAsBrowserAccess(t *testing.T) {
	f := newFixture(t)
	f.initialize()
	permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.AuthorizeBrowser, Method: f.day.ref})
	status, err := f.service.Status(f.ctx, f.actor.Login, permit.ID)
	must(t, err)
	if status.Status != "unconfirmed" {
		t.Fatal("proof consumption alone reported browser access")
	}
	delivery, err := f.grants.Issue(f.ctx, f.actor.Login, permit)
	must(t, err)
	status, err = f.service.Status(workspacegrants.WithToken(f.ctx, delivery.Token()), f.actor.Login, permit.ID)
	must(t, err)
	if status.Status != "complete" {
		t.Fatal("current granted browser could not observe authorization")
	}
}

func TestManagementSelfRemovalAndActiveCheckHaveNoNativeEffect(t *testing.T) {
	f := newFixture(t)
	f.initialize()
	delivery := f.grant()
	f.ctx = workspacegrants.WithToken(f.ctx, delivery.Token())
	check := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.VerifyMethod, Method: f.day.ref})
	before := len(f.native.calls)
	result, err := f.service.Execute(f.ctx, f.actor.Login, check, nil)
	must(t, err)
	if result != nil {
		t.Fatal("method check granted browser access")
	}
	// Recovery can remove itself without invalidating the already-committed
	// management result or changing the running workspace.
	remove := f.prove(f.recovery, workspacekeys.Action{Operation: workspacekeys.RemoveMethod, Method: f.recovery.ref, Target: &f.recovery.ref})
	result, err = f.service.Execute(f.ctx, f.actor.Login, remove, nil)
	must(t, err)
	if result != nil || len(f.native.calls) != before {
		t.Fatal("management dispatched native effects")
	}
}

func TestCancellationAndRevocationDuringMountCannotContinueStartup(t *testing.T) {
	f := newFixture(t)
	permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
	f.native.onMount = func() { must(t, f.grants.Logout(f.ctx, f.actor.Login)) }
	secret := append([]byte(nil), f.secret...)
	_, err := f.service.Execute(f.ctx, f.actor.Login, permit, secret)
	denied(t, err)
	if len(f.native.calls) != 1 || f.grantCount() != 0 || !reflect.DeepEqual(secret, make([]byte, 32)) {
		t.Fatal("revoked handoff continued startup or retained secret")
	}
	other := newFixture(t)
	permit = other.prove(other.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: other.day.ref, Recovery: &other.recovery.ref})
	other.native.startWait = true
	ctx, cancel := context.WithTimeout(other.ctx, 20*time.Millisecond)
	defer cancel()
	_, err = other.service.Execute(ctx, other.actor.Login, permit, append([]byte(nil), other.secret...))
	denied(t, err)
	if other.grantCount() != 0 || other.native.state.Runtime != "starting" {
		t.Fatal("cancelled startup granted access or claimed shutdown")
	}
}

func TestMigrationPauseRequiresFreshProofBeforeFirstStart(t *testing.T) {
	f := newFixture(t)
	f.native.startErr = workspaceruntime.ErrMigrationRequired
	permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Initialize, Method: f.day.ref, Recovery: &f.recovery.ref})
	result, err := f.service.Execute(f.ctx, f.actor.Login, permit, append([]byte(nil), f.secret...))
	if !errors.Is(err, ErrUnconfirmed) || result != nil || f.grantCount() != 0 {
		t.Fatal("migration admitted app before import")
	}
	if _, err := f.native.Operation(f.ctx, startID(permit.ID)); !errors.Is(err, workspaceruntime.ErrNotFound) {
		t.Fatal("blocked start recorded an effect")
	}
	must(t, f.grants.Close())
	f.openControl()
	f.openService()
	f.native.startErr = nil
	_, err = f.service.Execute(f.ctx, f.actor.Login, permit, append([]byte(nil), f.secret...))
	denied(t, err)
	resume := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.ResumeInitialization, Method: f.day.ref})
	result, err = f.service.Execute(f.ctx, f.actor.Login, resume, append([]byte(nil), f.secret...))
	must(t, err)
	if result == nil || resume.ID != permit.ID || f.grantCount() != 1 || !reflect.DeepEqual(f.native.calls, []string{"initialize:" + permit.ID, "start:" + startID(permit.ID)}) {
		t.Fatal("post-import resume replaced enrollment or replayed native initialization")
	}
}
