package apppreview

import (
	"errors"
	"fmt"
	"net"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// Target registers one project preview: the exact browser origin routed to
// this gateway, and the bare loopback IP:port of that project's
// dev server. A registration authorizes the endpoint; it is not a claim
// about which process currently owns the port — reassigning a port means
// stopping its registration first.
type Target struct {
	ProjectID string
	Origin    string
	Upstream  string
}

// Config supplies the initial registrations. DeckOrigins are the origins
// of Deck's own listener; only a POST carrying one of them may spend an
// entry ticket. Later registrations use Gateway.Register.
type Config struct {
	DeckOrigins []string
	Loopback    bool
	Targets     []Target
}

// target is the validated, normalized form of a Target.
type target struct {
	scheme     string
	projectID  string
	originStr  string   // canonical scheme://host[:port]
	publicHost string   // authority the app should see (Host, X-Forwarded-Host)
	hostname   string   // lowercased host without port, for cookie checks
	upstream   string   // canonical loopback ip:port
	hostKeys   []string // acceptable Host header values, lowercased
	proxy      *httputil.ReverseProxy
}

// normalizeOrigin validates an exact origin string and returns its
// canonical scheme://host[:port] form. Default ports are dropped, so
// "https://a.example" and "https://a.example:443" are one origin.
func normalizeOrigin(raw string, allowPlainHTTP bool) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("origin is empty")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("origin %q does not parse: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && (!allowPlainHTTP || scheme != "http") {
		return "", fmt.Errorf("origin %q must use https", raw)
	}
	if u.Opaque != "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return "", fmt.Errorf("origin %q must be scheme://host[:port] with no userinfo, query or fragment", raw)
	}
	if p := u.EscapedPath(); p != "" && p != "/" {
		return "", fmt.Errorf("origin %q must not carry a path", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("origin %q has no host", raw)
	}
	if strings.ContainsAny(host, " \t\r\n/?#%") {
		return "", fmt.Errorf("origin %q has an invalid host", raw)
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", fmt.Errorf("origin %q has an invalid port", raw)
		}
		if (scheme == "https" && n == 443) || (scheme == "http" && n == 80) {
			port = ""
		}
	}
	if port == "" {
		return scheme + "://" + host, nil
	}
	return scheme + "://" + host + ":" + port, nil
}

// externalOriginHost rejects loopback addresses and localhost names: an app
// origin is the public name routed to the preview listener,
// never a loopback one.
func externalOriginHost(hostname string) error {
	if ip := net.ParseIP(hostname); ip != nil {
		if ip.IsLoopback() || ip.IsUnspecified() {
			return fmt.Errorf("origin host %q is a loopback or unspecified address", hostname)
		}
		return nil
	}
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return fmt.Errorf("origin host %q is a localhost name", hostname)
	}
	return nil
}

// normalizeUpstream accepts exactly a loopback IP literal with an explicit
// port: "127.0.0.1:5173" or "[::1]:5173". Hostnames, URL forms, paths and
// non-loopback addresses are refused — the gateway must never be pointed
// anywhere but an explicitly registered loopback endpoint.
func normalizeUpstream(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("upstream is empty")
	}
	if strings.Contains(s, "://") {
		return "", fmt.Errorf("upstream %q must be a bare ip:port, not a URL", raw)
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", fmt.Errorf("upstream %q must be host:port: %w", raw, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", fmt.Errorf("upstream %q must be an IP literal, not a hostname", raw)
	}
	if !ip.IsLoopback() {
		return "", fmt.Errorf("upstream %q is not a loopback address", raw)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", fmt.Errorf("upstream %q has an invalid port", raw)
	}
	return net.JoinHostPort(ip.String(), port), nil
}

// buildTargets validates and normalizes every registration. Duplicate
// project ids, hostnames and upstream endpoints are rejected: distinct
// ports are assigned and dev servers bind them strictly, so a shared
// value is a misconfiguration that must surface at startup, not a routing
// ambiguity at request time.
func buildTargets(cfg Config) ([]*target, error) {
	targets := make([]*target, 0, len(cfg.Targets))
	seenProject := make(map[string]bool, len(cfg.Targets))
	seenOrigin := make(map[string]bool, len(cfg.Targets))
	seenHost := make(map[string]bool, len(cfg.Targets))
	seenUpstream := make(map[string]bool, len(cfg.Targets))
	for i, t := range cfg.Targets {
		if strings.TrimSpace(t.ProjectID) == "" {
			return nil, fmt.Errorf("target %d has an empty project id", i)
		}
		originStr, err := normalizeOrigin(t.Origin, cfg.Loopback)
		if err != nil {
			return nil, fmt.Errorf("project %q: %w", t.ProjectID, err)
		}
		u, err := url.Parse(originStr)
		if err != nil {
			return nil, fmt.Errorf("project %q: origin %q does not re-parse: %w", t.ProjectID, originStr, err)
		}
		hostname := u.Hostname()
		if err := validatePreviewOrigin(t.Origin, cfg.Loopback); err != nil {
			return nil, fmt.Errorf("project %q: %w", t.ProjectID, err)
		}
		for _, deckOrigin := range cfg.DeckOrigins {
			deck, _ := url.Parse(deckOrigin)
			if originStr == deckOrigin {
				return nil, fmt.Errorf("project %q: preview origin %s is Deck's own origin", t.ProjectID, originStr)
			}
			if cfg.Loopback && hostname == deck.Hostname() {
				return nil, fmt.Errorf("project %q: preview host %s is Deck's own host", t.ProjectID, hostname)
			}
		}
		upstream, err := normalizeUpstream(t.Upstream)
		if err != nil {
			return nil, fmt.Errorf("project %q: %w", t.ProjectID, err)
		}
		switch {
		case seenProject[t.ProjectID]:
			return nil, fmt.Errorf("duplicate registration for project %q", t.ProjectID)
		case seenOrigin[originStr]:
			return nil, fmt.Errorf("duplicate preview origin %s", originStr)
		case seenHost[hostname]:
			return nil, fmt.Errorf("duplicate preview hostname %s", hostname)
		case seenUpstream[upstream]:
			return nil, fmt.Errorf("duplicate upstream endpoint %s", upstream)
		}
		seenProject[t.ProjectID] = true
		seenOrigin[originStr] = true
		seenHost[hostname] = true
		seenUpstream[upstream] = true

		publicHost := hostname
		port := u.Port()
		if port != "" {
			publicHost = net.JoinHostPort(hostname, port)
		}
		// Browsers omit the default port in the Host header; both spellings
		// name the same configured origin. Every other Host value is
		// unknown and has no route.
		hostKeys := []string{publicHost}
		if port == "" {
			hostKeys = append(hostKeys, net.JoinHostPort(hostname, originPort(u)))
		}
		targets = append(targets, &target{
			scheme:     u.Scheme,
			projectID:  t.ProjectID,
			originStr:  originStr,
			publicHost: publicHost,
			hostname:   hostname,
			upstream:   upstream,
			hostKeys:   hostKeys,
		})
	}
	return targets, nil
}

func validatePreviewOrigin(raw string, loopback bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if !loopback {
		return externalOriginHost(strings.ToLower(u.Hostname()))
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || ip == nil || ip.To4() == nil || !ip.IsLoopback() || ip.Equal(net.IPv4(127, 0, 0, 1)) || strings.Contains(u.Hostname(), ":") || u.Port() == "" {
		return errors.New("loopback preview origin must use plain HTTP and an IPv4 loopback literal other than 127.0.0.1 with an explicit port")
	}
	return nil
}

func originPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "http" {
		return "80"
	}
	return "443"
}
