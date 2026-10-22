//go:build !linux

package conformance

import "testing"

func TestCodexProviderContract(t *testing.T) {
	t.Skip("the real Codex provider-contract probe requires Linux process identities")
}
