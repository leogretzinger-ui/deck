package supervisor

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"
)

// SOCK_SEQPACKET transport primitives for the confined supervisor's two
// role-separated local endpoints. Authority over which peer is admitted is
// always the kernel credential attached per message; the socket address only
// routes a connection and is never authority. These primitives carry no
// provider content, path, caller argv/env, or message decoder: each endpoint
// owns its own canonical message shape and pinned peer identity.

const (
	// maxTransportPayload bounds one SEQPACKET message. A request or hello
	// that exceeds it is rejected as truncated rather than partially read.
	maxTransportPayload = 4096
	// transportControlSize bounds the ancillary buffer. One SCM_CREDENTIALS
	// and one three-descriptor SCM_RIGHTS record fit well inside; a larger
	// control space is rejected as control-truncated instead of dropped.
	transportControlSize = 256
)

var (
	ErrTransport          = errors.New("supervisor local transport error")
	ErrTransportReceive   = errors.New("supervisor local transport receive failed")
	ErrTransportSend      = errors.New("supervisor local transport send failed")
	ErrTransportTruncated = errors.New("supervisor local transport message truncated")
	ErrTransportAncillary = errors.New("supervisor local transport ancillary data rejected")
	ErrTransportPeer      = errors.New("supervisor local transport peer rejected")
	ErrTransportMessage   = errors.New("supervisor local transport message rejected")
	ErrTransportClosed    = errors.New("supervisor local transport endpoint closed")
)

// peerCredentialMatches reports whether kernel-supplied credentials match a
// pinned UID and PID. A nil credential or an invalid pin (PID <= 0) never
// matches; every authority-bearing admission requires a valid positive exact
// kernel PID.
func peerCredentialMatches(uid uint32, pid int32, creds *unix.Ucred) bool {
	if creds == nil {
		return false
	}
	if pid <= 0 {
		return false
	}
	if creds.Uid != uid {
		return false
	}
	if creds.Pid != pid {
		return false
	}
	return true
}

// receivedMessage is one strictly-received SEQPACKET message with its
// verified ancillary data. On a successful receive the descriptors in FDs are
// owned by the caller; every rejection path inside the receive primitive
// closes them first.
type receivedMessage struct {
	Payload []byte
	Creds   *unix.Ucred
	FDs     []int
}

// enablePassCredentials asks the kernel to attach one truthful
// SCM_CREDENTIALS record to each message received on fd. The credential a
// receive observes is fixed by the SO_PASSCRED state at send time, so the
// listener sets it on the listen socket where accepted connections inherit it
// before any peer can send; the acceptor re-enables it as a defense-in-depth
// safety net for a connection that did not inherit it.
func enablePassCredentials(fd int) error {
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		return fmt.Errorf("%w: passcred", ErrTransport)
	}
	return nil
}

// recvSeqpacketMessage reads exactly one SEQPACKET message from fd and
// strictly validates its ancillary data. SO_PASSCRED must already be set on fd
// so the kernel supplies exactly one truthful SCM_CREDENTIALS record. The
// endpoint's fixed descriptor count is expectFDs: the runner admission
// endpoint requires exactly three descriptors in exactly one SCM_RIGHTS
// record, and the launcher bootstrap endpoint requires none.
//
// The receive rejects a truncated payload or control buffer, an unknown
// control message, a missing or duplicate credential record, a missing,
// duplicate, or extra rights record, and a descriptor count other than
// expectFDs. On any rejection it closes every received descriptor and returns
// a content-free error; no path, payload byte, credential, or descriptor
// number is ever reported.
func recvSeqpacketMessage(fd int, expectFDs int) (msg *receivedMessage, err error) {
	payload := make([]byte, maxTransportPayload)
	control := make([]byte, transportControlSize)
	n, oobn, flags, _, rerr := unix.Recvmsg(fd, payload, control, unix.MSG_CMSG_CLOEXEC)
	if rerr != nil {
		if errors.Is(rerr, unix.ECONNRESET) || errors.Is(rerr, unix.EPIPE) {
			return nil, fmt.Errorf("%w: peer reset", ErrTransportClosed)
		}
		return nil, fmt.Errorf("%w: recvmsg", ErrTransportReceive)
	}
	// Extract every installed descriptor before any validation: a control
	// buffer that the kernel truncated still installs descriptors into this
	// process, and the close-on-reject guarantee must reach them.
	creds, fds, credRecords, rightsRecords, otherRecords, perr := parseControlMessages(control[:oobn])
	defer func() {
		if err != nil {
			closeAll(fds)
		}
	}()
	if perr != nil {
		return nil, fmt.Errorf("%w: malformed control", ErrTransportAncillary)
	}
	if flags&unix.MSG_TRUNC != 0 {
		return nil, fmt.Errorf("%w: payload", ErrTransportTruncated)
	}
	if flags&unix.MSG_CTRUNC != 0 {
		return nil, fmt.Errorf("%w: control", ErrTransportTruncated)
	}
	if otherRecords > 0 {
		return nil, fmt.Errorf("%w: unknown control message", ErrTransportAncillary)
	}
	if credRecords != 1 {
		return nil, fmt.Errorf("%w: credential record count", ErrTransportAncillary)
	}
	if creds == nil {
		return nil, fmt.Errorf("%w: missing credential record", ErrTransportAncillary)
	}
	if rightsRecords > 1 {
		return nil, fmt.Errorf("%w: rights record count", ErrTransportAncillary)
	}
	if len(fds) != expectFDs {
		return nil, fmt.Errorf("%w: rights descriptor count", ErrTransportAncillary)
	}
	return &receivedMessage{Payload: payload[:n], Creds: creds, FDs: fds}, nil
}

// parseControlMessages classifies one receive's ancillary buffer. It returns
// the single credential (if present), every descriptor from every rights
// record, and per-kind record counts. recvSeqpacketMessage owns the
// close-on-reject decision; this helper only reads. It is exercised both
// through a real socket and with synthetic buffers because the Linux kernel
// coalesces duplicate credential and rights records a sender emits, so the
// duplicate and unknown-control rejections are reached only via a crafted
// buffer.
func parseControlMessages(oob []byte) (creds *unix.Ucred, fds []int, credRecords, rightsRecords, otherRecords int, err error) {
	if len(oob) == 0 {
		return nil, nil, 0, 0, 0, nil
	}
	cmsgs, cerr := unix.ParseSocketControlMessage(oob)
	if cerr != nil {
		return nil, nil, 0, 0, 0, cerr
	}
	for i := range cmsgs {
		cm := &cmsgs[i]
		switch {
		case cm.Header.Level == unix.SOL_SOCKET && cm.Header.Type == unix.SCM_CREDENTIALS:
			credRecords++
			uc, perr := unix.ParseUnixCredentials(cm)
			if perr != nil {
				return nil, fds, credRecords, rightsRecords, otherRecords, perr
			}
			creds = uc
		case cm.Header.Level == unix.SOL_SOCKET && cm.Header.Type == unix.SCM_RIGHTS:
			rightsRecords++
			got, perr := unix.ParseUnixRights(cm)
			if perr != nil {
				return nil, fds, credRecords, rightsRecords, otherRecords, perr
			}
			fds = append(fds, got...)
		default:
			otherRecords++
		}
	}
	return creds, fds, credRecords, rightsRecords, otherRecords, nil
}

// sendSeqpacketMessage sends exactly one SEQPACKET message, optionally
// passing fds as a single SCM_RIGHTS record. It never attaches credentials:
// the kernel supplies the receiver-requested SCM_CREDENTIALS from this
// process's real identity, so a sender cannot forge one.
func sendSeqpacketMessage(fd int, payload []byte, fds []int) error {
	var oob []byte
	if len(fds) > 0 {
		oob = unix.UnixRights(fds...)
	}
	if err := unix.Sendmsg(fd, payload, oob, nil, 0); err != nil {
		if errors.Is(err, unix.EPIPE) || errors.Is(err, unix.ECONNRESET) {
			return fmt.Errorf("%w: peer closed", ErrTransportClosed)
		}
		return fmt.Errorf("%w: sendmsg", ErrTransportSend)
	}
	return nil
}

// closeAll closes every descriptor in fds, ignoring errors. It is the single
// owner of the close-on-reject guarantee for received descriptors.
func closeAll(fds []int) {
	for _, fd := range fds {
		_ = unix.Close(fd)
	}
}

// SeqpacketListener is a bound SOCK_SEQPACKET accept socket. Admitting a peer
// is always a kernel-credential decision made on the accepted connection; the
// bound address only routes the connection and is never authority.
type SeqpacketListener struct {
	mu      sync.Mutex
	fd      int
	address string
	closed  bool
	// accepting counts accept calls inside the kernel. Close waits for them
	// before closing the descriptor, so the descriptor number is never
	// recycled under a pending accept.
	accepting sync.WaitGroup
}

// ListenSeqpacket binds a SOCK_SEQPACKET listener at address. An address
// whose first byte is '@' uses the Linux abstract namespace, which leaves no
// filesystem path and cannot be planted or symlinked; the supervisor and the
// tests bind abstract sockets so no user path is ever touched.
func ListenSeqpacket(address string) (*SeqpacketListener, error) {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: socket", ErrTransport)
	}
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: address}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%w: bind", ErrTransport)
	}
	if err := unix.Listen(fd, 1); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%w: listen", ErrTransport)
	}
	// SO_PASSCRED on the listen socket is inherited by every accepted
	// connection, so the kernel attaches a truthful credential to each
	// message regardless of when the peer sends relative to accept.
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%w: passcred", ErrTransport)
	}
	return &SeqpacketListener{fd: fd, address: address}, nil
}

// Accept waits for one connection and returns a close-on-exec descriptor for
// it. The caller enables SO_PASSCRED and runs a role-specific receive. Close
// interrupts a blocked Accept, which then returns ErrTransportClosed.
func (l *SeqpacketListener) Accept() (int, error) {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return -1, ErrTransportClosed
		}
		l.accepting.Add(1)
		fd := l.fd
		l.mu.Unlock()

		cfd, _, err := unix.Accept4(fd, unix.SOCK_CLOEXEC)
		l.accepting.Done()
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.ECONNABORTED) {
				continue
			}
			return -1, fmt.Errorf("%w: accept", ErrTransport)
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			// The listener closed while this accept was in the kernel; its
			// pending connections die with it, and Close's wake connection
			// is never an admitted peer. Re-wake so a sibling Accept still
			// blocked in the kernel observes the closure as well.
			_ = unix.Close(cfd)
			l.wake()
			return -1, ErrTransportClosed
		}
		l.mu.Unlock()
		return cfd, nil
	}
}

// wake opens a non-blocking connection to the listener's own address to
// interrupt an Accept blocked in the kernel. It is best-effort: when the
// pending queue is full the connect fails, but that full queue is itself
// what wakes the blocked Accept.
func (l *SeqpacketListener) wake() {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return
	}
	_ = unix.Connect(fd, &unix.SockaddrUnix{Name: l.address})
	_ = unix.Close(fd)
}

// Close releases the listen socket. It is idempotent and safe to call from
// any goroutine: the descriptor is closed exactly once, after every
// in-flight Accept has left the kernel, so no recycled descriptor number is
// ever accepted on or closed twice. A blocked Accept is interrupted and
// returns ErrTransportClosed. Already-accepted connections are unaffected.
func (l *SeqpacketListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
	l.wake()
	l.accepting.Wait()
	l.mu.Lock()
	err := unix.Close(l.fd)
	l.fd = -1
	l.mu.Unlock()
	if err != nil {
		return fmt.Errorf("%w: close listener", ErrTransport)
	}
	return nil
}

// connectSeqpacket dials a bound SEQPACKET listener and returns a
// close-on-exec connection descriptor. It is the test driver's entry to a
// listener endpoint; the real runner and launcher connect through their own
// pinned process boundaries.
func connectSeqpacket(address string) (int, error) {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("%w: socket", ErrTransport)
	}
	if err := unix.Connect(fd, &unix.SockaddrUnix{Name: address}); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("%w: connect", ErrTransport)
	}
	return fd, nil
}
