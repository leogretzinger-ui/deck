//go:build linux

package boot

import (
	"context"
	"path/filepath"

	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/harnesslogin"
	"github.com/noisefloor-oss/deck/internal/provider"
	"github.com/noisefloor-oss/deck/internal/session"
)

func (h *Handles) startHarnessLogin(ctx context.Context, cfg *config.Config) *harnesslogin.Service {
	if !cfg.HarnessLogin.Enabled || cfg.Auth.Mode == "none" || (cfg.Auth.Mode == "trusted_proxy" && cfg.HarnessLogin.TrustedProxyIdentity == "") {
		return nil
	}
	socket := filepath.Join(filepath.Dir(cfg.Database.Path), "harness-login.sock")
	environment := make(map[provider.Name][]string)
	for name, desc := range h.Providers {
		if env := harnesslogin.LaunchEnvironment(ctx, string(name), desc.Executable(), socket, desc.Environment()); len(env) != 0 {
			environment[name] = env
		}
	}
	if len(environment) == 0 {
		return nil
	}
	runtime := &harnesslogin.LinuxRuntime{Resolve: func(ctx context.Context, pid int, start uint64) (harnesslogin.SessionIdentity, bool) {
		active, err := h.Sessions.ListActive(ctx)
		if err != nil {
			return harnesslogin.SessionIdentity{}, false
		}
		for _, s := range active {
			if len(environment[provider.Name(s.Provider)]) == 0 || s.PanePID == nil || s.StartTicks == nil || *s.PanePID != pid {
				continue
			}
			ticks := *s.StartTicks
			if ticks < 0 || uint64(ticks) != start {
				continue
			}
			observed := h.Manager.ObserveRuntime(ctx, s)
			if observed.Pane == session.PaneAlive && observed.IdentityVerified != nil && *observed.IdentityVerified {
				identity := harnesslogin.SessionIdentity{ID: s.ID, Generation: s.Generation, Provider: string(s.Provider)}
				if s.Provider == "codex" {
					identity.CodexExecutable = harnesslogin.ShippedCodexExecutable(ctx, h.Providers[provider.Name(s.Provider)].Executable())
					if identity.CodexExecutable == "" {
						return harnesslogin.SessionIdentity{}, false
					}
				}
				return identity, true
			}
		}
		return harnesslogin.SessionIdentity{}, false
	}}
	service := harnesslogin.New(runtime)
	local, err := harnesslogin.StartLocal(socket, service)
	if err != nil {
		h.log.Warn().Msg("harness login handoff unavailable: local registration socket could not be opened")
		return nil
	}
	h.HarnessLogin = local
	h.Manager.SetHarnessLoginEnvironment(func(ctx context.Context, desc provider.Descriptor) []string {
		if len(environment[desc.Name()]) == 0 {
			return nil
		}
		return harnesslogin.LaunchEnvironment(ctx, string(desc.Name()), desc.Executable(), socket, desc.Environment())
	})
	return service
}
