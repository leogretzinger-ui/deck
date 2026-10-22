package supervisor

import (
	"encoding/json"
	"strings"

	"golang.org/x/sys/unix"
)

// runnerPTYDescriptorCount is the fixed number of PTY-slave descriptors the
// runner admission endpoint requires in its one SCM_RIGHTS record. The
// runner attaches stdin, stdout, and stderr of the one provider pane.
const runnerPTYDescriptorCount = 3

// RunnerAdmission is the pinned identity the runner admission endpoint
// admits: the one Deck runner process. UID and a valid positive PID are both
// required; zero, negative, or otherwise invalid pins fail closed. The
// admission decision is always the kernel-reported credential, never a path
// or caller claim.
type RunnerAdmission struct {
	UID uint32
	PID int32
}

func (r RunnerAdmission) matches(creds *unix.Ucred) bool {
	return peerCredentialMatches(r.UID, r.PID, creds)
}

// AcceptRunnerRequest receives and authorizes one runner admission message on
// the accepted connection fd. It admits only the pinned runner credential,
// decodes exactly one canonical launch request, and derives the PTY binding
// from the three PTY-slave descriptors the runner attached.
//
// On success the returned descriptors are open and owned by the caller. On
// any rejection the connection's received descriptors are closed and a
// content-free error is returned; the message is never partially accepted.
func AcceptRunnerRequest(fd int, runner RunnerAdmission) (req *LaunchRequestRevision, pty *PTYBindingRevision, fds []int, err error) {
	if err = enablePassCredentials(fd); err != nil {
		return nil, nil, nil, err
	}
	msg, recvErr := recvSeqpacketMessage(fd, runnerPTYDescriptorCount)
	if recvErr != nil {
		return nil, nil, nil, recvErr
	}
	fds = msg.FDs
	defer func() {
		if err != nil {
			closeAll(msg.FDs)
			fds = nil
		}
	}()
	if !runner.matches(msg.Creds) {
		err = ErrTransportPeer
		return nil, nil, nil, err
	}
	req, err = parseLaunchRequestPayload(msg.Payload)
	if err != nil {
		return nil, nil, nil, err
	}
	pty, err = MeasurePTYBinding(msg.FDs[0], msg.FDs[1], msg.FDs[2])
	if err != nil {
		return nil, nil, nil, err
	}
	return req, pty, fds, nil
}

// parseLaunchRequestPayload decodes canonical bytes received over the
// transport into a verified launch request. It rejects any non-canonical
// encoding so a reordered, duplicate, trailing, unknown, or otherwise
// reserialized body never becomes a different-but-equal identity. Decode and
// canonicalize failures are wrapped content-free: a malformed JSON error can
// carry payload bytes, which the transport contract never reports.
func parseLaunchRequestPayload(payload []byte) (*LaunchRequestRevision, error) {
	body := string(payload)
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	var request LaunchRequestV1
	if err := decoder.Decode(&request); err != nil {
		return nil, ErrTransportMessage
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, ErrTransportMessage
	}
	revision, err := CanonicalizeLaunchRequest(request)
	if err != nil {
		return nil, ErrTransportMessage
	}
	if revision.CanonicalJSON != body {
		return nil, ErrTransportMessage
	}
	return revision, nil
}
