package workspacehost

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacegateway"
	"github.com/noisefloor-oss/deck/internal/workspacegrants"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
	"github.com/noisefloor-oss/deck/internal/workspaceproxy"
	"github.com/noisefloor-oss/deck/internal/workspaceservice"
)

type gatewayMode struct {
	keys           *workspacekeys.Registry
	encrypted      *workspaceservice.Service
	grants         *workspacegrants.Store
	plain          *plainService
	encryptedProxy *workspaceproxy.Handler
	plainProxy     *workspaceproxy.Handler
}

func (m *gatewayMode) protection(ctx context.Context) (workspacekeys.Protection, error) {
	state, err := m.keys.Onboarding(ctx)
	if err != nil && !canceledRead(ctx, err) {
		m.grants.RevokeRegistryGrants(workspacekeys.GrantRevocation{StorageFailed: true})
		if m.plain != nil {
			m.plain.access.Close()
		}
	}
	return state.Protection, err
}

// A disconnected caller cannot establish a control-store fault. Deadlines still
// fail closed so a stuck store cannot leave browser access open indefinitely.
func canceledRead(ctx context.Context, err error) bool {
	return errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled)
}

func (m *gatewayMode) ObserveSecurity(ctx context.Context) (workspacegateway.RuntimeState, error) {
	mode, err := m.protection(ctx)
	if err == nil {
		switch mode {
		case workspacekeys.ProtectionEncrypted:
			if m.encrypted != nil {
				return m.encrypted.ObserveSecurity(ctx)
			}
		case workspacekeys.ProtectionNone:
			if m.plain != nil {
				return m.plain.ObserveSecurity(ctx)
			}
		}
	}
	return workspacegateway.RuntimeState{Storage: "unknown", Runtime: "unknown"}, ErrUnavailable
}

func (m *gatewayMode) Require(ctx context.Context, login auth.Login) error {
	mode, err := m.protection(ctx)
	if err == nil {
		if mode == workspacekeys.ProtectionEncrypted && m.encrypted != nil {
			return m.grants.Require(ctx, login)
		}
		if mode == workspacekeys.ProtectionNone && m.plain != nil {
			return m.plain.access.Require(ctx, login)
		}
	}
	return workspacekeys.ErrDenied
}

func (m *gatewayMode) Attach(ctx context.Context, login auth.Login, closeStream func()) (func(), error) {
	mode, err := m.protection(ctx)
	if err == nil {
		if mode == workspacekeys.ProtectionEncrypted && m.encrypted != nil {
			return m.grants.Attach(ctx, login, closeStream)
		}
		if mode == workspacekeys.ProtectionNone && m.plain != nil {
			return m.plain.access.Attach(ctx, login, closeStream)
		}
	}
	return nil, workspacekeys.ErrDenied
}

func (m *gatewayMode) Logout(ctx context.Context, login auth.Login) error {
	mode, err := m.protection(ctx)
	if err != nil {
		return err
	}
	if mode == workspacekeys.ProtectionNone && m.plain != nil {
		return m.plain.access.Logout(ctx, login)
	}
	return m.grants.Logout(ctx, login)
}

func (m *gatewayMode) Execute(ctx context.Context, login auth.Login, permit workspacekeys.AuthorizedOperation, secret []byte) (*workspacegateway.BrowserAuthorization, error) {
	mode, err := m.protection(ctx)
	if err == nil && mode == workspacekeys.ProtectionEncrypted && m.encrypted != nil {
		return m.encrypted.Execute(ctx, login, permit, secret)
	}
	clear(secret)
	return nil, workspacekeys.ErrDenied
}

func (m *gatewayMode) Status(ctx context.Context, login auth.Login, id string) (workspacegateway.OperationState, error) {
	mode, err := m.protection(ctx)
	if err == nil && mode == workspacekeys.ProtectionEncrypted && m.encrypted != nil {
		return m.encrypted.Status(ctx, login, id)
	}
	return workspacegateway.OperationState{}, workspacekeys.ErrDenied
}

func (m *gatewayMode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mode, err := m.protection(r.Context())
	if err == nil {
		if mode == workspacekeys.ProtectionEncrypted && m.encryptedProxy != nil {
			m.encryptedProxy.ServeHTTP(w, r)
			return
		}
		if mode == workspacekeys.ProtectionNone && m.plainProxy != nil {
			m.plainProxy.ServeHTTP(w, r)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, "{\"code\":\"unlock-required\"}\n")
}
