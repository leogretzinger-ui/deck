package config

import (
	"net/url"
	"path/filepath"
	"runtime"
)

func (c *Config) validatePrivateSocket(add func(string, ...any)) {
	s := c.Server
	if runtime.GOOS != "linux" {
		add("server.unix_socket requires Linux kernel peer credentials")
	}
	if s.Bind != "" {
		add("server.unix_socket requires an empty server.bind; a TCP listener would bypass the gateway")
	}
	if !filepath.IsAbs(s.UnixSocket) || filepath.Clean(s.UnixSocket) != s.UnixSocket || len(s.UnixSocket) > 103 {
		add("server.unix_socket requires a canonical absolute path no longer than 103 bytes")
	}
	if s.GatewayUID <= 0 || uint64(s.GatewayUID) > uint64(^uint32(0)) {
		add("server.gateway_uid must identify a non-root dedicated runtime owner")
	}
	if c.Auth.Mode != "oidc" {
		add("server.unix_socket requires oidc authentication forwarded by the workspace gateway")
	}
	if !c.Sealed.RequireEncryptedState || c.Sealed.Profile != "shared-host" {
		add("server.unix_socket requires the verified shared-host encrypted-state gate")
	}
	for _, origin := range s.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" {
			add("server.unix_socket requires HTTPS browser origins")
		}
	}
	if c.ProjectPreviews.Loopback || c.ProjectPreviews.Bind != "" {
		add("server.unix_socket refuses the alternate project preview listener; workspace grant forwarding is not configured for it")
	}
	if c.Experiments.UnconfinedCapacityRouting {
		add("server.unix_socket does not support the TCP capacity-routing experiment")
	}
}
