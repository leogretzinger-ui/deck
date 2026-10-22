package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// ProjectPreviews configures private previews in the unconfined workspace.
type ProjectPreviews struct {
	Loopback       bool            `yaml:"loopback"`
	LoopbackPort   int             `yaml:"loopback_port"`
	MetadataSocket string          `yaml:"metadata_socket"`
	Bind           string          `yaml:"bind"`
	DeckOrigin     string          `yaml:"deck_origin"`
	Unconfined     bool            `yaml:"unconfined"`
	BaseDomain     string          `yaml:"base_domain"`
	HostnamePrefix string          `yaml:"hostname_prefix"`
	PortMin        int             `yaml:"port_min"`
	PortMax        int             `yaml:"port_max"`
	Targets        []PreviewTarget `yaml:"targets"`
}

type PreviewTarget struct {
	ProjectID string `yaml:"project_id" json:"project_id"`
	Origin    string `yaml:"origin" json:"origin"`
	Upstream  string `yaml:"upstream" json:"upstream"`
}

func decodePreviewTargets(value string, targets *[]PreviewTarget) error {
	d := json.NewDecoder(strings.NewReader(value))
	d.DisallowUnknownFields()
	if err := d.Decode(targets); err != nil {
		return errors.New("NOISEDECK_PROJECT_PREVIEWS_TARGETS: expected a JSON array of project_id, origin, upstream registrations")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("NOISEDECK_PROJECT_PREVIEWS_TARGETS: unexpected trailing data")
	}
	return nil
}

func previewOrigin(value string) (*url.URL, string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || strings.ToLower(u.Host) != u.Host || strings.HasSuffix(u.Hostname(), ".") {
		return nil, "", errors.New("must be an exact lowercase HTTPS origin without path, credentials, query or fragment")
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return nil, "", errors.New("invalid origin port")
		}
		if p == "443" {
			return nil, "", errors.New("omit the default HTTPS port from origins")
		}
	}
	if len(u.Hostname()) > 253 {
		return nil, "", errors.New("DNS name is too long")
	}
	for _, label := range strings.Split(u.Hostname(), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return nil, "", errors.New("invalid DNS label")
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
				return nil, "", errors.New("DNS names must use lowercase ASCII or punycode labels")
			}
		}
	}
	site, err := publicsuffix.EffectiveTLDPlusOne(u.Hostname())
	if err != nil || net.ParseIP(u.Hostname()) != nil {
		return nil, "", errors.New("must use a DNS name with a registrable domain")
	}
	return u, site, nil
}

func previewEndpoint(value string) (string, error) {
	host, port, err := net.SplitHostPort(value)
	ip := net.ParseIP(host)
	n, pe := strconv.Atoi(port)
	if err != nil || ip == nil || !ip.IsLoopback() || pe != nil || n < 1 || n > 65535 {
		return "", errors.New("must be a loopback IP and port")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(n)), nil
}

func (c *Config) validateProjectPreviews() error {
	p := c.ProjectPreviews
	if !p.Loopback && p.LoopbackPort == 0 && p.Bind == "" && len(p.Targets) == 0 && p.DeckOrigin == "" && !p.Unconfined && p.BaseDomain == "" && p.HostnamePrefix == "" && p.PortMin == 0 && p.PortMax == 0 && p.MetadataSocket == "" {
		return nil
	}
	if p.MetadataSocket != "" && (!filepath.IsAbs(p.MetadataSocket) || strings.ContainsRune(p.MetadataSocket, '\x00')) {
		return errors.New("project_previews.metadata_socket must be an absolute Unix socket path")
	}
	if !p.Unconfined {
		return errors.New("project_previews: set unconfined: true to acknowledge that dev-server processes share the workspace")
	}
	if c.Auth.Mode == "none" {
		return errors.New("project_previews: authentication is required")
	}
	if p.Loopback {
		return c.validateLoopbackPreviews()
	}
	bind, err := previewEndpoint(p.Bind)
	if err != nil {
		return fmt.Errorf("project_previews.bind: %w", err)
	}
	deck, deckSite, err := previewOrigin(p.DeckOrigin)
	if err != nil {
		return fmt.Errorf("project_previews.deck_origin: %w", err)
	}
	allowed := false
	for _, origin := range c.Server.AllowedOrigins {
		if origin == p.DeckOrigin {
			allowed = true
		}
	}
	if !allowed {
		return errors.New("project_previews.deck_origin must be a server.allowed_origins entry")
	}
	_, deckPort, _ := net.SplitHostPort(c.Server.Bind)
	_, bindPort, _ := net.SplitHostPort(bind)
	if deckPort == bindPort {
		return errors.New("project_previews.bind must use a different port from Deck")
	}
	managed := p.BaseDomain != "" || p.HostnamePrefix != "" || p.PortMin != 0 || p.PortMax != 0
	if len(p.Targets) > 64 || (!managed && len(p.Targets) == 0) {
		return errors.New("project_previews: configure self-service allocation or register between 1 and 64 apps")
	}
	separateSite := func(site string) bool {
		for _, origin := range c.Server.AllowedOrigins {
			u, e := url.Parse(origin)
			if e == nil {
				otherSite, e := publicsuffix.EffectiveTLDPlusOne(strings.ToLower(u.Hostname()))
				if e == nil && site == otherSite {
					return false
				}
			}
		}
		return site != deckSite
	}
	poolContains := func(port string) bool {
		n, _ := strconv.Atoi(port)
		return managed && n >= p.PortMin && n <= p.PortMax
	}
	if managed {
		prefix := p.HostnamePrefix
		if prefix == "" {
			prefix = "p-"
		}
		if len(prefix) > 43 || prefix[0] == '-' || strings.Contains(prefix, ".") {
			return errors.New("project_previews.hostname_prefix: must form one lowercase DNS label with the 20-character project suffix")
		}
		if _, _, e := previewOrigin("https://" + prefix + strings.Repeat("0", 20) + ".example.net"); e != nil {
			return errors.New("project_previews.hostname_prefix: must form one lowercase DNS label with the 20-character project suffix")
		}
		u, site, e := previewOrigin("https://" + p.BaseDomain)
		if e != nil || u.Host != p.BaseDomain || u.Port() != "" || len(p.BaseDomain)+len(prefix)+21 > 253 {
			return errors.New("project_previews.base_domain: must be a lowercase DNS name with room for a generated project label")
		}
		if !separateSite(site) {
			return errors.New("project_previews.base_domain: must use a registrable domain separate from every Deck allowed origin")
		}
		if p.PortMin < 1024 || p.PortMax > 65535 || p.PortMax < p.PortMin || p.PortMax-p.PortMin+1 > 1024 {
			return errors.New("project_previews: port_min and port_max must define between 1 and 1024 ports within 1024..65535")
		}
		if poolContains(deckPort) || poolContains(bindPort) {
			return errors.New("project_previews: allocation ports cannot include Deck or its preview listener")
		}
	}
	projects, origins, endpoints := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, t := range p.Targets {
		if strings.TrimSpace(t.ProjectID) == "" || projects[t.ProjectID] {
			return errors.New("project_previews: project IDs must be nonempty and unique")
		}
		projects[t.ProjectID] = true
		u, site, err := previewOrigin(t.Origin)
		if err != nil {
			return fmt.Errorf("project_previews target origin: %w", err)
		}
		if site == deckSite || u.Hostname() == deck.Hostname() {
			return errors.New("project_previews: apps require a registrable domain separate from Deck")
		}
		if origins[u.Hostname()] {
			return errors.New("project_previews: each app requires a distinct hostname")
		}
		origins[u.Hostname()] = true
		if !separateSite(site) {
			return errors.New("project_previews: app sites cannot share any Deck allowed origin site")
		}
		endpoint, err := previewEndpoint(t.Upstream)
		if err != nil {
			return fmt.Errorf("project_previews upstream: %w", err)
		}
		_, port, _ := net.SplitHostPort(endpoint)
		if port == deckPort || port == bindPort {
			return errors.New("project_previews: upstream cannot target Deck or its preview listener")
		}
		if poolContains(port) {
			return errors.New("project_previews: allocation ports cannot include static upstream ports")
		}
		if endpoints[endpoint] {
			return errors.New("project_previews: upstream endpoints must be unique")
		}
		endpoints[endpoint] = true
	}
	return nil
}

func (c *Config) validateLoopbackPreviews() error {
	p := &c.ProjectPreviews
	if p.BaseDomain != "" || p.HostnamePrefix != "" || len(p.Targets) != 0 {
		return errors.New("project_previews.loopback is exclusive with base_domain, hostname_prefix and targets")
	}
	if p.DeckOrigin != "" {
		return errors.New("project_previews.deck_origin must be empty in loopback mode; use server.allowed_origins")
	}
	if len(c.Server.AllowedOrigins) == 0 {
		return errors.New("project_previews.loopback requires at least one server.allowed_origins entry")
	}
	for _, origin := range c.Server.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(origin, "#") {
			return errors.New("project_previews.loopback requires every server.allowed_origins entry to be an exact loopback HTTP origin")
		}
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || ip.To4() == nil || !ip.IsLoopback() || strings.Contains(u.Hostname(), ":")) {
			return errors.New("project_previews.loopback requires every server.allowed_origins host to be an IPv4 loopback literal or localhost")
		}
		if port := u.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
				return errors.New("project_previews.loopback: invalid server.allowed_origins port")
			}
		} else if strings.HasSuffix(u.Host, ":") {
			return errors.New("project_previews.loopback: invalid server.allowed_origins port")
		}
	}
	if p.LoopbackPort == 0 {
		p.LoopbackPort = 8840
	}
	if p.LoopbackPort < 1 || p.LoopbackPort > 65535 {
		return errors.New("project_previews.loopback_port must be within 1..65535")
	}
	_, deckPort, _ := net.SplitHostPort(c.Server.Bind)
	deckPortNumber, _ := strconv.Atoi(deckPort)
	if p.LoopbackPort == deckPortNumber {
		return errors.New("project_previews.loopback_port must use a different port from Deck")
	}
	if p.PortMin < 1024 || p.PortMax > 65535 || p.PortMax < p.PortMin || p.PortMax-p.PortMin+1 > 254 {
		return errors.New("project_previews.loopback: port_min and port_max must define between 1 and 254 ports within 1024..65535")
	}
	if (p.LoopbackPort >= p.PortMin && p.LoopbackPort <= p.PortMax) || (deckPortNumber >= p.PortMin && deckPortNumber <= p.PortMax) {
		return errors.New("project_previews: allocation ports cannot include Deck or its preview listener")
	}
	if p.Bind != "" {
		host, port, err := net.SplitHostPort(p.Bind)
		n, portErr := strconv.Atoi(port)
		if err != nil || (host != "0.0.0.0" && host != "::") || portErr != nil || n != p.LoopbackPort {
			return errors.New("project_previews.bind in loopback mode must be empty or an unspecified address (0.0.0.0 or [::]) with loopback_port")
		}
	}
	return nil
}
