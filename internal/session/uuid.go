package session

import (
	"crypto/rand"
	"fmt"
)

// newUUIDv4 generates a canonical random UUID. Claude's identity contract
// requires a canonical UUID for --session-id (docs/provider-contracts/
// claude.yaml, id_format: canonical-uuid); a dependency is not worth
// sixteen random bytes and two bit masks.
func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
