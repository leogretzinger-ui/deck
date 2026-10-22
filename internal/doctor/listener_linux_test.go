//go:build linux

package doctor

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/noisefloor-oss/deck/internal/lock"
)

func TestRecognizesThisExecutableHoldingItsLockAndListener(t *testing.T) {
	for _, network := range []string{"tcp", "unix"} {
		t.Run(network, func(t *testing.T) {
			cfg, _ := healthy(t)
			cfg.Database.LockPath = filepath.Join(t.TempDir(), "deck.lock")
			address := "127.0.0.1:0"
			if network == "unix" {
				address = filepath.Join(t.TempDir(), "deck.sock")
				cfg.Server.UnixSocket = address
			}
			ln, err := net.Listen(network, address)
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			if network == "tcp" {
				cfg.Server.Bind = ln.Addr().String()
			}
			if ownedListener(cfg) {
				t.Fatal("accepted listener without a held lock")
			}
			held, err := lock.Acquire(cfg.Database.LockPath)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Release()
			if !ownedListener(cfg) || !listener(cfg, false).OK {
				t.Fatal("did not recognize this Deck's listener")
			}
			held.Release()
			if ownedListener(cfg) {
				t.Fatal("accepted released lock")
			}
		})
	}
}
