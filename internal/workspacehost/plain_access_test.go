//go:build linux

package workspacehost

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacegateway"
	"github.com/rs/zerolog"
)

func plainLoginContext(t *testing.T, cfg Config, id string, expires time.Time) (context.Context, auth.Login) {
	t.Helper()
	handler, err := auth.New(cfg.Auth, zerolog.Nop())
	requireOK(t, err)
	login := auth.Login{ID: strings.Repeat(id, 64), Issuer: "https://accounts.google.com", Subject: "fixture-owner", Email: cfg.AllowedEmail}
	parts := []string{"v3", fmt.Sprint(time.Now().Add(-time.Minute).Unix()), fmt.Sprint(expires.Unix()), login.ID}
	for _, value := range []string{login.Issuer, login.Subject, login.Email} {
		parts = append(parts, base64.RawURLEncoding.EncodeToString([]byte(value)))
	}
	payload := strings.Join(parts, ":")
	mac := hmac.New(sha256.New, cfg.Auth.SessionSecret)
	_, _ = mac.Write([]byte(payload))
	r := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-noisedeck_session", Value: base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))})
	var ctx context.Context
	handler.Require(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), r)
	if ctx == nil {
		t.Fatal("synthetic login was not admitted")
	}
	return ctx, login
}

func TestPlainLogoutRevokesOnlyItsLoginAcrossRestart(t *testing.T) {
	cfg := hostConfig(t)
	db, err := workspacegateway.OpenControl(filepath.Join(cfg.Native.ControlDir, "plain-test.sqlite"))
	requireOK(t, err)
	t.Cleanup(func() { _ = db.Close() })
	authorize := func(context.Context, auth.Login) error { return nil }
	a, err := newPlainAccess(context.Background(), db, authorize)
	requireOK(t, err)
	t.Cleanup(a.Close)
	a.reconcile(true, false)
	ctxA, loginA := plainLoginContext(t, cfg, "a", time.Now().Add(time.Hour))
	ctxB, loginB := plainLoginContext(t, cfg, "b", time.Now().Add(time.Hour))
	var closedA, closedB atomic.Int32
	releaseA, err := a.Attach(ctxA, loginA, func() { closedA.Add(1) })
	requireOK(t, err)
	releaseB, err := a.Attach(ctxB, loginB, func() { closedB.Add(1) })
	requireOK(t, err)
	defer releaseB()
	requireOK(t, a.Logout(ctxA, loginA))
	releaseA()
	if closedA.Load() != 1 || closedB.Load() != 0 || a.Require(ctxA, loginA) == nil || a.Require(ctxB, loginB) != nil {
		t.Fatal("sign out did not revoke exactly its own browser")
	}
	a.Close()
	reopened, err := newPlainAccess(context.Background(), db, authorize)
	requireOK(t, err)
	defer reopened.Close()
	reopened.reconcile(true, false)
	if reopened.Require(ctxA, loginA) == nil || reopened.Require(ctxB, loginB) != nil {
		t.Fatal("restart lost the signed-out login fence")
	}
}

func TestPlainStreamExpiresWhileControlDatabaseIsBusy(t *testing.T) {
	cfg := hostConfig(t)
	db, err := workspacegateway.OpenControl(filepath.Join(cfg.Native.ControlDir, "plain-test.sqlite"))
	requireOK(t, err)
	defer db.Close()
	a, err := newPlainAccess(context.Background(), db, func(context.Context, auth.Login) error { return nil })
	requireOK(t, err)
	defer a.Close()
	a.reconcile(true, false)
	ctx, login := plainLoginContext(t, cfg, "a", time.Now().Add(2*time.Second))
	closed := make(chan struct{})
	release, err := a.Attach(ctx, login, func() { close(closed) })
	requireOK(t, err)
	defer release()
	tx, err := db.BeginTx(context.Background(), nil)
	requireOK(t, err)
	defer tx.Rollback()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("a busy control store extended a signed-in stream past cookie expiry")
	}
}

func TestPlainControlFailureClosesExistingStreams(t *testing.T) {
	cfg := hostConfig(t)
	db, err := workspacegateway.OpenControl(filepath.Join(cfg.Native.ControlDir, "plain-test.sqlite"))
	requireOK(t, err)
	a, err := newPlainAccess(context.Background(), db, func(context.Context, auth.Login) error { return nil })
	requireOK(t, err)
	defer a.Close()
	a.reconcile(true, false)
	ctx, login := plainLoginContext(t, cfg, "a", time.Now().Add(time.Hour))
	var closed atomic.Int32
	release, err := a.Attach(ctx, login, func() { closed.Add(1) })
	requireOK(t, err)
	defer release()
	requireOK(t, db.Close())
	if a.Require(ctx, login) == nil || closed.Load() != 1 {
		t.Fatal("unavailable control state left a private stream open")
	}
}

func TestPlainAttachCannotCrossFailedRuntimeObservation(t *testing.T) {
	cfg := hostConfig(t)
	db, err := workspacegateway.OpenControl(filepath.Join(cfg.Native.ControlDir, "plain-test.sqlite"))
	requireOK(t, err)
	defer db.Close()
	entered, resume := make(chan struct{}), make(chan struct{})
	a, err := newPlainAccess(context.Background(), db, func(context.Context, auth.Login) error {
		close(entered)
		<-resume
		return nil
	})
	requireOK(t, err)
	defer a.Close()
	a.reconcile(true, false)
	ctx, login := plainLoginContext(t, cfg, "a", time.Now().Add(time.Hour))
	result := make(chan error, 1)
	go func() {
		release, err := a.Attach(ctx, login, func() {})
		if release != nil {
			release()
		}
		result <- err
	}()
	<-entered
	a.reconcile(false, false)
	close(resume)
	if err := <-result; err == nil {
		t.Fatal("request attached after runtime observation failed")
	}
}

func TestPlainAbandonedReadPreservesOtherBrowser(t *testing.T) {
	for _, path := range []string{"owner", "logout"} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deadline=%t", path, deadline), func(t *testing.T) {
				cfg := hostConfig(t)
				db, err := workspacegateway.OpenControl(filepath.Join(cfg.Native.ControlDir, "plain-test.sqlite"))
				requireOK(t, err)
				defer db.Close()
				a, err := newPlainAccess(context.Background(), db, func(ctx context.Context, _ auth.Login) error {
					if path == "owner" {
						return ctx.Err()
					}
					return nil
				})
				requireOK(t, err)
				defer a.Close()
				a.reconcile(true, false)
				ctx, login := plainLoginContext(t, cfg, "a", time.Now().Add(time.Hour))
				var closed atomic.Int32
				release, err := a.Attach(ctx, login, func() { closed.Add(1) })
				requireOK(t, err)
				defer release()
				var abandoned context.Context
				var cancel context.CancelFunc
				if deadline {
					abandoned, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				} else {
					abandoned, cancel = context.WithCancel(ctx)
				}
				cancel()
				if a.Require(abandoned, login) == nil {
					t.Fatal("incomplete authorization admitted an abandoned request")
				}
				if deadline {
					if closed.Load() != 1 || a.Require(ctx, login) == nil {
						t.Fatal("control-read deadline left browser access open")
					}
				} else if closed.Load() != 0 || a.Require(ctx, login) != nil {
					t.Fatal("abandoned request permanently disabled another browser")
				}
			})
		}
	}
}
