package supervisor

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runs.db")
	store, err := InitializeStore(context.Background(), Config{Path: path})
	if err != nil {
		t.Fatalf("InitializeStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func reopenStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := OpenStore(context.Background(), Config{Path: path})
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// reserveInputFor builds a canonical reservation for an arbitrary session key.
func reserveInputFor(t *testing.T, sessionID string, generation int64) ReserveInput {
	t.Helper()
	request, err := CanonicalizeLaunchRequest(LaunchRequestV1{
		Schema:                       LaunchRequestSchemaV1,
		ApplicationSessionID:         sessionID,
		ApplicationSessionGeneration: generation,
		ProjectPolicyRevision:        strings.Repeat("1", 64),
		ProviderProfileRevision:      strings.Repeat("2", 64),
	})
	if err != nil {
		t.Fatalf("CanonicalizeLaunchRequest: %v", err)
	}
	binding, err := CanonicalizePTYBinding(validPTYBinding())
	if err != nil {
		t.Fatalf("CanonicalizePTYBinding: %v", err)
	}
	return ReserveInput{
		ApplicationSessionID:         sessionID,
		ApplicationSessionGeneration: generation,
		LaunchRequest:                request,
		PTYBinding:                   binding,
	}
}

func reserveInput(t *testing.T, mutate func(*ReserveInput)) ReserveInput {
	t.Helper()
	in := reserveInputFor(t, validLaunchRequest().ApplicationSessionID, 7)
	if mutate != nil {
		mutate(&in)
	}
	return in
}

// launchTicketForRun builds the exact canonical ticket the supervisor would
// release for a reserved run. The six authority identities come from the row;
// the sandbox-plan digest and launcher-invocation identity are opaque fields
// the ticket carries itself.
func launchTicketForRun(t *testing.T, run *Run) *LaunchTicketRevision {
	t.Helper()
	ticket := LaunchTicketV1{
		Schema:               LaunchTicketSchemaV1,
		RunID:                run.RunID,
		SandboxGeneration:    run.SandboxGeneration,
		LaunchRequestDigest:  run.LaunchRequestDigest,
		PTYBindingDigest:     run.PTYBindingDigest,
		SandboxPlanDigest:    strings.Repeat("3", 64),
		UnitName:             run.UnitName,
		UnitPropertyDigest:   run.UnitPropertyDigest,
		LauncherInvocationID: "0123456789abcdef0123456789abcdef",
	}
	revision, err := CanonicalizeLaunchTicket(ticket)
	if err != nil {
		t.Fatalf("CanonicalizeLaunchTicket: %v", err)
	}
	return revision
}

// syntheticLaunchTicket builds a valid canonical ticket for a fixed synthetic
// run, independent of any reserved row. It is the ticket content needed only
// to exercise lookup-time failure paths where the ticket never binds a row.
func syntheticLaunchTicket(t *testing.T, runID string) *LaunchTicketRevision {
	t.Helper()
	unit, err := DeriveTransientUnitProperties(TransientUnitPropertiesBinding{
		RunID:             runID,
		SandboxGeneration: "323e4567-e89b-42d3-a456-426614174000",
	})
	if err != nil {
		t.Fatalf("DeriveTransientUnitProperties: %v", err)
	}
	ticket := LaunchTicketV1{
		Schema:               LaunchTicketSchemaV1,
		RunID:                runID,
		SandboxGeneration:    "323e4567-e89b-42d3-a456-426614174000",
		LaunchRequestDigest:  strings.Repeat("1", 64),
		PTYBindingDigest:     strings.Repeat("2", 64),
		SandboxPlanDigest:    strings.Repeat("3", 64),
		UnitName:             unit.UnitName,
		UnitPropertyDigest:   unit.Digest,
		LauncherInvocationID: "0123456789abcdef0123456789abcdef",
	}
	revision, err := CanonicalizeLaunchTicket(ticket)
	if err != nil {
		t.Fatalf("CanonicalizeLaunchTicket: %v", err)
	}
	return revision
}

// authorizeRun advances a starting run to authorized with its canonical ticket.
func authorizeRun(t *testing.T, store *Store, run *Run) *Run {
	t.Helper()
	authorized, err := store.TransitionToAuthorized(context.Background(), run.RunID, launchTicketForRun(t, run))
	if err != nil {
		t.Fatalf("TransitionToAuthorized: %v", err)
	}
	return authorized
}

// startAndAuthorize advances a reserved run through starting to authorized.
func startAndAuthorize(t *testing.T, ctx context.Context, store *Store, run *Run) *Run {
	t.Helper()
	started, err := store.TransitionToStarting(ctx, run.RunID)
	if err != nil {
		t.Fatalf("TransitionToStarting: %v", err)
	}
	return authorizeRun(t, store, started)
}

func TestStoreInitializeOpenReopenPersistence(t *testing.T) {
	store, path := newTestStore(t)
	ctx := context.Background()
	in := reserveInput(t, nil)
	run, err := store.Reserve(ctx, in)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if run.Phase != PhasePrepared || run.NeedsAttention || run.CleanupState != CleanupNone {
		t.Fatalf("new run = %#v, want prepared/clean", run)
	}
	if !isCanonicalUUIDv4(run.RunID) || !isCanonicalUUIDv4(run.SandboxGeneration) {
		t.Fatalf("new run missing supervisor identity: %#v", run)
	}
	if run.RunID == run.SandboxGeneration {
		t.Fatalf("run and sandbox share identity: %#v", run)
	}
	unit, err := DeriveTransientUnitProperties(TransientUnitPropertiesBinding{
		RunID:             run.RunID,
		SandboxGeneration: run.SandboxGeneration,
	})
	if err != nil {
		t.Fatalf("DeriveTransientUnitProperties: %v", err)
	}
	if run.UnitName != unit.UnitName || run.UnitPropertyDigest != unit.Digest {
		t.Fatalf("stored unit contract = %q/%q, want %q/%q",
			run.UnitName, run.UnitPropertyDigest, unit.UnitName, unit.Digest)
	}
	_ = store.Close()

	reopened := reopenStore(t, path)
	got, err := reopened.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !reflect.DeepEqual(got, run) {
		t.Fatalf("reopened run = %#v, want %#v", got, run)
	}
}

func TestStoreReserveExactReplayReturnsSameIdentity(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	in := reserveInput(t, nil)

	first, err := store.Reserve(ctx, in)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	second, err := store.Reserve(ctx, in)
	if err != nil {
		t.Fatalf("Reserve replay: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("replay = %#v, want %#v", second, first)
	}
	if second.RunID != first.RunID {
		t.Fatalf("replay changed supervisor identity")
	}
}

func TestStoreReserveExactReplayAcrossReopen(t *testing.T) {
	store, path := newTestStore(t)
	ctx := context.Background()
	in := reserveInput(t, nil)
	first, err := store.Reserve(ctx, in)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	_ = store.Close()

	reopened := reopenStore(t, path)
	second, err := reopened.Reserve(ctx, in)
	if err != nil {
		t.Fatalf("Reserve after reopen: %v", err)
	}
	if second.RunID != first.RunID {
		t.Fatalf("reopen replay = %q, want %q", second.RunID, first.RunID)
	}
}

func TestStoreReserveConcurrentOneWinner(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	in := reserveInput(t, nil)

	const workers = 32
	var wg sync.WaitGroup
	runs := make(chan *Run, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, err := store.Reserve(ctx, in)
			if err != nil {
				errs <- err
				return
			}
			runs <- run
		}()
	}
	wg.Wait()
	close(runs)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Reserve: %v", err)
	}

	var reference *Run
	count := 0
	for run := range runs {
		if reference == nil {
			reference = run
		} else if !reflect.DeepEqual(reference, run) {
			t.Fatalf("loser got a different run: %#v vs %#v", reference, run)
		}
		count++
	}
	if count != workers {
		t.Fatalf("got %d reservations, want %d", count, workers)
	}

	reconciled, err := store.ListForReconciliation(ctx)
	if err != nil {
		t.Fatalf("ListForReconciliation: %v", err)
	}
	if len(reconciled) != 1 {
		t.Fatalf("store holds %d runs, want exactly one", len(reconciled))
	}
	if reconciled[0].RunID != reference.RunID {
		t.Fatalf("stored run %q != reserved %q", reconciled[0].RunID, reference.RunID)
	}
	if reconciled[0].NeedsAttention {
		t.Fatalf("identical-fact replay must not park the run")
	}
}

func TestStoreReserveConflictForEveryImmutableBoundField(t *testing.T) {
	base := reserveInput(t, nil)
	cases := []struct {
		name   string
		mutate func(*ReserveInput)
	}{
		// A change to any field of the request changes its digest, so this
		// case exercises the launch-request-digest comparison as well.
		{"project policy revision", func(in *ReserveInput) {
			req := validLaunchRequest()
			req.ProjectPolicyRevision = strings.Repeat("3", 64)
			rev, err := CanonicalizeLaunchRequest(req)
			if err != nil {
				t.Fatalf("CanonicalizeLaunchRequest: %v", err)
			}
			in.LaunchRequest = rev
		}},
		{"provider profile revision", func(in *ReserveInput) {
			req := validLaunchRequest()
			req.ProviderProfileRevision = strings.Repeat("3", 64)
			rev, err := CanonicalizeLaunchRequest(req)
			if err != nil {
				t.Fatalf("CanonicalizeLaunchRequest: %v", err)
			}
			in.LaunchRequest = rev
		}},
		{"pty binding digest", func(in *ReserveInput) {
			binding := validPTYBinding()
			binding.Inode++
			rev, err := CanonicalizePTYBinding(binding)
			if err != nil {
				t.Fatalf("CanonicalizePTYBinding: %v", err)
			}
			in.PTYBinding = rev
		}},
		{"boot id", func(in *ReserveInput) {
			binding := validPTYBinding()
			binding.BootID = "223e4567-e89b-42d3-a456-426614174000"
			rev, err := CanonicalizePTYBinding(binding)
			if err != nil {
				t.Fatalf("CanonicalizePTYBinding: %v", err)
			}
			in.PTYBinding = rev
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := newTestStore(t)
			ctx := context.Background()
			original, err := store.Reserve(ctx, base)
			if err != nil {
				t.Fatalf("Reserve: %v", err)
			}
			conflicting := base
			tc.mutate(&conflicting)
			if _, err := store.Reserve(ctx, conflicting); !errors.Is(err, ErrRunConflict) {
				t.Fatalf("error = %v, want ErrRunConflict", err)
			}
			// the conflict must leave the healthy incumbent byte-for-byte
			// unchanged: no park, no identity change, no timestamp bump.
			got, err := store.Get(ctx, original.ApplicationSessionID, original.ApplicationSessionGeneration)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !reflect.DeepEqual(got, original) {
				t.Fatalf("conflict mutated incumbent: %#v -> %#v", original, got)
			}
			if got.NeedsAttention {
				t.Fatalf("conflict parked the healthy incumbent")
			}
			// an exact replay still resolves to the untouched incumbent
			replay, err := store.Reserve(ctx, base)
			if err != nil {
				t.Fatalf("exact replay after conflict: %v", err)
			}
			if replay.RunID != original.RunID {
				t.Fatalf("exact replay after conflict = %q, want %q", replay.RunID, original.RunID)
			}
		})
	}
}

func TestStoreDifferentKeysReserveSeparateRuns(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	first, err := store.Reserve(ctx, reserveInput(t, nil))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	secondIn := reserveInputFor(t, "223e4567-e89b-42d3-a456-426614174000", 9)
	second, err := store.Reserve(ctx, secondIn)
	if err != nil {
		t.Fatalf("Reserve second: %v", err)
	}
	if second.RunID == first.RunID || second.SandboxGeneration == first.SandboxGeneration || second.UnitName == first.UnitName {
		t.Fatalf("separate keys share derived identity: %#v / %#v", first, second)
	}
}

func TestStoreLegalPhaseTransitions(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	evidence := strings.Repeat("c", 64)

	run, err := store.Reserve(ctx, reserveInput(t, nil))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if run.Phase != PhasePrepared {
		t.Fatalf("phase = %s, want prepared", run.Phase)
	}
	run, err = store.TransitionToStarting(ctx, run.RunID)
	if err != nil {
		t.Fatalf("TransitionToStarting: %v", err)
	}
	if run.Phase != PhaseStarting {
		t.Fatalf("phase = %s, want starting", run.Phase)
	}
	run, err = store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run))
	if err != nil {
		t.Fatalf("TransitionToAuthorized: %v", err)
	}
	if run.Phase != PhaseAuthorized {
		t.Fatalf("phase = %s, want authorized", run.Phase)
	}
	if run.LaunchTicketDigest == "" || run.LaunchTicketJSON == "" {
		t.Fatalf("authorized run missing durable ticket: %#v", run)
	}
	run, err = store.TransitionToRunning(ctx, run.RunID)
	if err != nil {
		t.Fatalf("TransitionToRunning: %v", err)
	}
	if run.Phase != PhaseRunning {
		t.Fatalf("phase = %s, want running", run.Phase)
	}
	run, err = store.TransitionToTerminal(ctx, run.RunID, OutcomeFailed, evidence)
	if err != nil {
		t.Fatalf("TransitionToTerminal: %v", err)
	}
	if run.Phase != PhaseTerminal || run.CleanupState != CleanupPending {
		t.Fatalf("terminal run = %#v, want terminal with cleanup pending", run)
	}
	if run.TerminalOutcome != OutcomeFailed || run.EmptyCGroupEvidenceDigest != evidence {
		t.Fatalf("terminal outcome/evidence not retained: %#v", run)
	}
	run, err = store.MarkCleanupComplete(ctx, run.RunID)
	if err != nil {
		t.Fatalf("MarkCleanupComplete: %v", err)
	}
	if run.Phase != PhaseTerminal || run.CleanupState != CleanupComplete {
		t.Fatalf("cleanup run = %#v, want terminal cleanup complete", run)
	}
}

func TestStoreStartingDirectToTerminal(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	run, err := store.Reserve(ctx, reserveInput(t, nil))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	run, err = store.TransitionToStarting(ctx, run.RunID)
	if err != nil {
		t.Fatalf("TransitionToStarting: %v", err)
	}
	// A fast failure after the unit started is terminal from starting.
	run, err = store.TransitionToTerminal(ctx, run.RunID, OutcomeSuccess, strings.Repeat("d", 64))
	if err != nil {
		t.Fatalf("TransitionToTerminal from starting: %v", err)
	}
	if run.Phase != PhaseTerminal || run.CleanupState != CleanupPending {
		t.Fatalf("run = %#v, want terminal pending", run)
	}
}

func TestStoreRejectsIllegalStaleBackwardSkippedTransitions(t *testing.T) {
	ctx := context.Background()
	evidence := strings.Repeat("c", 64)

	t.Run("starting twice is stale", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		if _, err := store.TransitionToStarting(ctx, run.RunID); !errors.Is(err, ErrStaleTransition) {
			t.Fatalf("error = %v, want ErrStaleTransition", err)
		}
	})

	t.Run("running skips starting", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if _, err := store.TransitionToRunning(ctx, run.RunID); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("error = %v, want ErrIllegalTransition", err)
		}
	})

	t.Run("terminal from prepared skips workload", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if _, err := store.TransitionToTerminal(ctx, run.RunID, OutcomeSuccess, evidence); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("error = %v, want ErrIllegalTransition", err)
		}
	})

	t.Run("terminal twice with different data is stale", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		if _, err := store.TransitionToTerminal(ctx, run.RunID, OutcomeFailed, evidence); err != nil {
			t.Fatalf("TransitionToTerminal: %v", err)
		}
		if _, err := store.TransitionToTerminal(ctx, run.RunID, OutcomeSuccess, strings.Repeat("e", 64)); !errors.Is(err, ErrStaleTransition) {
			t.Fatalf("error = %v, want ErrStaleTransition", err)
		}
	})

	t.Run("starting after running is backward", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		run, err = store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run))
		if err != nil {
			t.Fatalf("TransitionToAuthorized: %v", err)
		}
		run, err = store.TransitionToRunning(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToRunning: %v", err)
		}
		if _, err := store.TransitionToStarting(ctx, run.RunID); !errors.Is(err, ErrStaleTransition) {
			t.Fatalf("error = %v, want ErrStaleTransition", err)
		}
	})

	t.Run("starting after terminal is backward", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		if _, err := store.TransitionToTerminal(ctx, run.RunID, OutcomeSuccess, evidence); err != nil {
			t.Fatalf("TransitionToTerminal: %v", err)
		}
		if _, err := store.TransitionToStarting(ctx, run.RunID); !errors.Is(err, ErrStaleTransition) {
			t.Fatalf("error = %v, want ErrStaleTransition", err)
		}
	})

	t.Run("running after terminal is backward", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		if _, err := store.TransitionToTerminal(ctx, run.RunID, OutcomeSuccess, evidence); err != nil {
			t.Fatalf("TransitionToTerminal: %v", err)
		}
		if _, err := store.TransitionToRunning(ctx, run.RunID); !errors.Is(err, ErrStaleTransition) {
			t.Fatalf("error = %v, want ErrStaleTransition", err)
		}
	})

	t.Run("cleanup before terminal is illegal", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if _, err := store.MarkCleanupComplete(ctx, run.RunID); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("error = %v, want ErrIllegalTransition", err)
		}
	})

	t.Run("cleanup twice is stale", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		if _, err := store.TransitionToTerminal(ctx, run.RunID, OutcomeSuccess, evidence); err != nil {
			t.Fatalf("TransitionToTerminal: %v", err)
		}
		if _, err := store.MarkCleanupComplete(ctx, run.RunID); err != nil {
			t.Fatalf("MarkCleanupComplete: %v", err)
		}
		if _, err := store.MarkCleanupComplete(ctx, run.RunID); !errors.Is(err, ErrStaleTransition) {
			t.Fatalf("error = %v, want ErrStaleTransition", err)
		}
	})

	t.Run("needs attention parks transitions", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.MarkNeedsAttention(ctx, run.RunID, ReasonUnverifiedState)
		if err != nil {
			t.Fatalf("MarkNeedsAttention: %v", err)
		}
		if _, err := store.TransitionToStarting(ctx, run.RunID); !errors.Is(err, ErrNeedsAttention) {
			t.Fatalf("error = %v, want ErrNeedsAttention", err)
		}
		if _, err := store.TransitionToRunning(ctx, run.RunID); !errors.Is(err, ErrNeedsAttention) {
			t.Fatalf("error = %v, want ErrNeedsAttention", err)
		}
		// a parked prepared run never started a workload, so it cannot record
		// a terminal either
		if _, err := store.TransitionToTerminal(ctx, run.RunID, OutcomeSuccess, evidence); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("error = %v, want ErrIllegalTransition", err)
		}
	})

	t.Run("unknown run", func(t *testing.T) {
		store, _ := newTestStore(t)
		unknown := "223e4567-e89b-42d3-a456-426614174000"
		if _, err := store.TransitionToStarting(ctx, unknown); !errors.Is(err, ErrRunNotFound) {
			t.Fatalf("error = %v, want ErrRunNotFound", err)
		}
	})

	t.Run("malformed run id is invalid argument", func(t *testing.T) {
		store, _ := newTestStore(t)
		if _, err := store.TransitionToStarting(ctx, "not-a-uuid"); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("error = %v, want ErrInvalidArgument", err)
		}
	})
}

func TestStoreCrashReopenAtEveryPhase(t *testing.T) {
	ctx := context.Background()
	evidence := strings.Repeat("c", 64)

	t.Run("prepared", func(t *testing.T) {
		store, path := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		_ = store.Close()
		reopened := reopenStore(t, path)
		got, err := reopened.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Phase != PhasePrepared {
			t.Fatalf("phase = %s, want prepared", got.Phase)
		}
		// an exact retry while prepared continues the same run
		replay, err := reopened.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve after reopen: %v", err)
		}
		if replay.RunID != run.RunID {
			t.Fatalf("replay = %q, want %q", replay.RunID, run.RunID)
		}
	})

	t.Run("starting", func(t *testing.T) {
		store, path := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		_ = store.Close()
		reopened := reopenStore(t, path)
		got, err := reopened.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Phase != PhaseStarting {
			t.Fatalf("phase = %s, want starting", got.Phase)
		}
		got, err = reopened.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, got))
		if err != nil {
			t.Fatalf("TransitionToAuthorized after reopen: %v", err)
		}
		if got.Phase != PhaseAuthorized {
			t.Fatalf("phase = %s, want authorized", got.Phase)
		}
		got, err = reopened.TransitionToRunning(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToRunning after reopen: %v", err)
		}
		if got.Phase != PhaseRunning {
			t.Fatalf("phase = %s, want running", got.Phase)
		}
	})

	t.Run("running", func(t *testing.T) {
		store, path := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		run, err = store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run))
		if err != nil {
			t.Fatalf("TransitionToAuthorized: %v", err)
		}
		run, err = store.TransitionToRunning(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToRunning: %v", err)
		}
		_ = store.Close()
		reopened := reopenStore(t, path)
		got, err := reopened.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Phase != PhaseRunning {
			t.Fatalf("phase = %s, want running", got.Phase)
		}
		got, err = reopened.TransitionToTerminal(ctx, run.RunID, OutcomeStopped, evidence)
		if err != nil {
			t.Fatalf("TransitionToTerminal after reopen: %v", err)
		}
		if got.Phase != PhaseTerminal || got.CleanupState != CleanupPending {
			t.Fatalf("run = %#v, want terminal pending", got)
		}
	})

	t.Run("terminal cleanup pending", func(t *testing.T) {
		store, path := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		run, err = store.TransitionToTerminal(ctx, run.RunID, OutcomeFailed, evidence)
		if err != nil {
			t.Fatalf("TransitionToTerminal: %v", err)
		}
		_ = store.Close()
		reopened := reopenStore(t, path)
		got, err := reopened.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Phase != PhaseTerminal || got.CleanupState != CleanupPending {
			t.Fatalf("run = %#v, want terminal pending", got)
		}
		got, err = reopened.MarkCleanupComplete(ctx, run.RunID)
		if err != nil {
			t.Fatalf("MarkCleanupComplete after reopen: %v", err)
		}
		if got.CleanupState != CleanupComplete {
			t.Fatalf("cleanup = %s, want complete", got.CleanupState)
		}
	})

	t.Run("terminal cleanup complete", func(t *testing.T) {
		store, path := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		run, err = store.TransitionToTerminal(ctx, run.RunID, OutcomeSuccess, evidence)
		if err != nil {
			t.Fatalf("TransitionToTerminal: %v", err)
		}
		run, err = store.MarkCleanupComplete(ctx, run.RunID)
		if err != nil {
			t.Fatalf("MarkCleanupComplete: %v", err)
		}
		_ = store.Close()
		reopened := reopenStore(t, path)
		got, err := reopened.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Phase != PhaseTerminal || got.CleanupState != CleanupComplete {
			t.Fatalf("run = %#v, want terminal cleanup complete", got)
		}
		// a terminal exact retry returns the recorded identity and outcome
		replay, err := reopened.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve after terminal: %v", err)
		}
		if replay.RunID != run.RunID || replay.TerminalOutcome != OutcomeSuccess {
			t.Fatalf("terminal replay = %#v, want original identity and outcome", replay)
		}
	})
}

func TestStoreNeedsAttentionMonotonicAndNoResurrection(t *testing.T) {
	store, path := newTestStore(t)
	ctx := context.Background()
	run, err := store.Reserve(ctx, reserveInput(t, nil))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	run, err = store.MarkNeedsAttention(ctx, run.RunID, ReasonUnverifiedState)
	if err != nil {
		t.Fatalf("MarkNeedsAttention: %v", err)
	}
	if !run.NeedsAttention || run.NeedsAttentionReason != string(ReasonUnverifiedState) {
		t.Fatalf("parked run = %#v", run)
	}
	if run.Phase != PhasePrepared {
		t.Fatalf("needs_attention must leave the last proven phase, got %s", run.Phase)
	}

	// idempotent re-mark with the same reason
	same, err := store.MarkNeedsAttention(ctx, run.RunID, ReasonUnverifiedState)
	if err != nil {
		t.Fatalf("idempotent re-mark: %v", err)
	}
	if !same.NeedsAttention {
		t.Fatalf("re-mark cleared the flag")
	}

	_ = store.Close()
	reopened := reopenStore(t, path)
	got, err := reopened.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !got.NeedsAttention || got.NeedsAttentionReason != string(ReasonUnverifiedState) {
		t.Fatalf("parked state did not persist: %#v", got)
	}

	// exact replay is parked, never a new or resumed run
	if _, err := reopened.Reserve(ctx, reserveInput(t, nil)); !errors.Is(err, ErrNeedsAttention) {
		t.Fatalf("exact replay of parked run = %v, want ErrNeedsAttention", err)
	}
	// conflicting reuse of a parked run is also refused with the park sentinel,
	// never a new or renamed run
	if _, err := reopened.Reserve(ctx, reserveInput(t, func(in *ReserveInput) {
		binding := validPTYBinding()
		binding.Inode++
		revision, err := CanonicalizePTYBinding(binding)
		if err != nil {
			t.Fatalf("CanonicalizePTYBinding: %v", err)
		}
		in.PTYBinding = revision
	})); !errors.Is(err, ErrNeedsAttention) {
		t.Fatalf("conflicting reuse of parked run = %v, want ErrNeedsAttention", err)
	}
	after, err := reopened.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.RunID != run.RunID || after.UnitName != run.UnitName {
		t.Fatalf("identity changed while parked: %#v vs %#v", after, run)
	}
}

func TestStoreConflictDuringEveryPhaseLeavesIncumbentUntouched(t *testing.T) {
	ctx := context.Background()
	evidence := strings.Repeat("c", 64)

	advance := func(t *testing.T, store *Store, run *Run, phase Phase) *Run {
		t.Helper()
		switch phase {
		case PhasePrepared:
			return run
		case PhaseStarting:
			run, err := store.TransitionToStarting(ctx, run.RunID)
			if err != nil {
				t.Fatalf("TransitionToStarting: %v", err)
			}
			return run
		case PhaseAuthorized:
			run, err := store.TransitionToStarting(ctx, run.RunID)
			if err != nil {
				t.Fatalf("TransitionToStarting: %v", err)
			}
			run, err = store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run))
			if err != nil {
				t.Fatalf("TransitionToAuthorized: %v", err)
			}
			return run
		case PhaseRunning:
			run, err := store.TransitionToStarting(ctx, run.RunID)
			if err != nil {
				t.Fatalf("TransitionToStarting: %v", err)
			}
			run, err = store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run))
			if err != nil {
				t.Fatalf("TransitionToAuthorized: %v", err)
			}
			run, err = store.TransitionToRunning(ctx, run.RunID)
			if err != nil {
				t.Fatalf("TransitionToRunning: %v", err)
			}
			return run
		case PhaseTerminal:
			run, err := store.TransitionToStarting(ctx, run.RunID)
			if err != nil {
				t.Fatalf("TransitionToStarting: %v", err)
			}
			run, err = store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run))
			if err != nil {
				t.Fatalf("TransitionToAuthorized: %v", err)
			}
			run, err = store.TransitionToRunning(ctx, run.RunID)
			if err != nil {
				t.Fatalf("TransitionToRunning: %v", err)
			}
			run, err = store.TransitionToTerminal(ctx, run.RunID, OutcomeFailed, evidence)
			if err != nil {
				t.Fatalf("TransitionToTerminal: %v", err)
			}
			run, err = store.MarkCleanupComplete(ctx, run.RunID)
			if err != nil {
				t.Fatalf("MarkCleanupComplete: %v", err)
			}
			return run
		}
		t.Fatalf("unknown phase %s", phase)
		return nil
	}

	for _, phase := range []Phase{PhasePrepared, PhaseStarting, PhaseAuthorized, PhaseRunning, PhaseTerminal} {
		t.Run(string(phase), func(t *testing.T) {
			store, path := newTestStore(t)
			run, err := store.Reserve(ctx, reserveInput(t, nil))
			if err != nil {
				t.Fatalf("Reserve: %v", err)
			}
			run = advance(t, store, run, phase)

			before, err := store.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			conflicting := reserveInput(t, func(in *ReserveInput) {
				binding := validPTYBinding()
				binding.Inode++
				revision, err := CanonicalizePTYBinding(binding)
				if err != nil {
					t.Fatalf("CanonicalizePTYBinding: %v", err)
				}
				in.PTYBinding = revision
			})
			if _, err := store.Reserve(ctx, conflicting); !errors.Is(err, ErrRunConflict) {
				t.Fatalf("error = %v, want ErrRunConflict", err)
			}

			after, err := store.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("conflict mutated incumbent at %s: %#v -> %#v", phase, before, after)
			}
			if after.NeedsAttention {
				t.Fatalf("conflict parked the healthy incumbent at %s", phase)
			}
			raw := rawDB(t, path)
			var count int
			if err := raw.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count); err != nil {
				t.Fatalf("count rows: %v", err)
			}
			if count != 1 {
				t.Fatalf("conflict created or removed rows: %d, want exactly 1", count)
			}
		})
	}
}

func TestStoreParkedRunRecordsTerminalAndCleanup(t *testing.T) {
	ctx := context.Background()
	evidence := strings.Repeat("c", 64)

	for _, phase := range []Phase{PhaseStarting, PhaseAuthorized, PhaseRunning} {
		t.Run(string(phase), func(t *testing.T) {
			store, _ := newTestStore(t)
			run, err := store.Reserve(ctx, reserveInput(t, nil))
			if err != nil {
				t.Fatalf("Reserve: %v", err)
			}
			run, err = store.TransitionToStarting(ctx, run.RunID)
			if err != nil {
				t.Fatalf("TransitionToStarting: %v", err)
			}
			if phase == PhaseAuthorized || phase == PhaseRunning {
				run, err = store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run))
				if err != nil {
					t.Fatalf("TransitionToAuthorized: %v", err)
				}
			}
			if phase == PhaseRunning {
				run, err = store.TransitionToRunning(ctx, run.RunID)
				if err != nil {
					t.Fatalf("TransitionToRunning: %v", err)
				}
			}
			run, err = store.MarkNeedsAttention(ctx, run.RunID, ReasonUnverifiedState)
			if err != nil {
				t.Fatalf("MarkNeedsAttention: %v", err)
			}
			if !run.NeedsAttention {
				t.Fatalf("park did not take")
			}

			// recording an observed terminal is bookkeeping, not resumption
			run, err = store.TransitionToTerminal(ctx, run.RunID, OutcomeFailed, evidence)
			if err != nil {
				t.Fatalf("TransitionToTerminal on parked run: %v", err)
			}
			if run.Phase != PhaseTerminal || run.CleanupState != CleanupPending {
				t.Fatalf("parked run = %#v, want terminal pending", run)
			}
			if !run.NeedsAttention || run.NeedsAttentionReason != string(ReasonUnverifiedState) {
				t.Fatalf("terminal recording cleared the attention flag: %#v", run)
			}

			// and cleanup can then release the retained unit
			run, err = store.MarkCleanupComplete(ctx, run.RunID)
			if err != nil {
				t.Fatalf("MarkCleanupComplete on parked run: %v", err)
			}
			if run.CleanupState != CleanupComplete {
				t.Fatalf("cleanup = %s, want complete", run.CleanupState)
			}
			if !run.NeedsAttention {
				t.Fatalf("attention cleared after cleanup")
			}
			// the run no longer blocks restart reconciliation
			list, err := store.ListForReconciliation(ctx)
			if err != nil {
				t.Fatalf("ListForReconciliation: %v", err)
			}
			if len(list) != 0 {
				t.Fatalf("parked terminal-clean run still reconciles: %v", list)
			}
		})
	}
}

func TestStoreParkedRunPreparedCanNeverLaunch(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	run, err := store.Reserve(ctx, reserveInput(t, nil))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	run, err = store.MarkNeedsAttention(ctx, run.RunID, ReasonUnverifiedState)
	if err != nil {
		t.Fatalf("MarkNeedsAttention: %v", err)
	}
	// no launch authority may resume
	if _, err := store.TransitionToStarting(ctx, run.RunID); !errors.Is(err, ErrNeedsAttention) {
		t.Fatalf("TransitionToStarting = %v, want ErrNeedsAttention", err)
	}
	if _, err := store.TransitionToRunning(ctx, run.RunID); !errors.Is(err, ErrNeedsAttention) {
		t.Fatalf("TransitionToRunning = %v, want ErrNeedsAttention", err)
	}
	// nothing ever started, so there is no terminal to record
	if _, err := store.TransitionToTerminal(ctx, run.RunID, OutcomeSuccess, strings.Repeat("d", 64)); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("TransitionToTerminal = %v, want ErrIllegalTransition", err)
	}
}

func TestStoreParkedRunCrashReopenAtEveryPhase(t *testing.T) {
	ctx := context.Background()
	evidence := strings.Repeat("c", 64)

	t.Run("parked prepared", func(t *testing.T) {
		store, path := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.MarkNeedsAttention(ctx, run.RunID, ReasonUnverifiedState)
		if err != nil {
			t.Fatalf("MarkNeedsAttention: %v", err)
		}
		_ = store.Close()
		reopened := reopenStore(t, path)
		got, err := reopened.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !got.NeedsAttention || got.Phase != PhasePrepared {
			t.Fatalf("parked prepared run = %#v", got)
		}
		if _, err := reopened.TransitionToStarting(ctx, run.RunID); !errors.Is(err, ErrNeedsAttention) {
			t.Fatalf("TransitionToStarting = %v, want ErrNeedsAttention", err)
		}
	})

	t.Run("parked starting", func(t *testing.T) {
		store, path := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		run, err = store.MarkNeedsAttention(ctx, run.RunID, ReasonUnverifiedState)
		if err != nil {
			t.Fatalf("MarkNeedsAttention: %v", err)
		}
		_ = store.Close()
		reopened := reopenStore(t, path)
		got, err := reopened.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !got.NeedsAttention || got.Phase != PhaseStarting {
			t.Fatalf("parked starting run = %#v", got)
		}
		got, err = reopened.TransitionToTerminal(ctx, run.RunID, OutcomeFailed, evidence)
		if err != nil {
			t.Fatalf("TransitionToTerminal after reopen: %v", err)
		}
		if got.Phase != PhaseTerminal || !got.NeedsAttention || got.CleanupState != CleanupPending {
			t.Fatalf("run = %#v, want terminal pending with attention", got)
		}
		got, err = reopened.MarkCleanupComplete(ctx, run.RunID)
		if err != nil {
			t.Fatalf("MarkCleanupComplete after reopen: %v", err)
		}
		if got.CleanupState != CleanupComplete {
			t.Fatalf("cleanup = %s, want complete", got.CleanupState)
		}
	})

	t.Run("parked running", func(t *testing.T) {
		store, path := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		run, err = store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run))
		if err != nil {
			t.Fatalf("TransitionToAuthorized: %v", err)
		}
		run, err = store.TransitionToRunning(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToRunning: %v", err)
		}
		run, err = store.MarkNeedsAttention(ctx, run.RunID, ReasonUnverifiedState)
		if err != nil {
			t.Fatalf("MarkNeedsAttention: %v", err)
		}
		_ = store.Close()
		reopened := reopenStore(t, path)
		got, err := reopened.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !got.NeedsAttention || got.Phase != PhaseRunning {
			t.Fatalf("parked running run = %#v", got)
		}
		got, err = reopened.TransitionToTerminal(ctx, run.RunID, OutcomeStopped, evidence)
		if err != nil {
			t.Fatalf("TransitionToTerminal after reopen: %v", err)
		}
		if got.Phase != PhaseTerminal || !got.NeedsAttention || got.CleanupState != CleanupPending {
			t.Fatalf("run = %#v, want terminal pending with attention", got)
		}
	})

	t.Run("parked terminal cleanup pending", func(t *testing.T) {
		store, path := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		run, err = store.TransitionToTerminal(ctx, run.RunID, OutcomeFailed, evidence)
		if err != nil {
			t.Fatalf("TransitionToTerminal: %v", err)
		}
		run, err = store.MarkNeedsAttention(ctx, run.RunID, ReasonUnverifiedState)
		if err != nil {
			t.Fatalf("MarkNeedsAttention: %v", err)
		}
		_ = store.Close()
		reopened := reopenStore(t, path)
		got, err := reopened.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Phase != PhaseTerminal || !got.NeedsAttention || got.CleanupState != CleanupPending {
			t.Fatalf("parked terminal-pending run = %#v", got)
		}
		got, err = reopened.MarkCleanupComplete(ctx, run.RunID)
		if err != nil {
			t.Fatalf("MarkCleanupComplete after reopen: %v", err)
		}
		if got.CleanupState != CleanupComplete {
			t.Fatalf("cleanup = %s, want complete", got.CleanupState)
		}
	})

	t.Run("parked terminal cleanup complete", func(t *testing.T) {
		store, path := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		run, err = store.TransitionToTerminal(ctx, run.RunID, OutcomeSuccess, evidence)
		if err != nil {
			t.Fatalf("TransitionToTerminal: %v", err)
		}
		run, err = store.MarkCleanupComplete(ctx, run.RunID)
		if err != nil {
			t.Fatalf("MarkCleanupComplete: %v", err)
		}
		run, err = store.MarkNeedsAttention(ctx, run.RunID, ReasonUnverifiedState)
		if err != nil {
			t.Fatalf("MarkNeedsAttention: %v", err)
		}
		_ = store.Close()
		reopened := reopenStore(t, path)
		got, err := reopened.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Phase != PhaseTerminal || !got.NeedsAttention || got.CleanupState != CleanupComplete {
			t.Fatalf("parked terminal-complete run = %#v", got)
		}
		list, err := reopened.ListForReconciliation(ctx)
		if err != nil {
			t.Fatalf("ListForReconciliation: %v", err)
		}
		if len(list) != 0 {
			t.Fatalf("parked terminal-clean run still reconciles: %v", list)
		}
	})
}

func TestStoreTerminalRequiresPositiveEmptyCGroupEvidence(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	run, err := store.Reserve(ctx, reserveInput(t, nil))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	run, err = store.TransitionToStarting(ctx, run.RunID)
	if err != nil {
		t.Fatalf("TransitionToStarting: %v", err)
	}
	run, err = store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run))
	if err != nil {
		t.Fatalf("TransitionToAuthorized: %v", err)
	}
	run, err = store.TransitionToRunning(ctx, run.RunID)
	if err != nil {
		t.Fatalf("TransitionToRunning: %v", err)
	}

	evidence := strings.Repeat("c", 64)
	for _, tc := range []struct {
		name     string
		outcome  Outcome
		evidence string
	}{
		{"empty evidence", OutcomeSuccess, ""},
		{"malformed evidence", OutcomeSuccess, "not-a-digest"},
		{"uppercase evidence", OutcomeSuccess, strings.ToUpper(evidence)},
		{"unknown outcome", Outcome("weird"), evidence},
		{"empty outcome", Outcome(""), evidence},
	} {
		if _, err := store.TransitionToTerminal(ctx, run.RunID, tc.outcome, tc.evidence); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("%s: error = %v, want ErrInvalidArgument", tc.name, err)
		}
	}
	got, err := store.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Phase != PhaseRunning {
		t.Fatalf("rejected terminal transition advanced phase to %s", got.Phase)
	}

	// cleanup completion requires the terminal row with its evidence
	if _, err := store.MarkCleanupComplete(ctx, run.RunID); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("cleanup before terminal = %v, want ErrIllegalTransition", err)
	}

	run, err = store.TransitionToTerminal(ctx, run.RunID, OutcomeFailed, evidence)
	if err != nil {
		t.Fatalf("TransitionToTerminal: %v", err)
	}
	if run.TerminalOutcome != OutcomeFailed || run.EmptyCGroupEvidenceDigest != evidence || run.CleanupState != CleanupPending {
		t.Fatalf("terminal run = %#v", run)
	}
	if _, err := store.MarkCleanupComplete(ctx, run.RunID); err != nil {
		t.Fatalf("MarkCleanupComplete after evidence: %v", err)
	}
}

func TestStoreTerminalRecordIsContentFree(t *testing.T) {
	store, path := newTestStore(t)
	ctx := context.Background()
	run, err := store.Reserve(ctx, reserveInput(t, nil))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	run, err = store.TransitionToStarting(ctx, run.RunID)
	if err != nil {
		t.Fatalf("TransitionToStarting: %v", err)
	}
	run, err = store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run))
	if err != nil {
		t.Fatalf("TransitionToAuthorized: %v", err)
	}
	run, err = store.TransitionToRunning(ctx, run.RunID)
	if err != nil {
		t.Fatalf("TransitionToRunning: %v", err)
	}
	evidence := strings.Repeat("d", 64)
	run, err = store.TransitionToTerminal(ctx, run.RunID, OutcomeSuccess, evidence)
	if err != nil {
		t.Fatalf("TransitionToTerminal: %v", err)
	}

	_ = store.Close()
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = raw.Close() }()
	var outcome, storedEvidence, cleanup string
	if err := raw.QueryRow(
		`SELECT terminal_outcome, empty_cgroup_evidence_digest, cleanup_state
		 FROM runs WHERE run_id = ?`, run.RunID).Scan(&outcome, &storedEvidence, &cleanup); err != nil {
		t.Fatalf("raw scan: %v", err)
	}
	if outcome != string(OutcomeSuccess) || storedEvidence != evidence || cleanup != string(CleanupPending) {
		t.Fatalf("stored terminal = %q/%q/%q, want success/evidence/pending", outcome, storedEvidence, cleanup)
	}
}

func TestRunRecordCarriesNoTerminalContentFields(t *testing.T) {
	runType := reflect.TypeOf(Run{})
	want := []string{
		"RunID", "SandboxGeneration", "ApplicationSessionID", "ApplicationSessionGeneration",
		"LaunchRequestDigest", "LaunchRequestJSON",
		"PTYBindingDigest", "PTYBindingJSON",
		"ProjectPolicyRevision", "ProviderProfileRevision",
		"UnitName", "UnitPropertyDigest", "BootID",
		"LaunchTicketDigest", "LaunchTicketJSON",
		"Phase", "NeedsAttention", "NeedsAttentionReason",
		"TerminalOutcome", "EmptyCGroupEvidenceDigest", "CleanupState",
		"CreatedAt", "UpdatedAt",
	}
	if runType.NumField() != len(want) {
		t.Fatalf("Run has %d fields, want %d", runType.NumField(), len(want))
	}
	for i, name := range want {
		if runType.Field(i).Name != name {
			t.Errorf("field %d = %s, want %s", i, runType.Field(i).Name, name)
		}
	}
}

func TestReserveInputCarriesNoUnitSelection(t *testing.T) {
	inputType := reflect.TypeOf(ReserveInput{})
	want := []string{
		"ApplicationSessionID", "ApplicationSessionGeneration",
		"LaunchRequest", "PTYBinding",
	}
	if inputType.NumField() != len(want) {
		t.Fatalf("ReserveInput has %d fields, want %d", inputType.NumField(), len(want))
	}
	for i, name := range want {
		if inputType.Field(i).Name != name {
			t.Errorf("field %d = %s, want %s", i, inputType.Field(i).Name, name)
		}
	}
}

func TestStoreFailedOperationsLeaveNoPartialState(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	run, err := store.Reserve(ctx, reserveInput(t, nil))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	before, err := store.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// every rejected operation must leave the row byte-identical
	rejections := []func() error{
		func() error {
			_, err := store.TransitionToTerminal(ctx, run.RunID, OutcomeSuccess, "bad")
			return err
		},
		func() error {
			_, err := store.TransitionToRunning(ctx, run.RunID)
			return err
		},
		func() error {
			_, err := store.TransitionToStarting(ctx, "223e4567-e89b-42d3-a456-426614174000")
			return err
		},
		func() error {
			_, err := store.MarkCleanupComplete(ctx, run.RunID)
			return err
		},
	}
	for i, reject := range rejections {
		if err := reject(); err == nil {
			t.Fatalf("rejection %d unexpectedly succeeded", i)
		}
		after, err := store.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("rejection %d mutated the run: %#v -> %#v", i, before, after)
		}
	}

	// a failure inside a transaction must roll the phase change back
	if err := store.withWrite(ctx, func(tx *sql.Tx) error {
		if err := casRows(ctx, tx,
			`UPDATE runs SET phase = 'starting', updated_at = ?
			 WHERE run_id = ? AND phase = ? AND needs_attention = 0`,
			formatStoreTime(time.Now()), run.RunID, string(PhasePrepared)); err != nil {
			return err
		}
		return errors.New("boom")
	}); err == nil {
		t.Fatalf("withWrite committed despite callback failure")
	}
	afterTx, err := store.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if afterTx.Phase != PhasePrepared {
		t.Fatalf("rolled-back transition advanced phase to %s", afterTx.Phase)
	}

	// a rejected reservation creates no partial run
	if _, err := store.Reserve(ctx, reserveInput(t, func(in *ReserveInput) {
		in.ApplicationSessionID = "not-a-uuid"
	})); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error = %v, want ErrInvalidArgument", err)
	}
	all, err := store.ListForReconciliation(ctx)
	if err != nil {
		t.Fatalf("ListForReconciliation: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("store holds %d runs after rejections, want 1", len(all))
	}
}

func TestStoreTransitionDoesNotMutateUnrelatedRun(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	first, err := store.Reserve(ctx, reserveInputFor(t, "123e4567-e89b-42d3-a456-426614174000", 7))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	second, err := store.Reserve(ctx, reserveInputFor(t, "723e4567-e89b-42d3-a456-426614174000", 11))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	before, err := store.Get(ctx, second.ApplicationSessionID, second.ApplicationSessionGeneration)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	evidence := strings.Repeat("c", 64)
	first, err = store.TransitionToStarting(ctx, first.RunID)
	if err != nil {
		t.Fatalf("TransitionToStarting: %v", err)
	}
	first, err = store.TransitionToAuthorized(ctx, first.RunID, launchTicketForRun(t, first))
	if err != nil {
		t.Fatalf("TransitionToAuthorized: %v", err)
	}
	first, err = store.TransitionToRunning(ctx, first.RunID)
	if err != nil {
		t.Fatalf("TransitionToRunning: %v", err)
	}
	first, err = store.TransitionToTerminal(ctx, first.RunID, OutcomeSuccess, evidence)
	if err != nil {
		t.Fatalf("TransitionToTerminal: %v", err)
	}
	if _, err := store.MarkCleanupComplete(ctx, first.RunID); err != nil {
		t.Fatalf("MarkCleanupComplete: %v", err)
	}
	if first.RunID == second.RunID || first.UnitName == second.UnitName {
		t.Fatalf("unrelated runs share identity")
	}

	after, err := store.Get(ctx, second.ApplicationSessionID, second.ApplicationSessionGeneration)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("transitions on one run mutated the unrelated run: %#v -> %#v", before, after)
	}
}

func TestStoreReserveRejectsInvalidInput(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	base := reserveInput(t, nil)
	cases := []struct {
		name   string
		mutate func(*ReserveInput)
	}{
		{"nil launch request", func(in *ReserveInput) { in.LaunchRequest = nil }},
		{"nil pty binding", func(in *ReserveInput) { in.PTYBinding = nil }},
		{"noncanonical request", func(in *ReserveInput) {
			in.LaunchRequest = &LaunchRequestRevision{Digest: base.LaunchRequest.Digest, CanonicalJSON: "{}"}
		}},
		{"noncanonical binding", func(in *ReserveInput) {
			in.PTYBinding = &PTYBindingRevision{Digest: base.PTYBinding.Digest, CanonicalJSON: "{}"}
		}},
		{"session id mismatch", func(in *ReserveInput) {
			in.ApplicationSessionID = "223e4567-e89b-42d3-a456-426614174000"
		}},
		{"session id not a uuid", func(in *ReserveInput) { in.ApplicationSessionID = "not-a-uuid" }},
		{"zero generation", func(in *ReserveInput) { in.ApplicationSessionGeneration = 0 }},
		{"negative generation", func(in *ReserveInput) { in.ApplicationSessionGeneration = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mutate(&in)
			if _, err := store.Reserve(ctx, in); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
		})
	}
	all, err := store.ListForReconciliation(ctx)
	if err != nil {
		t.Fatalf("ListForReconciliation: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("rejected reservations created %d runs", len(all))
	}
}

func TestStoreMarkNeedsAttentionRejectsInvalidReason(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	run, err := store.Reserve(ctx, reserveInput(t, nil))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := store.MarkNeedsAttention(ctx, run.RunID, AttentionReason("made-up")); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error = %v, want ErrInvalidArgument", err)
	}
}

func TestStoreGetReturnsNotFound(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	if _, err := store.Get(ctx, "123e4567-e89b-42d3-a456-426614174000", 1); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("Get = %v, want ErrRunNotFound", err)
	}
	if _, err := store.GetByRunID(ctx, "223e4567-e89b-42d3-a456-426614174000"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("GetByRunID = %v, want ErrRunNotFound", err)
	}
	if _, err := store.GetByUnitName(ctx, "noisedeck-absent.service"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("GetByUnitName = %v, want ErrRunNotFound", err)
	}
	if _, err := store.Get(ctx, "not-a-uuid", 1); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Get malformed = %v, want ErrInvalidArgument", err)
	}
}

func TestStoreGetByUnitNameResolvesRecordedUnit(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	run, err := store.Reserve(ctx, reserveInput(t, nil))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	got, err := store.GetByUnitName(ctx, run.UnitName)
	if err != nil {
		t.Fatalf("GetByUnitName: %v", err)
	}
	if got.RunID != run.RunID {
		t.Fatalf("unit name resolved %q, want %q", got.RunID, run.RunID)
	}
}

func TestStoreListForReconciliation(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	// terminal + cleanup complete: excluded
	a, err := store.Reserve(ctx, reserveInputFor(t, "323e4567-e89b-42d3-a456-426614174000", 1))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	a, err = store.TransitionToStarting(ctx, a.RunID)
	if err != nil {
		t.Fatalf("TransitionToStarting: %v", err)
	}
	a, err = store.TransitionToAuthorized(ctx, a.RunID, launchTicketForRun(t, a))
	if err != nil {
		t.Fatalf("TransitionToAuthorized: %v", err)
	}
	a, err = store.TransitionToRunning(ctx, a.RunID)
	if err != nil {
		t.Fatalf("TransitionToRunning: %v", err)
	}
	a, err = store.TransitionToTerminal(ctx, a.RunID, OutcomeSuccess, strings.Repeat("e", 64))
	if err != nil {
		t.Fatalf("TransitionToTerminal: %v", err)
	}
	if _, err := store.MarkCleanupComplete(ctx, a.RunID); err != nil {
		t.Fatalf("MarkCleanupComplete: %v", err)
	}

	// running: included
	b, err := store.Reserve(ctx, reserveInputFor(t, "423e4567-e89b-42d3-a456-426614174000", 2))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	b, err = store.TransitionToStarting(ctx, b.RunID)
	if err != nil {
		t.Fatalf("TransitionToStarting: %v", err)
	}
	b, err = store.TransitionToAuthorized(ctx, b.RunID, launchTicketForRun(t, b))
	if err != nil {
		t.Fatalf("TransitionToAuthorized: %v", err)
	}
	b, err = store.TransitionToRunning(ctx, b.RunID)
	if err != nil {
		t.Fatalf("TransitionToRunning: %v", err)
	}

	// terminal + cleanup pending: included
	c, err := store.Reserve(ctx, reserveInputFor(t, "523e4567-e89b-42d3-a456-426614174000", 3))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	c, err = store.TransitionToStarting(ctx, c.RunID)
	if err != nil {
		t.Fatalf("TransitionToStarting: %v", err)
	}
	if _, err := store.TransitionToTerminal(ctx, c.RunID, OutcomeStopped, strings.Repeat("f", 64)); err != nil {
		t.Fatalf("TransitionToTerminal: %v", err)
	}

	// prepared: included
	d, err := store.Reserve(ctx, reserveInputFor(t, "623e4567-e89b-42d3-a456-426614174000", 4))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	list, err := store.ListForReconciliation(ctx)
	if err != nil {
		t.Fatalf("ListForReconciliation: %v", err)
	}
	got := make(map[string]bool, len(list))
	for _, r := range list {
		got[r.RunID] = true
	}
	if len(got) != 3 {
		t.Fatalf("reconciliation list has %d runs, want 3", len(got))
	}
	if !got[b.RunID] || !got[c.RunID] || !got[d.RunID] {
		t.Fatalf("list misses active runs: %v", got)
	}
	if got[a.RunID] {
		t.Fatalf("list includes a terminal cleanup-complete run")
	}
}

// canonicalTicketMutated builds the row's canonical ticket with one bound
// identity changed, then canonicalizes the result. A ticket canonicalized
// against any binding other than the row must fail authorization.
func canonicalTicketMutated(t *testing.T, run *Run, mutate func(*LaunchTicketV1)) *LaunchTicketRevision {
	t.Helper()
	ticket := LaunchTicketV1{
		Schema:               LaunchTicketSchemaV1,
		RunID:                run.RunID,
		SandboxGeneration:    run.SandboxGeneration,
		LaunchRequestDigest:  run.LaunchRequestDigest,
		PTYBindingDigest:     run.PTYBindingDigest,
		SandboxPlanDigest:    strings.Repeat("3", 64),
		UnitName:             run.UnitName,
		UnitPropertyDigest:   run.UnitPropertyDigest,
		LauncherInvocationID: "0123456789abcdef0123456789abcdef",
	}
	mutate(&ticket)
	setLaunchTicketUnit(t, &ticket)
	revision, err := CanonicalizeLaunchTicket(ticket)
	if err != nil {
		t.Fatalf("CanonicalizeLaunchTicket: %v", err)
	}
	return revision
}

func TestStoreAuthorizePersistsExactTicketAndReopens(t *testing.T) {
	store, path := newTestStore(t)
	ctx := context.Background()
	run, err := store.Reserve(ctx, reserveInput(t, nil))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	want := startAndAuthorize(t, ctx, store, run)
	if want.Phase != PhaseAuthorized {
		t.Fatalf("phase = %s, want authorized", want.Phase)
	}
	ticket := launchTicketForRun(t, run)
	if want.LaunchTicketDigest != ticket.Digest || want.LaunchTicketJSON != ticket.CanonicalJSON {
		t.Fatalf("stored ticket = %q/%q, want %q/%q",
			want.LaunchTicketDigest, want.LaunchTicketJSON, ticket.Digest, ticket.CanonicalJSON)
	}

	_ = store.Close()
	reopened := reopenStore(t, path)
	got, err := reopened.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened authorized run = %#v, want %#v", got, want)
	}
	// running is reachable only from authorized, then terminal from running
	got, err = reopened.TransitionToRunning(ctx, run.RunID)
	if err != nil {
		t.Fatalf("TransitionToRunning after reopen: %v", err)
	}
	if got.Phase != PhaseRunning {
		t.Fatalf("phase = %s, want running", got.Phase)
	}
	if got.LaunchTicketDigest != want.LaunchTicketDigest || got.LaunchTicketJSON != want.LaunchTicketJSON {
		t.Fatalf("running run lost its authorized ticket: %#v, want %#v", got, want)
	}
	got, err = reopened.TransitionToTerminal(ctx, run.RunID, OutcomeSuccess, strings.Repeat("d", 64))
	if err != nil {
		t.Fatalf("TransitionToTerminal: %v", err)
	}
	if got.LaunchTicketDigest == "" || got.LaunchTicketJSON == "" {
		t.Fatalf("terminal-after-authorized run lost its ticket: %#v", got)
	}
}

func TestStoreAuthorizeReplayAndConflict(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	run, err := store.Reserve(ctx, reserveInput(t, nil))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	first := startAndAuthorize(t, ctx, store, run)

	// an exact retry returns the same record without mutation
	replay, err := store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run))
	if err != nil {
		t.Fatalf("TransitionToAuthorized replay: %v", err)
	}
	if !reflect.DeepEqual(first, replay) {
		t.Fatalf("replay = %#v, want %#v", replay, first)
	}

	// a different ticket on an already-authorized run is a conflict that
	// never mutates the incumbent
	different := canonicalTicketMutated(t, run, func(t *LaunchTicketV1) {
		t.SandboxPlanDigest = strings.Repeat("4", 64)
	})
	if _, err := store.TransitionToAuthorized(ctx, run.RunID, different); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("different ticket = %v, want ErrRunConflict", err)
	}
	after, err := store.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(first, after) {
		t.Fatalf("conflicting authorize mutated incumbent: %#v -> %#v", first, after)
	}
}

func TestStoreAuthorizeRejectsBadTicket(t *testing.T) {
	ctx := context.Background()

	t.Run("nil ticket is invalid argument", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		if _, err := store.TransitionToAuthorized(ctx, run.RunID, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("error = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("malformed run id is invalid argument", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		if _, err := store.TransitionToAuthorized(ctx, "not-a-uuid", launchTicketForRun(t, run)); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("error = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("noncanonical ticket json is invalid argument", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		ticket := launchTicketForRun(t, run)
		noncanonical := &LaunchTicketRevision{
			Digest:        ticket.Digest,
			CanonicalJSON: strings.Replace(ticket.CanonicalJSON, `"run_id"`, ` "run_id"`, 1),
		}
		if _, err := store.TransitionToAuthorized(ctx, run.RunID, noncanonical); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("error = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("ticket digest mismatch is invalid argument", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		ticket := launchTicketForRun(t, run)
		mismatched := &LaunchTicketRevision{
			Digest:        strings.Repeat("9", 64),
			CanonicalJSON: ticket.CanonicalJSON,
		}
		if _, err := store.TransitionToAuthorized(ctx, run.RunID, mismatched); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("error = %v, want ErrInvalidArgument", err)
		}
	})

	// every cross-binding mutation of one of the six row-bound identities
	// must fail closed with a content-free invalid-argument error
	crossBindings := []struct {
		name   string
		mutate func(*LaunchTicketV1)
	}{
		{"different run id", func(t *LaunchTicketV1) { t.RunID = "723e4567-e89b-42d3-a456-426614174000" }},
		{"different sandbox generation", func(t *LaunchTicketV1) { t.SandboxGeneration = "723e4567-e89b-42d3-a456-426614174000" }},
		{"different launch request digest", func(t *LaunchTicketV1) { t.LaunchRequestDigest = strings.Repeat("9", 64) }},
		{"different pty binding digest", func(t *LaunchTicketV1) { t.PTYBindingDigest = strings.Repeat("9", 64) }},
	}
	for _, tc := range crossBindings {
		t.Run("cross-binding "+tc.name, func(t *testing.T) {
			store, _ := newTestStore(t)
			run, err := store.Reserve(ctx, reserveInput(t, nil))
			if err != nil {
				t.Fatalf("Reserve: %v", err)
			}
			run, err = store.TransitionToStarting(ctx, run.RunID)
			if err != nil {
				t.Fatalf("TransitionToStarting: %v", err)
			}
			if _, err := store.TransitionToAuthorized(ctx, run.RunID, canonicalTicketMutated(t, run, tc.mutate)); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
			got, err := store.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Phase != PhaseStarting || got.LaunchTicketDigest != "" {
				t.Fatalf("rejected authorize left partial state: %#v", got)
			}
		})
	}

	t.Run("errors are content-free", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		_, err = store.TransitionToAuthorized(ctx, run.RunID, canonicalTicketMutated(t, run, func(t *LaunchTicketV1) {
			t.LaunchRequestDigest = strings.Repeat("9", 64)
		}))
		if err == nil {
			t.Fatalf("expected a content-free error")
		}
		want := ErrInvalidArgument.Error()
		if err.Error() != want {
			t.Fatalf("error text = %q, want content-free %q", err.Error(), want)
		}
	})
}

func TestStoreAuthorizePhaseEdges(t *testing.T) {
	ctx := context.Background()
	evidence := strings.Repeat("c", 64)

	t.Run("authorize before starting is illegal", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if _, err := store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run)); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("error = %v, want ErrIllegalTransition", err)
		}
	})

	t.Run("authorize after running is stale", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run = startAndAuthorize(t, ctx, store, run)
		if _, err := store.TransitionToRunning(ctx, run.RunID); err != nil {
			t.Fatalf("TransitionToRunning: %v", err)
		}
		if _, err := store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run)); !errors.Is(err, ErrStaleTransition) {
			t.Fatalf("error = %v, want ErrStaleTransition", err)
		}
	})

	t.Run("authorize after terminal is stale", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run = startAndAuthorize(t, ctx, store, run)
		if _, err := store.TransitionToTerminal(ctx, run.RunID, OutcomeFailed, evidence); err != nil {
			t.Fatalf("TransitionToTerminal: %v", err)
		}
		if _, err := store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run)); !errors.Is(err, ErrStaleTransition) {
			t.Fatalf("error = %v, want ErrStaleTransition", err)
		}
	})

	t.Run("authorize parked run needs attention", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		if _, err := store.MarkNeedsAttention(ctx, run.RunID, ReasonUnverifiedState); err != nil {
			t.Fatalf("MarkNeedsAttention: %v", err)
		}
		if _, err := store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run)); !errors.Is(err, ErrNeedsAttention) {
			t.Fatalf("error = %v, want ErrNeedsAttention", err)
		}
	})

	t.Run("unknown run", func(t *testing.T) {
		store, _ := newTestStore(t)
		unknown := "223e4567-e89b-42d3-a456-426614174000"
		if _, err := store.TransitionToAuthorized(ctx, unknown, syntheticLaunchTicket(t, unknown)); !errors.Is(err, ErrRunNotFound) {
			t.Fatalf("error = %v, want ErrRunNotFound", err)
		}
	})
}

func TestStoreRunningRequiresAuthorized(t *testing.T) {
	ctx := context.Background()

	t.Run("starting to running is illegal", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		if _, err := store.TransitionToRunning(ctx, run.RunID); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("error = %v, want ErrIllegalTransition", err)
		}
	})

	t.Run("full authorized lifecycle", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		run, err = store.TransitionToAuthorized(ctx, run.RunID, launchTicketForRun(t, run))
		if err != nil {
			t.Fatalf("TransitionToAuthorized: %v", err)
		}
		run, err = store.TransitionToRunning(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToRunning: %v", err)
		}
		if _, err := store.TransitionToTerminal(ctx, run.RunID, OutcomeSuccess, strings.Repeat("d", 64)); err != nil {
			t.Fatalf("TransitionToTerminal: %v", err)
		}
	})
}

func TestStoreTerminalBeforeAndAfterAuthorized(t *testing.T) {
	ctx := context.Background()
	evidence := strings.Repeat("c", 64)

	// a fast failure during starting records terminal with no ticket: the run
	// never crossed authorization
	t.Run("terminal from starting carries no ticket", func(t *testing.T) {
		store, _ := newTestStore(t)
		run, err := store.Reserve(ctx, reserveInput(t, nil))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		run, err = store.TransitionToStarting(ctx, run.RunID)
		if err != nil {
			t.Fatalf("TransitionToStarting: %v", err)
		}
		run, err = store.TransitionToTerminal(ctx, run.RunID, OutcomeFailed, evidence)
		if err != nil {
			t.Fatalf("TransitionToTerminal from starting: %v", err)
		}
		if run.LaunchTicketDigest != "" || run.LaunchTicketJSON != "" {
			t.Fatalf("starting-terminal run carried a ticket: %#v", run)
		}
	})

	// a run that crossed authorization retains its ticket through terminal
	for _, from := range []Phase{PhaseAuthorized, PhaseRunning} {
		t.Run("terminal from "+string(from)+" retains ticket", func(t *testing.T) {
			store, _ := newTestStore(t)
			run, err := store.Reserve(ctx, reserveInput(t, nil))
			if err != nil {
				t.Fatalf("Reserve: %v", err)
			}
			run = startAndAuthorize(t, ctx, store, run)
			if from == PhaseRunning {
				run, err = store.TransitionToRunning(ctx, run.RunID)
				if err != nil {
					t.Fatalf("TransitionToRunning: %v", err)
				}
			}
			run, err = store.TransitionToTerminal(ctx, run.RunID, OutcomeStopped, evidence)
			if err != nil {
				t.Fatalf("TransitionToTerminal from %s: %v", from, err)
			}
			if run.LaunchTicketDigest == "" || run.LaunchTicketJSON == "" {
				t.Fatalf("%s-terminal run lost its ticket: %#v", from, run)
			}
		})
	}
}
