package session

import (
	"testing"

	"github.com/noisefloor-oss/deck/internal/provider"
)

func TestFreshRelaunchOnlyNeverSelectsNativeResume(t *testing.T) {
	d := &fakeDescriptor{name: provider.Cline, contract: provider.IdentityFreshRelaunch}
	unexpected := "unexpected-native-id"
	for _, native := range []*string{nil, &unexpected} {
		noIdentity, fresh := launchShape(&Session{NativeSessionID: native}, d)
		if !fresh {
			t.Fatalf("native id %v selected resume; fresh-only must always relaunch", native)
		}
		if noIdentity != (native == nil) {
			t.Fatalf("noIdentity = %v for native id %v", noIdentity, native)
		}
	}
}
