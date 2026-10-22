//go:build linux

package workspacehost

import (
	"context"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
)

func TestModeReadCancellationPreservesAccessButStorageFailureRevokes(t *testing.T) {
	for _, protection := range []string{"encrypted", "none"} {
		for _, failure := range []string{"canceled", "deadline", "closed-store"} {
			t.Run(protection+"/"+failure, func(t *testing.T) {
				var f *assemblyFixture
				var b *assemblyBrowser
				if protection == "encrypted" {
					f = newAssembly(t)
					b = f.browser("a")
					f.initialize(b)
				} else {
					f, _ = plainAssembly(t, workspacekeys.EncryptionOptional)
					b = f.browser("a")
					f.setupLink(b)
					b.call("POST", "/api/security/v1/onboarding/protection", map[string]string{"protection": "none"}, 200, nil)
					b.call("POST", "/api/security/v1/onboarding/start", map[string]string{}, 200, nil)
				}
				connection := b.websocket()
				ctx := context.Background()
				switch failure {
				case "canceled":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				case "deadline":
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer cancel()
				case "closed-store":
					requireOK(t, f.h.db.Close())
				}
				if _, err := f.h.mode.protection(ctx); err == nil {
					t.Fatal("unreadable protection state was accepted")
				}
				if failure != "canceled" {
					requireClosedSocket(t, connection)
					b.call("GET", "/api/sessions", nil, 403, nil)
					return
				}
				b.call("GET", "/api/sessions", nil, 200, nil)
				requireOK(t, connection.SetReadDeadline(time.Now().Add(2*time.Second)))
				requireOK(t, connection.WriteMessage(websocket.TextMessage, []byte("still connected")))
				_, body, err := connection.ReadMessage()
				requireOK(t, err)
				if string(body) != "still connected" {
					t.Fatal("abandoned request disrupted the existing browser stream")
				}
			})
		}
	}
}
