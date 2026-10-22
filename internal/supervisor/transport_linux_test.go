package supervisor

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// seqpacketPair returns a connected SEQPACKET socketpair for direct tests of
// the receive primitive. The server end has SO_PASSCRED enabled and a short
// receive timeout so a missing send fails the test instead of hanging it; the
// client end sends. Both descriptors are close-on-exec and cleaned up.
func seqpacketPair(t *testing.T, passCred bool) (server, client int) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	for _, fd := range fds {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
			t.Fatalf("F_SETFD: %v", err)
		}
	}
	t.Cleanup(func() {
		unix.Close(fds[0])
		unix.Close(fds[1])
	})
	setTestRecvTimeout(t, fds[0])
	if passCred {
		if err := enablePassCredentials(fds[0]); err != nil {
			t.Fatalf("enablePassCredentials: %v", err)
		}
	}
	return fds[0], fds[1]
}

// setTestRecvTimeout bounds a receive so a test that forgets to send fails
// fast instead of hanging the suite.
func setTestRecvTimeout(t *testing.T, fd int) {
	t.Helper()
	tv := unix.NsecToTimeval(int64(2 * time.Second))
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		t.Fatalf("SO_RCVTIMEO: %v", err)
	}
}

// selfRunner returns a runner admission identity for this test process.
func selfRunner() RunnerAdmission {
	return RunnerAdmission{UID: uint32(unix.Getuid()), PID: int32(unix.Getpid())}
}

// selfLauncher returns a launcher bootstrap identity for this test process.
func selfLauncher() LauncherBootstrap {
	return LauncherBootstrap{UID: uint32(unix.Getuid()), PID: int32(unix.Getpid())}
}

// pipeFDs returns n distinct close-on-exec pipe read descriptors for the
// primitive-level descriptor-count tests; they are not PTYs and never reach
// PTY measurement. Both original ends are cleaned up; SCM_RIGHTS duplicates
// installed in the receiver remain independently owned.
func pipeFDs(t *testing.T, n int) []int {
	t.Helper()
	fds := make([]int, n)
	for i := range fds {
		var p [2]int
		if err := unix.Pipe(p[:]); err != nil {
			t.Fatalf("pipe: %v", err)
		}
		for _, fd := range p {
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
				t.Fatalf("F_SETFD: %v", err)
			}
		}
		readFD, writeFD := p[0], p[1]
		fds[i] = readFD
		t.Cleanup(func() {
			_ = unix.Close(readFD)
			_ = unix.Close(writeFD)
		})
	}
	return fds
}

func fdIsOpen(fd int) bool {
	_, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	return err == nil
}

// openFDCount reports how many descriptors this process has open. The
// close-on-reject guarantee is about the receiver's installed copies (new fd
// numbers the kernel assigns at recvmsg), not the sender's originals, so a
// leak is detected as an open-fd-count delta across a rejecting receive.
func openFDCount(t *testing.T) int {
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
	return len(names)
}

func TestPeerCredentialMatches(t *testing.T) {
	uid := uint32(unix.Getuid())
	pid := int32(unix.Getpid())
	creds := &unix.Ucred{Pid: pid, Uid: uid, Gid: uint32(unix.Getgid())}
	if peerCredentialMatches(uid, pid, nil) {
		t.Fatalf("nil credential must not match")
	}
	tests := []struct {
		name string
		uid  uint32
		pid  int32
		want bool
	}{
		{"exact uid and pid", uid, pid, true},
		{"uid only (pid zero)", uid, 0, false},
		{"negative pid", uid, -1, false},
		{"wrong uid", uid + 1, pid, false},
		{"right uid wrong pid", uid, 1, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := peerCredentialMatches(test.uid, test.pid, creds); got != test.want {
				t.Fatalf("matches = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRoleAdmissionsRequireExactPin(t *testing.T) {
	creds := &unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(unix.Getuid()), Gid: uint32(unix.Getgid())}
	uid := uint32(unix.Getuid())
	pid := int32(unix.Getpid())
	if !selfRunner().matches(creds) || !selfLauncher().matches(creds) {
		t.Fatalf("exact pin must match both roles")
	}
	// A UID-only pin is never authority for either role.
	if (RunnerAdmission{UID: uid}).matches(creds) || (LauncherBootstrap{UID: uid}).matches(creds) {
		t.Fatalf("uid-only pin must not match")
	}
	// The same UID under a different process is never authority.
	if (RunnerAdmission{UID: uid, PID: pid + 1}).matches(creds) || (LauncherBootstrap{UID: uid, PID: pid + 1}).matches(creds) {
		t.Fatalf("same uid with different pid must not match")
	}
}

func TestRecvSeqpacketCanonical(t *testing.T) {
	server, client := seqpacketPair(t, true)
	want := []byte("canonical payload")
	sent := pipeFDs(t, 3)
	if err := sendSeqpacketMessage(client, want, sent); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg, err := recvSeqpacketMessage(server, 3)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if string(msg.Payload) != string(want) {
		t.Fatalf("payload mismatch")
	}
	if msg.Creds == nil {
		t.Fatalf("missing credential")
	}
	if msg.Creds.Uid != uint32(unix.Getuid()) {
		t.Fatalf("credential uid mismatch")
	}
	if len(msg.FDs) != 3 {
		t.Fatalf("fd count = %d, want 3", len(msg.FDs))
	}
	for _, fd := range msg.FDs {
		if !fdIsOpen(fd) {
			t.Fatalf("received descriptor unexpectedly closed")
		}
		unix.Close(fd)
	}
}

// runRejectingReceive sends sent descriptors from the client, receives on the
// server expecting expectFDs, asserts the receive fails with want, and
// verifies the receiver closed every descriptor it installed (no leak).
func runRejectingReceive(t *testing.T, passCred bool, payload []byte, sent []int, expectFDs int, want error) {
	t.Helper()
	server, client := seqpacketPair(t, passCred)
	before := openFDCount(t)
	if err := sendSeqpacketMessage(client, payload, sent); err != nil {
		t.Fatalf("send: %v", err)
	}
	_, err := recvSeqpacketMessage(server, expectFDs)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if after := openFDCount(t); after != before {
		t.Fatalf("receiver leaked descriptors: before=%d after=%d", before, after)
	}
}

func TestRecvSeqpacketRejectsMissingCredential(t *testing.T) {
	runRejectingReceive(t, false, []byte("no cred"), pipeFDs(t, 3), 3, ErrTransportAncillary)
}

func TestRecvSeqpacketRejectsDescriptorCount(t *testing.T) {
	cases := []struct {
		name      string
		sendFDs   int
		expectFDs int
	}{
		{"too few", 2, 3},
		{"too many", 4, 3},
		{"none when three expected", 0, 3},
		{"one when none expected", 1, 0},
		{"three when none expected", 3, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runRejectingReceive(t, true, []byte("count"), pipeFDs(t, tc.sendFDs), tc.expectFDs, ErrTransportAncillary)
		})
	}
}

func TestRecvSeqpacketRejectsPayloadTruncation(t *testing.T) {
	server, client := seqpacketPair(t, true)
	if err := sendSeqpacketMessage(client, make([]byte, maxTransportPayload+1), nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := recvSeqpacketMessage(server, 0); !errors.Is(err, ErrTransportTruncated) {
		t.Fatalf("error = %v, want ErrTransportTruncated", err)
	}
}

func TestRecvSeqpacketRejectsControlTruncation(t *testing.T) {
	// Enough descriptors to overflow the fixed control buffer. The kernel
	// installs the descriptors that fit; the receive must close every one it
	// parsed and the kernel closes the truncated remainder.
	runRejectingReceive(t, true, []byte("ctrunc"), pipeFDs(t, transportControlSize/4), transportControlSize/4, ErrTransportTruncated)
}

func TestRecvSeqpacketZeroDescriptorsExpected(t *testing.T) {
	server, client := seqpacketPair(t, true)
	if err := sendSeqpacketMessage(client, []byte("hello"), nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg, err := recvSeqpacketMessage(server, 0)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if msg.Creds == nil || len(msg.FDs) != 0 {
		t.Fatalf("unexpected message shape")
	}
}

// syntheticControlMessage builds one well-formed control message with an
// arbitrary level/type, so the duplicate and unknown-control classification
// branches can be exercised directly. The Linux kernel coalesces duplicate
// credential and rights records a sender emits, so those branches are reached
// only through a crafted buffer, never over a real socket.
func syntheticControlMessage(level, typ int, data []byte) []byte {
	buf := make([]byte, unix.CmsgSpace(len(data)))
	hdr := (*unix.Cmsghdr)(unsafe.Pointer(&buf[0]))
	hdr.Len = uint64(unix.CmsgLen(len(data)))
	hdr.Level = int32(level)
	hdr.Type = int32(typ)
	copy(buf[unix.CmsgLen(0):], data)
	return buf
}

func TestParseControlMessagesClassifiesRecords(t *testing.T) {
	self := &unix.Ucred{Pid: 1, Uid: 2, Gid: 3}
	credBytes := func() []byte { return unix.UnixCredentials(self) }
	rightsBytes := func(n int) []byte {
		fds := make([]int, n)
		for i := range fds {
			fds[i] = i
		}
		return unix.UnixRights(fds...)
	}

	t.Run("single credential and rights", func(t *testing.T) {
		oob := append(credBytes(), rightsBytes(3)...)
		creds, fds, cred, rights, other, err := parseControlMessages(oob)
		if err != nil || creds == nil || cred != 1 || rights != 1 || other != 0 || len(fds) != 3 {
			t.Fatalf("classify single: creds=%v fds=%d cred=%d rights=%d other=%d err=%v",
				creds, len(fds), cred, rights, other, err)
		}
	})

	t.Run("duplicate credentials", func(t *testing.T) {
		oob := append(credBytes(), credBytes()...)
		_, _, cred, _, _, err := parseControlMessages(oob)
		if err != nil || cred != 2 {
			t.Fatalf("classify dup creds: cred=%d err=%v", cred, err)
		}
	})

	t.Run("duplicate rights records", func(t *testing.T) {
		oob := append(rightsBytes(3), rightsBytes(3)...)
		_, fds, _, rights, _, err := parseControlMessages(oob)
		if err != nil || rights != 2 || len(fds) != 6 {
			t.Fatalf("classify dup rights: rights=%d fds=%d err=%v", rights, len(fds), err)
		}
	})

	t.Run("unknown control message", func(t *testing.T) {
		oob := syntheticControlMessage(unix.SOL_SOCKET, 0x9999, []byte{1, 2, 3, 4})
		_, _, _, _, other, err := parseControlMessages(oob)
		if err != nil || other != 1 {
			t.Fatalf("classify unknown: other=%d err=%v", other, err)
		}
	})

	t.Run("empty buffer", func(t *testing.T) {
		creds, fds, cred, rights, other, err := parseControlMessages(nil)
		if err != nil || creds != nil || len(fds) != 0 || cred != 0 || rights != 0 || other != 0 {
			t.Fatalf("classify empty: err=%v", err)
		}
	})
}

// testListenerAddress returns an abstract socket name that is unique to this
// process and test name so concurrent test binaries cannot collide.
func testListenerAddress(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("@noisedeck-test-listener-%d-%d-%s", os.Getpid(), time.Now().UnixNano(), t.Name())
}

func TestSeqpacketListenerAcceptsAndInheritsPassCred(t *testing.T) {
	address := testListenerAddress(t)
	listener, err := ListenSeqpacket(address)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	// The peer connects and sends immediately, before the server accepts, to
	// exercise the inherited-SO_PASSCRED race freedom.
	client, err := connectSeqpacket(address)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer unix.Close(client)
	if err := sendSeqpacketMessage(client, []byte("race"), nil); err != nil {
		t.Fatalf("send: %v", err)
	}

	server, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer unix.Close(server)

	msg, err := recvSeqpacketMessage(server, 0)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if msg.Creds == nil || msg.Creds.Uid != uint32(unix.Getuid()) {
		t.Fatalf("credential not inherited from listen socket")
	}
	if string(msg.Payload) != "race" {
		t.Fatalf("payload mismatch")
	}
}

func TestSendSeqpacketReportsPeerClosed(t *testing.T) {
	server, client := seqpacketPair(t, true)
	unix.Close(server)
	if err := sendSeqpacketMessage(client, []byte("gone"), nil); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("error = %v, want ErrTransportClosed", err)
	}
}

func TestSeqpacketListenerCloseIdempotent(t *testing.T) {
	// Repeatedly create a listener and close it from many goroutines. The
	// descriptor must be closed exactly once; a reused descriptor must never be
	// double-closed by a late goroutine.
	for i := 0; i < 200; i++ {
		l, err := ListenSeqpacket(testListenerAddress(t))
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		var wg sync.WaitGroup
		for j := 0; j < 10; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = l.Close()
			}()
		}
		wg.Wait()
		if err := l.Close(); err != nil {
			t.Fatalf("second close: %v", err)
		}
	}
}

func TestSeqpacketListenerAcceptCloseRace(t *testing.T) {
	// Repeatedly race Accept against Close. Accept must return promptly (not
	// hang) and must never accept on a recycled descriptor. Odd iterations
	// close while Accept is blocked in the kernel; even iterations close
	// before Accept necessarily reaches the kernel.
	for i := 0; i < 200; i++ {
		l, err := ListenSeqpacket(testListenerAddress(t))
		if err != nil {
			t.Fatalf("listen: %v", err)
		}

		done := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(done)
			_, err := l.Accept()
			if err != nil && !errors.Is(err, ErrTransportClosed) {
				t.Errorf("Accept error = %v, want ErrTransportClosed", err)
			}
		}()

		if i%2 == 1 {
			time.Sleep(2 * time.Millisecond)
		}
		if err := l.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("Accept did not return after Close in iteration %d", i)
		}
		wg.Wait()
	}
}

func TestSeqpacketListenerCloseWakesAllAccepts(t *testing.T) {
	// Every Accept blocked in the kernel must observe the close, not just the
	// one that receives the wake connection.
	for i := 0; i < 100; i++ {
		l, err := ListenSeqpacket(testListenerAddress(t))
		if err != nil {
			t.Fatalf("listen: %v", err)
		}

		done := make(chan struct{})
		var wg sync.WaitGroup
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := l.Accept(); !errors.Is(err, ErrTransportClosed) {
					t.Errorf("Accept error = %v, want ErrTransportClosed", err)
				}
			}()
		}
		go func() {
			wg.Wait()
			close(done)
		}()

		time.Sleep(2 * time.Millisecond)
		if err := l.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("an Accept did not return after Close in iteration %d", i)
		}
	}
}

func TestSeqpacketListenerAcceptAfterClose(t *testing.T) {
	l, err := ListenSeqpacket(testListenerAddress(t))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := l.Accept(); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("Accept after Close error = %v, want ErrTransportClosed", err)
	}
}
