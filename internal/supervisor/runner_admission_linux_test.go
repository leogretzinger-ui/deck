package supervisor

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// canonicalRunnerRequest builds the canonical revision of a valid launch
// request; mutate varies one bound fact for conflict cases.
func canonicalRunnerRequest(t *testing.T, mutate func(*LaunchRequestV1)) *LaunchRequestRevision {
	t.Helper()
	request := validLaunchRequest()
	if mutate != nil {
		mutate(&request)
	}
	revision, err := CanonicalizeLaunchRequest(request)
	if err != nil {
		t.Fatalf("CanonicalizeLaunchRequest: %v", err)
	}
	return revision
}

// admitOverSocket drives one admission end to end: it sends the runner message
// with fds over a fresh connected SEQPACKET pair and runs the coordinator on
// the server end, asserting the receiver closes the descriptor copies it
// installed whatever the outcome.
func admitOverSocket(ctx context.Context, t *testing.T, store *Store, runner RunnerAdmission, payload []byte, fds []int) (*Run, error) {
	t.Helper()
	server, client := seqpacketPair(t, true)
	if err := sendSeqpacketMessage(client, payload, fds); err != nil {
		t.Fatalf("send: %v", err)
	}
	before := openFDCount(t)
	run, err := AdmitRunnerLaunch(ctx, server, runner, store)
	if after := openFDCount(t); after != before {
		t.Fatalf("admission leaked received descriptors: before=%d after=%d", before, after)
	}
	return run, err
}

func requireNoReservedRun(t *testing.T, store *Store, revision *LaunchRequestRevision) {
	t.Helper()
	_, err := store.Get(context.Background(), revision.Request.ApplicationSessionID, revision.Request.ApplicationSessionGeneration)
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("store error = %v, want ErrRunNotFound", err)
	}
}

// ptyDescriptorCount reports how many open descriptors reference a PTY slave
// node. Received SCM_RIGHTS copies are new descriptor numbers referencing the
// same /dev/pts node, so a leaked copy shows up here while unrelated
// descriptor churn — for example pooled SQLite connections opened by
// concurrent store writes — does not.
func ptyDescriptorCount(t *testing.T) int {
	t.Helper()
	dir, err := os.Open("/proc/self/fd")
	if err != nil {
		t.Fatalf("open /proc/self/fd: %v", err)
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		t.Fatalf("readdirnames /proc/self/fd: %v", err)
	}
	count := 0
	for _, name := range names {
		target, err := os.Readlink("/proc/self/fd/" + name)
		if err == nil && strings.HasPrefix(target, "/dev/pts/") {
			count++
		}
	}
	return count
}

func TestAdmitRunnerLaunchFirstReserve(t *testing.T) {
	_, slave := requireMeasurableHost(t)
	store, _ := newTestStore(t)
	revision := canonicalRunnerRequest(t, nil)

	run, err := admitOverSocket(context.Background(), t, store, selfRunner(),
		[]byte(revision.CanonicalJSON), slaveTriple(t, int(slave.Fd())))
	if err != nil {
		t.Fatalf("AdmitRunnerLaunch: %v", err)
	}
	if run == nil {
		t.Fatalf("missing run")
	}
	if run.Phase != PhasePrepared || run.CleanupState != CleanupNone || run.NeedsAttention {
		t.Fatalf("run state = %s/%s/attention=%v, want fresh prepared", run.Phase, run.CleanupState, run.NeedsAttention)
	}
	if run.ApplicationSessionID != revision.Request.ApplicationSessionID ||
		run.ApplicationSessionGeneration != revision.Request.ApplicationSessionGeneration {
		t.Fatalf("run keyed %s/%d, want %s/%d", run.ApplicationSessionID, run.ApplicationSessionGeneration,
			revision.Request.ApplicationSessionID, revision.Request.ApplicationSessionGeneration)
	}
	if run.LaunchRequestDigest != revision.Digest {
		t.Fatalf("run request digest %s, want the canonical request digest", run.LaunchRequestDigest)
	}
	direct := slaveTriple(t, int(slave.Fd()))
	measured, err := MeasurePTYBinding(direct[0], direct[1], direct[2])
	if err != nil {
		t.Fatalf("MeasurePTYBinding: %v", err)
	}
	if run.PTYBindingDigest != measured.Digest || run.BootID != measured.Binding.BootID {
		t.Fatalf("run PTY binding does not match a direct measurement of the same slave")
	}
	if run.RunID == "" || run.SandboxGeneration == "" || run.UnitName == "" || run.UnitPropertyDigest == "" {
		t.Fatalf("run is missing generated identities: %#v", run)
	}
	stored, err := store.Get(context.Background(), revision.Request.ApplicationSessionID, revision.Request.ApplicationSessionGeneration)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(stored, run) {
		t.Fatalf("stored run differs from returned run: %#v vs %#v", stored, run)
	}
}

func TestAdmitRunnerLaunchExactReplayReturnsSameRun(t *testing.T) {
	_, slave := requireMeasurableHost(t)
	store, _ := newTestStore(t)
	revision := canonicalRunnerRequest(t, nil)
	payload := []byte(revision.CanonicalJSON)

	first, err := admitOverSocket(context.Background(), t, store, selfRunner(), payload, slaveTriple(t, int(slave.Fd())))
	if err != nil {
		t.Fatalf("first AdmitRunnerLaunch: %v", err)
	}
	second, err := admitOverSocket(context.Background(), t, store, selfRunner(), payload, slaveTriple(t, int(slave.Fd())))
	if err != nil {
		t.Fatalf("replay AdmitRunnerLaunch: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("exact replay returned a different run: %#v vs %#v", first, second)
	}
	reconciled, err := store.ListForReconciliation(context.Background())
	if err != nil {
		t.Fatalf("ListForReconciliation: %v", err)
	}
	if len(reconciled) != 1 || reconciled[0].RunID != first.RunID {
		t.Fatalf("store holds %d runs after replay, want exactly %q", len(reconciled), first.RunID)
	}
}

func TestAdmitRunnerLaunchConflictChangedRequest(t *testing.T) {
	_, slave := requireMeasurableHost(t)
	store, _ := newTestStore(t)
	base := canonicalRunnerRequest(t, nil)
	first, err := admitOverSocket(context.Background(), t, store, selfRunner(),
		[]byte(base.CanonicalJSON), slaveTriple(t, int(slave.Fd())))
	if err != nil {
		t.Fatalf("first AdmitRunnerLaunch: %v", err)
	}
	changed := canonicalRunnerRequest(t, func(request *LaunchRequestV1) {
		request.ProviderProfileRevision = strings.Repeat("3", 64)
	})
	if changed.Digest == base.Digest {
		t.Fatalf("mutation did not change the request digest")
	}
	run, err := admitOverSocket(context.Background(), t, store, selfRunner(),
		[]byte(changed.CanonicalJSON), slaveTriple(t, int(slave.Fd())))
	if !errors.Is(err, ErrRunConflict) {
		t.Fatalf("error = %v, want ErrRunConflict", err)
	}
	if run != nil {
		t.Fatalf("conflict returned a run")
	}
	incumbent, err := store.Get(context.Background(), base.Request.ApplicationSessionID, base.Request.ApplicationSessionGeneration)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(incumbent, first) {
		t.Fatalf("conflict mutated the incumbent run: %#v vs %#v", incumbent, first)
	}
}

func TestAdmitRunnerLaunchConflictChangedPTY(t *testing.T) {
	_, slave := requireMeasurableHost(t)
	otherMaster, otherSlave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer otherMaster.Close()
	defer otherSlave.Close()
	store, _ := newTestStore(t)
	revision := canonicalRunnerRequest(t, nil)
	payload := []byte(revision.CanonicalJSON)

	first, err := admitOverSocket(context.Background(), t, store, selfRunner(), payload, slaveTriple(t, int(slave.Fd())))
	if err != nil {
		t.Fatalf("first AdmitRunnerLaunch: %v", err)
	}
	run, err := admitOverSocket(context.Background(), t, store, selfRunner(), payload, slaveTriple(t, int(otherSlave.Fd())))
	if !errors.Is(err, ErrRunConflict) {
		t.Fatalf("error = %v, want ErrRunConflict", err)
	}
	if run != nil {
		t.Fatalf("conflict returned a run")
	}
	incumbent, err := store.Get(context.Background(), revision.Request.ApplicationSessionID, revision.Request.ApplicationSessionGeneration)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(incumbent, first) {
		t.Fatalf("conflict mutated the incumbent run: %#v vs %#v", incumbent, first)
	}
}

func TestAdmitRunnerLaunchRejectsInvalidAdmissions(t *testing.T) {
	_, slave := requireMeasurableHost(t)
	store, _ := newTestStore(t)
	revision := canonicalRunnerRequest(t, nil)
	payload := []byte(revision.CanonicalJSON)
	slaveFd := int(slave.Fd())
	self := selfRunner()
	cases := []struct {
		name    string
		runner  RunnerAdmission
		payload []byte
		fds     func(t *testing.T) []int
		want    error
	}{
		{"wrong uid", RunnerAdmission{UID: uint32(unix.Getuid()) + 1, PID: int32(unix.Getpid())},
			payload, func(t *testing.T) []int { return slaveTriple(t, slaveFd) }, ErrTransportPeer},
		{"wrong pid", RunnerAdmission{UID: uint32(unix.Getuid()), PID: int32(unix.Getpid()) + 9999},
			payload, func(t *testing.T) []int { return slaveTriple(t, slaveFd) }, ErrTransportPeer},
		{"malformed payload", self, []byte("{not json"),
			func(t *testing.T) []int { return slaveTriple(t, slaveFd) }, ErrTransportMessage},
		{"two descriptors", self, payload,
			func(t *testing.T) []int {
				return []int{dupDescriptor(t, slaveFd, true), dupDescriptor(t, slaveFd, true)}
			},
			ErrTransportAncillary},
		{"four descriptors", self, payload,
			func(t *testing.T) []int {
				return []int{dupDescriptor(t, slaveFd, true), dupDescriptor(t, slaveFd, true),
					dupDescriptor(t, slaveFd, true), dupDescriptor(t, slaveFd, true)}
			}, ErrTransportAncillary},
		{"non-PTY descriptors", self, payload,
			func(t *testing.T) []int { return pipeFDs(t, runnerPTYDescriptorCount) }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, err := admitOverSocket(context.Background(), t, store, tc.runner, tc.payload, tc.fds(t))
			if err == nil {
				t.Fatalf("expected rejection")
			}
			if run != nil {
				t.Fatalf("rejected admission returned a run")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if errors.Is(err, ErrPTYBindingUnavailable) {
				t.Fatalf("error must not be the unavailable sentinel: %v", err)
			}
			requireNoReservedRun(t, store, revision)
		})
	}
}

func TestAdmitRunnerLaunchStoreFailureCreatesNoRun(t *testing.T) {
	_, slave := requireMeasurableHost(t)
	store, path := newTestStore(t)
	revision := canonicalRunnerRequest(t, nil)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	run, err := admitOverSocket(context.Background(), t, store, selfRunner(),
		[]byte(revision.CanonicalJSON), slaveTriple(t, int(slave.Fd())))
	if !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("error = %v, want ErrStoreUnavailable", err)
	}
	if run != nil {
		t.Fatalf("failed admission returned a run")
	}
	reopened := reopenStore(t, path)
	requireNoReservedRun(t, reopened, revision)
}

func TestAdmitRunnerLaunchCancelledContextCreatesNoRun(t *testing.T) {
	_, slave := requireMeasurableHost(t)
	store, _ := newTestStore(t)
	revision := canonicalRunnerRequest(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	run, err := admitOverSocket(ctx, t, store, selfRunner(),
		[]byte(revision.CanonicalJSON), slaveTriple(t, int(slave.Fd())))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if run != nil {
		t.Fatalf("cancelled admission returned a run")
	}
	requireNoReservedRun(t, store, revision)
}

func TestAdmitRunnerLaunchCallerShutdownWhileBlocked(t *testing.T) {
	store, _ := newTestStore(t)
	revision := canonicalRunnerRequest(t, nil)
	server, _ := seqpacketPair(t, true)
	type result struct {
		run *Run
		err error
	}
	done := make(chan result, 1)
	before := openFDCount(t)
	go func() {
		run, err := AdmitRunnerLaunch(context.Background(), server, selfRunner(), store)
		done <- result{run: run, err: err}
	}()
	time.Sleep(25 * time.Millisecond)
	if err := unix.Shutdown(server, unix.SHUT_RDWR); err != nil {
		t.Fatalf("shutdown caller-owned connection: %v", err)
	}
	select {
	case got := <-done:
		if got.run != nil || !errors.Is(got.err, ErrTransportAncillary) {
			t.Fatalf("result = %#v/%v, want nil/ErrTransportAncillary", got.run, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("caller shutdown did not interrupt blocked admission")
	}
	if after := openFDCount(t); after != before {
		t.Fatalf("caller shutdown changed descriptor count: before=%d after=%d", before, after)
	}
	requireNoReservedRun(t, store, revision)
}

func TestAdmitRunnerLaunchConcurrentIdenticalAdmissions(t *testing.T) {
	_, slave := requireMeasurableHost(t)
	store, _ := newTestStore(t)
	revision := canonicalRunnerRequest(t, nil)
	payload := []byte(revision.CanonicalJSON)
	slaveFd := int(slave.Fd())

	const admissions = 8
	servers := make([]int, admissions)
	for i := range servers {
		server, client := seqpacketPair(t, true)
		servers[i] = server
		if err := sendSeqpacketMessage(client, payload, slaveTriple(t, slaveFd)); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	before := ptyDescriptorCount(t)

	runs := make([]*Run, admissions)
	errs := make([]error, admissions)
	var wg sync.WaitGroup
	for i := range servers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runs[i], errs[i] = AdmitRunnerLaunch(context.Background(), servers[i], selfRunner(), store)
		}(i)
	}
	wg.Wait()

	if after := ptyDescriptorCount(t); after != before {
		t.Fatalf("admissions leaked received PTY descriptors: before=%d after=%d", before, after)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("admission %d: %v", i, err)
		}
	}
	for i, run := range runs {
		if run == nil {
			t.Fatalf("admission %d returned no run", i)
		}
		if run.RunID != runs[0].RunID {
			t.Fatalf("admission %d got run %q, want the one run %q", i, run.RunID, runs[0].RunID)
		}
	}
	reconciled, err := store.ListForReconciliation(context.Background())
	if err != nil {
		t.Fatalf("ListForReconciliation: %v", err)
	}
	if len(reconciled) != 1 || reconciled[0].RunID != runs[0].RunID {
		t.Fatalf("store holds %d runs, want exactly %q", len(reconciled), runs[0].RunID)
	}
	if reconciled[0].NeedsAttention {
		t.Fatalf("identical concurrent admission must not park the run")
	}
}
