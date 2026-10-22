package session

import (
	"errors"
	"fmt"

	"github.com/noisefloor-oss/deck/internal/tmux"
)

// ErrBootEpochStale reports a recorded boot epoch that is not the current
// host boot. Every boot-relative member of the recorded identity tuple —
// pid, start ticks, process group — is meaningless against the running
// system, so no match on them may be verified, signalled, or recovered
// from.
var ErrBootEpochStale = errors.New("session: recorded identity predates the current host boot")

// ErrBootEpochUnknown reports a legacy identity tuple that never recorded
// which boot produced it. The tuple cannot be trusted on any later
// observation because pid and start ticks may have been reused after a
// reboot or host migration.
var ErrBootEpochUnknown = errors.New("session: recorded identity has no boot epoch")

// VerifyBootEpoch compares the recorded boot epoch against the current
// host boot. A nil recorded epoch predates the column and cannot support
// an identity claim, so it fails closed like an unreadable current epoch.
// A differing epoch is an identity mismatch wrapping ErrBootEpochStale.
func VerifyBootEpoch(s *Session) error {
	if s.BootEpoch == nil {
		return fmt.Errorf("%w: %w", ErrIdentityMismatch, ErrBootEpochUnknown)
	}
	current, err := tmux.BootID()
	if err != nil {
		return fmt.Errorf("%w: current host boot epoch unreadable: %w", ErrIdentityMismatch, err)
	}
	if current != *s.BootEpoch {
		return fmt.Errorf("%w: %w: recorded %s, current host boot %s",
			ErrIdentityMismatch, ErrBootEpochStale, *s.BootEpoch, current)
	}
	return nil
}

// currentBootEpoch samples the host boot epoch before a process is spawned.
// Launching without it would create an identity tuple the service can never
// safely verify, so an unavailable epoch is a launch refusal.
func currentBootEpoch() (string, error) {
	id, err := tmux.BootID()
	if err != nil {
		return "", fmt.Errorf("session: read current boot epoch: %w", err)
	}
	return id, nil
}
