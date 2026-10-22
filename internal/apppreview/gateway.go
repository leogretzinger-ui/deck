package apppreview

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// entryPath is the one reserved path. Everything else — including any
	// other /__deck_preview/... path — belongs to the app and is proxied
	// opaquely.
	entryPath = "/__deck_preview/enter"

	ticketTTL  = 60 * time.Second
	sessionTTL = 30 * time.Minute
)

// Bounds on in-memory state. Tickets live a minute and sessions are
// per-principal, so a single user cannot approach either bound; past a
// bound the oldest state dies first rather than the gateway growing
// without limit.
var (
	maxOutstandingTickets   = 128
	maxSessionsPerPrincipal = 8
)

// Gateway serves authenticated previews of the registered targets. It is
// an http.Handler for the private preview listener only; Deck's own routes
// never pass through it.
type Gateway struct {
	deckOrigins []string
	loopback    bool
	byProject   map[string]*target // guarded by mu
	byHost      map[string]*target // guarded by mu

	// Session removal holds mu through shutdown so Unregister cannot miss
	// connections whose revocation is still in progress.
	mu       sync.Mutex
	sequence uint64
	now      func() time.Time
	closed   bool
	sessions map[string]*session
	tickets  map[string]*ticket
	done     chan struct{}
}

// New validates the registration and returns a ready gateway. Invalid
// targets and duplicates are configuration errors and refuse construction.
func New(cfg Config) (*Gateway, error) {
	if len(cfg.DeckOrigins) == 0 {
		return nil, errors.New("apppreview: at least one Deck origin is required")
	}
	deckOrigins := make([]string, 0, len(cfg.DeckOrigins))
	for _, raw := range cfg.DeckOrigins {
		origin, err := normalizeOrigin(raw, true)
		if err != nil {
			return nil, fmt.Errorf("apppreview: deck origin: %w", err)
		}
		deckOrigins = append(deckOrigins, origin)
	}
	cfg.DeckOrigins = deckOrigins
	targets, err := buildTargets(cfg)
	if err != nil {
		return nil, fmt.Errorf("apppreview: %w", err)
	}
	g := &Gateway{
		deckOrigins: deckOrigins,
		loopback:    cfg.Loopback,
		byProject:   make(map[string]*target, len(targets)),
		byHost:      make(map[string]*target, len(targets)),
		now:         time.Now,
		sessions:    make(map[string]*session),
		tickets:     make(map[string]*ticket),
		done:        make(chan struct{}),
	}
	router := sessionRouter{}
	for _, t := range targets {
		t.proxy = newTargetProxy(t, router)
		g.byProject[t.projectID] = t
		for _, key := range t.hostKeys {
			g.byHost[key] = t
		}
	}
	go g.janitor()
	return g, nil
}

// HasProject reports whether a project has a registered preview.
func (g *Gateway) HasProject(projectID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.byProject[projectID]
	return !g.closed && ok
}

// Register adds an exact preview route without restarting the gateway. An
// unchanged registration preserves its sessions; changing a route requires
// Unregister first so old cookies and tickets cannot follow a new server.
func (g *Gateway) Register(registration Target) error {
	targets, err := buildTargets(Config{Targets: []Target{registration}, DeckOrigins: g.deckOrigins, Loopback: g.loopback})
	if err != nil {
		return fmt.Errorf("apppreview: %w", err)
	}
	t := targets[0]
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return errors.New("apppreview: gateway is closed")
	}
	if existing := g.byProject[t.projectID]; existing != nil {
		if existing.originStr == t.originStr && existing.upstream == t.upstream {
			return nil
		}
		return fmt.Errorf("apppreview: project %q already has a different registration", t.projectID)
	}
	for _, existing := range g.byProject {
		if existing.hostname == t.hostname {
			return fmt.Errorf("apppreview: preview hostname %q is already registered", t.hostname)
		}
		if existing.upstream == t.upstream {
			return fmt.Errorf("apppreview: upstream endpoint %q is already registered", t.upstream)
		}
	}
	t.proxy = newTargetProxy(t, sessionRouter{})
	g.byProject[t.projectID] = t
	for _, key := range t.hostKeys {
		g.byHost[key] = t
	}
	return nil
}

// Unregister withdraws a project's route and ends every ticket, session and
// proxied connection for it before returning. It does not stop the dev server.
func (g *Gateway) Unregister(projectID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	t := g.byProject[projectID]
	if t == nil {
		return
	}
	delete(g.byProject, projectID)
	for _, key := range t.hostKeys {
		delete(g.byHost, key)
	}
	for hash, tk := range g.tickets {
		if tk.projectID == projectID {
			delete(g.tickets, hash)
		}
	}
	for hash, s := range g.sessions {
		if s.projectID == projectID {
			delete(g.sessions, hash)
			// Keep registration and authorization blocked until these sessions
			// are dead, including requests that already resolved the route.
			s.shutdown()
		}
	}
}

// ServeHTTP dispatches one request on the preview listener. The order of
// the gates is the security posture: refuse closed gateways and unknown
// hosts outright, refuse proxy-form requests, keep the entry path for
// ticket redemption, and default-deny everything else without a live
// session for exactly this preview host.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	g.purgeExpired()
	g.mu.Lock()
	closed := g.closed
	t := g.byHost[strings.ToLower(req.Host)]
	g.mu.Unlock()
	if closed {
		refuse(w, http.StatusServiceUnavailable)
		return
	}
	if t == nil {
		refuse(w, http.StatusMisdirectedRequest)
		return
	}
	if isProxyFormRequest(req) {
		refuse(w, http.StatusBadRequest)
		return
	}
	if req.URL.Path == entryPath {
		g.handleEnter(w, req, t)
		return
	}
	s := g.authorize(req, t)
	if s == nil {
		refuse(w, http.StatusForbidden)
		return
	}
	if strings.EqualFold(req.Header.Get("Service-Worker"), "script") {
		// A project service worker would outlive the dev server as an
		// apparently live preview and could intercept the reserved entry
		// flow; previews do not support them.
		refuse(w, http.StatusForbidden)
		return
	}
	if isUpgradeRequest(req) {
		// A WebSocket has no same-origin policy; the handshake's Origin is
		// its only cross-site defense, so require the exact app origin even
		// when the preview cookie is valid.
		if normalizeOriginHeader(req.Header.Get("Origin")) != t.originStr {
			refuse(w, http.StatusForbidden)
			return
		}
	} else if !browserAllows(req, t) {
		refuse(w, http.StatusForbidden)
		return
	}
	t.proxy.ServeHTTP(&hijackCapture{ResponseWriter: w, s: s, t: t}, req.WithContext(withSession(req.Context(), s)))
}

// handleEnter redeems a ticket: the one place a browser earns preview
// authority. The POST must carry one of Deck's origins (the ticket is the
// CSRF secret; the Origin check keeps any other site from spending it), the
// ticket must name this host's project, and the lookup consumes it.
func (g *Gateway) handleEnter(w http.ResponseWriter, req *http.Request, t *target) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		refuse(w, http.StatusMethodNotAllowed)
		return
	}
	if !slices.Contains(g.deckOrigins, normalizeOriginHeader(req.Header.Get("Origin"))) {
		refuse(w, http.StatusForbidden)
		return
	}
	req.Body = http.MaxBytesReader(w, req.Body, 16<<10)
	if err := req.ParseForm(); err != nil {
		refuse(w, http.StatusForbidden)
		return
	}
	s, ok := g.consumeTicket(req.PostFormValue("ticket"), t)
	if !ok {
		refuse(w, http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.SetCookie(w, &http.Cookie{
		Name:     g.cookieName(),
		Value:    s.token,
		Path:     "/", // __Host- requires Path=/ and no Domain attribute
		MaxAge:   int(sessionTTL / time.Second),
		Secure:   !g.loopback,
		HttpOnly: true,
		// Lax, not Strict: a top-level GET OAuth callback must still carry
		// the cookie. Cross-site POST callbacks are refused elsewhere and
		// are not worth weakening the cookie for.
		SameSite: http.SameSiteLaxMode,
	})
	s.token = ""
	http.Redirect(w, req, "/", http.StatusSeeOther)
}

// authorize resolves the preview cookie to a live session for exactly this
// target. A session is authority on its own preview only; the same token
// presented on a sibling host is just an unknown value.
func (g *Gateway) authorize(req *http.Request, t *target) *session {
	c, err := req.Cookie(g.cookieName())
	if err != nil || c.Value == "" {
		return nil
	}
	hash := tokenHash(c.Value)
	now := g.clock()
	g.mu.Lock()
	if g.closed || g.byProject[t.projectID] != t {
		g.mu.Unlock()
		return nil
	}
	s := g.sessions[hash]
	if s != nil {
		if !now.Before(s.expires) {
			delete(g.sessions, hash)
			s.shutdown()
			s = nil
		} else if s.target != t {
			s = nil
		}
	}
	g.mu.Unlock()
	return s
}

// isProxyFormRequest reports requests no origin-server should ever see.
// The gateway speaks origin-form HTTP for its configured hosts only;
// CONNECT and absolute- or asterisk-form targets are proxy requests, and
// this is not a proxy.
func isProxyFormRequest(req *http.Request) bool {
	return req.Method == http.MethodConnect ||
		req.URL.IsAbs() ||
		req.URL.Scheme != "" ||
		req.URL.Host != "" ||
		req.RequestURI == "*"
}

func isUpgradeRequest(req *http.Request) bool {
	for _, v := range req.Header.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "Upgrade") {
				return true
			}
		}
	}
	return false
}

// browserAllows applies fail-closed browser rules to authenticated app
// traffic. Same-origin requests pass. Anything a browser marks cross-site
// — or same-site, since sibling previews share the registrable domain and
// are different origins — passes only as a top-level GET navigation:
// ordinary app links and OAuth callbacks. An explicit foreign Origin is
// refused outright. Without Fetch Metadata a request must still carry
// positive same-origin evidence — the app's own Origin, or a same-origin
// Referer; a request with no evidence at all looks exactly like a
// referrer-suppressed cross-site tag from an old browser and is refused.
func browserAllows(req *http.Request, t *target) bool {
	if o := req.Header.Get("Origin"); o != "" {
		if normalizeOriginHeader(o) != t.originStr {
			return false
		}
	}
	site := strings.TrimSpace(req.Header.Get("Sec-Fetch-Site"))
	if site == "" {
		if req.Header.Get("Origin") == "" && req.Header.Get("Referer") == "" {
			return false
		}
		return refererAllows(req, t)
	}
	switch strings.ToLower(site) {
	case "same-origin":
		return true
	case "none", "same-site", "cross-site":
		return strings.EqualFold(req.Header.Get("Sec-Fetch-Mode"), "navigate") &&
			strings.EqualFold(req.Header.Get("Sec-Fetch-Dest"), "document") &&
			req.Method == http.MethodGet
	default:
		return false
	}
}

func refererAllows(req *http.Request, t *target) bool {
	ref := req.Header.Get("Referer")
	if ref == "" {
		return true
	}
	u, err := url.Parse(ref)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return false
	}
	origin, err := normalizeOrigin(u.Scheme+"://"+u.Host, true)
	if err != nil {
		return false
	}
	return origin == t.originStr
}

// refuse answers with a bare status: no body, nothing cacheable, nothing
// that distinguishes refusal reasons to the client.
func refuse(w http.ResponseWriter, code int) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
}

// janitor closes expired sessions promptly even when no further request
// arrives — an otherwise idle WebSocket would outlive its authorization
// until traffic happened.
func (g *Gateway) janitor() {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-g.done:
			return
		case <-tick.C:
			g.purgeExpired()
		}
	}
}

// purgeExpired eagerly drops expired tickets and sessions and closes the
// dead sessions' proxied connections.
func (g *Gateway) purgeExpired() {
	now := g.clock()
	g.mu.Lock()
	defer g.mu.Unlock()
	for h, s := range g.sessions {
		if !now.Before(s.expires) {
			delete(g.sessions, h)
			s.shutdown()
		}
	}
	for h, tk := range g.tickets {
		if !now.Before(tk.expires) {
			delete(g.tickets, h)
		}
	}
}

func (g *Gateway) clock() time.Time {
	g.mu.Lock()
	now := g.now()
	g.mu.Unlock()
	return now
}

// setClock replaces the time source. Tests use it to cross expiry
// boundaries deterministically.
func (g *Gateway) setClock(f func() time.Time) {
	g.mu.Lock()
	g.now = f
	g.mu.Unlock()
}

func normalizeOriginHeader(raw string) string {
	s, err := normalizeOrigin(raw, true)
	if err != nil {
		return ""
	}
	return s
}
