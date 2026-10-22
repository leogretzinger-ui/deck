package supervisor

import "golang.org/x/sys/unix"

// launcherBootstrapDescriptorCount is the fixed number of descriptors the
// launcher bootstrap endpoint requires in its inbound hello: none. The
// launcher begins with null streams and no PTY; the supervisor later
// delivers the PTY descriptors with the authorized ticket and closed plan.
const launcherBootstrapDescriptorCount = 0

// LauncherBootstrap is the pinned identity the launcher bootstrap endpoint
// admits: the one systemd launcher process for the recorded unit. UID and a
// valid positive PID are both required; zero, negative, or otherwise invalid
// pins fail closed. The admission decision is always the kernel-reported
// credential, never a path, unit name, or caller claim.
type LauncherBootstrap struct {
	UID uint32
	PID int32
}

func (l LauncherBootstrap) matches(creds *unix.Ucred) bool {
	return peerCredentialMatches(l.UID, l.PID, creds)
}

// AcceptLauncherHello receives and authorizes one launcher bootstrap hello on
// the accepted connection fd. It admits only the pinned launcher credential
// and decodes exactly one canonical run/invocation hello carrying no
// descriptors. No ticket, closed plan, or PTY delivery happens here: this
// endpoint only authenticates the inbound hello and returns its identity.
func AcceptLauncherHello(fd int, launcher LauncherBootstrap) (*LauncherHelloRevision, error) {
	if err := enablePassCredentials(fd); err != nil {
		return nil, err
	}
	msg, recvErr := recvSeqpacketMessage(fd, launcherBootstrapDescriptorCount)
	if recvErr != nil {
		return nil, recvErr
	}
	// The primitive already closed and rejected any descriptor that arrived
	// with the hello, so a later failure leaves nothing to close.
	if !launcher.matches(msg.Creds) {
		return nil, ErrTransportPeer
	}
	hello, err := parseLauncherHelloPayload(msg.Payload)
	if err != nil {
		return nil, err
	}
	return hello, nil
}
