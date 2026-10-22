package supervisor

import (
	"errors"
	"fmt"
	"runtime"
)

var (
	ErrUnsupportedBubblewrapTarget  = errors.New("unsupported bubblewrap target")
	ErrBubblewrapExecutableMismatch = errors.New("bubblewrap executable mismatch")
)

// pinnedBubblewrapExecutableSHA256 is the closed executable identity of the
// Bubblewrap build confined-linux-v1 admits, keyed by GOOS/GOARCH.
// linux/amd64 is the only supported target; the digest is the proven
// output of the deploy/bwrap recipe: two independent clean builds inside
// the digest-pinned builder container reproduced each byte, and the
// executable is statically linked with no PT_INTERP and no DT_NEEDED
// entry. The release archive pin in PinnedBubblewrapArchiveSHA256 is
// source provenance only and is never a member of this map; it cannot
// substitute for an executable digest. Adding a target is an explicit code
// change, not data-plane input.
var pinnedBubblewrapExecutableSHA256 = map[string]string{
	"linux/amd64": "7710b19d89bedbdd3b1dd8ac46643f188f8fdc92917fe3093abd827eb783cf7f",
}

// ResolveBubblewrapExecutableSHA256 returns the pinned executable digest for
// one target. Every GOOS/GOARCH outside the closed map fails closed with the
// fixed ErrUnsupportedBubblewrapTarget sentinel; the caller's strings are
// never interpolated into the error.
func ResolveBubblewrapExecutableSHA256(goos, goarch string) (string, error) {
	digest, ok := pinnedBubblewrapExecutableSHA256[goos+"/"+goarch]
	if !ok {
		return "", ErrUnsupportedBubblewrapTarget
	}
	return digest, nil
}

// VerifyBubblewrapExecutableSHA256 equality-checks a supervisor-observed
// executable digest against the closed pin for one target. The check selects
// nothing from the caller: the target names a compiled pin, the observed
// value must equal it exactly, and any other input — malformed, foreign,
// or the release archive digest — is a mismatch.
func VerifyBubblewrapExecutableSHA256(goos, goarch, observedSHA256 string) error {
	pin, err := ResolveBubblewrapExecutableSHA256(goos, goarch)
	if err != nil {
		return err
	}
	if !isLowerSHA256(observedSHA256) {
		return fmt.Errorf("%w: malformed executable digest", ErrBubblewrapExecutableMismatch)
	}
	if observedSHA256 != pin {
		return fmt.Errorf("%w: executable digest", ErrBubblewrapExecutableMismatch)
	}
	return nil
}

// CurrentBubblewrapExecutableSHA256 returns the pin for the running target.
// It is the identity an attestation's observed executable digest must equal.
func CurrentBubblewrapExecutableSHA256() (string, error) {
	return ResolveBubblewrapExecutableSHA256(runtime.GOOS, runtime.GOARCH)
}
