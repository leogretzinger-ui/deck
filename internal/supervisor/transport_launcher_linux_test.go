package supervisor

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAcceptLauncherHelloCanonical(t *testing.T) {
	rev, err := CanonicalizeLauncherHello(validLauncherHello())
	if err != nil {
		t.Fatalf("canonicalize hello: %v", err)
	}
	server, client := seqpacketPair(t, true)
	if err := sendSeqpacketMessage(client, []byte(rev.CanonicalJSON), nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	hello, err := AcceptLauncherHello(server, selfLauncher())
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if hello.Digest != rev.Digest {
		t.Fatalf("hello digest mismatch")
	}
	if hello.Hello.RunID != rev.Hello.RunID {
		t.Fatalf("run id mismatch")
	}
}

func sendRejectingLauncher(t *testing.T, peer LauncherBootstrap, payload []byte, descriptorCount int, want error) {
	t.Helper()
	_, slave := requireMeasurableHost(t)
	server, client := seqpacketPair(t, true)
	var sent []int
	if descriptorCount > 0 {
		sent = slaveTriple(t, int(slave.Fd()))
	}
	before := openFDCount(t)
	if err := sendSeqpacketMessage(client, payload, sent); err != nil {
		t.Fatalf("send: %v", err)
	}
	_, err := AcceptLauncherHello(server, peer)
	if want != nil && !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if want == nil && err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if after := openFDCount(t); after != before {
		t.Fatalf("receiver leaked descriptors: before=%d after=%d", before, after)
	}
}

func TestAcceptLauncherHelloRejectsWrongPeer(t *testing.T) {
	rev, err := CanonicalizeLauncherHello(validLauncherHello())
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	wrongUID := LauncherBootstrap{UID: uint32(unix.Getuid()) + 1, PID: int32(unix.Getpid())}
	sendRejectingLauncher(t, wrongUID, []byte(rev.CanonicalJSON), 0, ErrTransportPeer)
}

func TestAcceptLauncherHelloRejectsSameUIDDifferentPID(t *testing.T) {
	rev, err := CanonicalizeLauncherHello(validLauncherHello())
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	wrongPID := LauncherBootstrap{UID: uint32(unix.Getuid()), PID: int32(unix.Getpid()) + 9999}
	sendRejectingLauncher(t, wrongPID, []byte(rev.CanonicalJSON), 0, ErrTransportPeer)
}

func TestAcceptLauncherHelloRejectsNonCanonicalMessage(t *testing.T) {
	rev, err := CanonicalizeLauncherHello(validLauncherHello())
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	cases := []struct {
		name    string
		payload []byte
	}{
		{"malformed", []byte("{not json")},
		{"trailing value", []byte(rev.CanonicalJSON + "{}")},
		{"unknown field", []byte(strings.TrimSuffix(rev.CanonicalJSON, "}") + `,"extra":1}`)},
		{"reordered", []byte(reorderLauncherHelloJSON(rev.CanonicalJSON))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sendRejectingLauncher(t, selfLauncher(), tc.payload, 0, ErrLauncherHelloEncoding)
		})
	}
}

func TestAcceptLauncherHelloRejectsCrossRoleMessage(t *testing.T) {
	// A launch request is valid JSON but not a hello shape; the launcher
	// endpoint's own decoder rejects it, so a role cannot speak to the other
	// endpoint's message shape.
	req, err := CanonicalizeLaunchRequest(validLaunchRequest())
	if err != nil {
		t.Fatalf("canonicalize request: %v", err)
	}
	sendRejectingLauncher(t, selfLauncher(), []byte(req.CanonicalJSON), 0, ErrLauncherHelloEncoding)
}

func TestAcceptLauncherHelloRejectsDescriptors(t *testing.T) {
	// The launcher hello carries no descriptors; any attached descriptors are
	// rejected and closed.
	rev, err := CanonicalizeLauncherHello(validLauncherHello())
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	sendRejectingLauncher(t, selfLauncher(), []byte(rev.CanonicalJSON), runnerPTYDescriptorCount, ErrTransportAncillary)
}

// reorderLauncherHelloJSON produces a valid-JSON, semantically-equal but
// non-canonical body by swapping the first two fields.
func reorderLauncherHelloJSON(canonical string) string {
	return strings.Replace(canonical,
		`{"schema":"noisedeck.supervisor-launcher-hello.v1","run_id":"123e4567-e89b-42d3-a456-426614174000",`,
		`{"run_id":"123e4567-e89b-42d3-a456-426614174000","schema":"noisedeck.supervisor-launcher-hello.v1",`, 1)
}
