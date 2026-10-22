package apppreview

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// Entry is what Deck hands the browser: the exact URL to POST the ticket
// to, and the single-use ticket itself.
type Entry struct {
	URL    string `json:"url"`
	Ticket string `json:"ticket"`
}

type ticket struct {
	projectID string
	target    *target
	principal string
	sequence  uint64
	expires   time.Time
}

// session is one authorized preview: the cookie's token hash, the owning
// principal and project, an absolute expiry, and every connection the
// proxy opened for it. Sessions live only in this process's memory, so a
// gateway restart ends them all.
type session struct {
	hash      string
	token     string // needed once, to set the entry cookie
	principal string
	projectID string
	target    *target
	sequence  uint64
	expires   time.Time

	mu        sync.Mutex
	dead      bool
	conns     map[net.Conn]struct{}
	transport *http.Transport
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// tokenHash indexes server-side tickets and sessions without retaining their
// bearer values as map keys.
func tokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

type sessionKey struct{}

func withSession(ctx context.Context, s *session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// sessionRouter routes each proxied request through its own session's
// transport: pooled upstream connections are never shared across sessions,
// so closing one session (expiry, logout, shutdown) closes its connections
// without touching another preview's traffic.
type sessionRouter struct{}

func (r sessionRouter) RoundTrip(req *http.Request) (*http.Response, error) {
	s, _ := req.Context().Value(sessionKey{}).(*session)
	if s == nil {
		return nil, errors.New("apppreview: proxied request carries no preview session")
	}
	t := s.ensureTransport()
	if t == nil {
		return nil, errors.New("apppreview: preview session has ended")
	}
	return t.RoundTrip(req)
}

// ensureTransport lazily builds the session transport. Proxy is left nil
// on purpose: a fresh http.Transport consults no environment proxy, and
// the gateway dials only the configured loopback endpoint anyway.
func (s *session) ensureTransport() *http.Transport {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return nil
	}
	if s.transport == nil {
		sess := s
		s.transport = &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				return sess.track(conn), nil
			},
			MaxIdleConns:          8,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: time.Second,
		}
	}
	return s.transport
}

// track registers a connection — dialed upstream connections and hijacked
// client connections alike — so session teardown closes it.
func (s *session) track(c net.Conn) net.Conn {
	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		_ = c.Close()
		return c
	}
	wrapped := &trackedConn{s: s, Conn: c}
	s.conns[wrapped] = struct{}{}
	s.mu.Unlock()
	return wrapped
}

func (s *session) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

type trackedConn struct {
	net.Conn
	s *session
}

func (t *trackedConn) Close() error {
	t.s.untrack(t)
	return t.Conn.Close()
}

// shutdown closes everything the session owns: hijacked client connections,
// live upstream connections, and the transport's idle pool. It never
// signals the dev server process, which outlives every preview session.
func (s *session) shutdown() {
	s.mu.Lock()
	s.dead = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.conns = make(map[net.Conn]struct{})
	transport := s.transport
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	if transport != nil {
		transport.CloseIdleConnections()
	}
}

// Issue mints a single-use entry ticket for one configured project, bound
// to that project's preview and to the requesting Deck principal.
func (g *Gateway) Issue(projectID, principal string) (Entry, error) {
	if principal == "" {
		return Entry{}, errors.New("apppreview: issuing an entry ticket requires an authenticated principal")
	}
	g.purgeExpired()
	now := g.clock()
	raw, err := randomToken()
	if err != nil {
		return Entry{}, fmt.Errorf("apppreview: ticket randomness: %w", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return Entry{}, errors.New("apppreview: gateway is closed")
	}
	t, ok := g.byProject[projectID]
	if !ok {
		return Entry{}, fmt.Errorf("apppreview: no preview is registered for project %q", projectID)
	}
	g.sequence++
	g.tickets[tokenHash(raw)] = &ticket{
		projectID: projectID,
		target:    t,
		principal: principal,
		sequence:  g.sequence,
		expires:   now.Add(ticketTTL),
	}
	for len(g.tickets) > maxOutstandingTickets {
		g.dropOldestTicketLocked()
	}
	return Entry{URL: t.originStr + entryPath, Ticket: raw}, nil
}

func (g *Gateway) dropOldestTicketLocked() {
	oldestHash := ""
	var oldest uint64
	first := true
	for h, tk := range g.tickets {
		if first || tk.sequence < oldest {
			oldestHash, oldest, first = h, tk.sequence, false
		}
	}
	if !first {
		delete(g.tickets, oldestHash)
	}
}

// consumeTicket atomically redeems a ticket on target t. The lookup itself
// consumes the ticket whether redemption succeeds or the values have gone
// stale — a ticket is single-use, full stop.
func (g *Gateway) consumeTicket(raw string, t *target) (*session, bool) {
	if raw == "" {
		return nil, false
	}
	hash := tokenHash(raw)
	now := g.clock()
	token, err := randomToken()
	if err != nil {
		return nil, false
	}
	g.mu.Lock()
	tk, ok := g.tickets[hash]
	if ok {
		delete(g.tickets, hash)
	}
	if !ok || g.closed || g.byProject[t.projectID] != t || !now.Before(tk.expires) || tk.target != t {
		g.mu.Unlock()
		return nil, false
	}
	g.sequence++
	s := &session{
		hash:      tokenHash(token),
		token:     token,
		principal: tk.principal,
		projectID: tk.projectID,
		target:    t,
		sequence:  g.sequence,
		expires:   now.Add(sessionTTL),
		conns:     make(map[net.Conn]struct{}),
	}
	// Sessions per principal are bounded; reopening through Deck renews
	// access, so past the bound the oldest session of this principal ends
	// rather than the request failing.
	var evicted *session
	live := 0
	for _, other := range g.sessions {
		if other.principal != tk.principal {
			continue
		}
		live++
		if evicted == nil || other.sequence < evicted.sequence {
			evicted = other
		}
	}
	if live >= maxSessionsPerPrincipal {
		delete(g.sessions, evicted.hash)
		evicted.shutdown()
	}
	g.sessions[s.hash] = s
	g.mu.Unlock()
	return s, true
}

// Revoke ends every preview session and outstanding ticket issued to one
// Deck principal — user logout. Upstream dev servers are never
// signalled.
func (g *Gateway) Revoke(principal string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for h, s := range g.sessions {
		if s.principal == principal {
			delete(g.sessions, h)
			s.shutdown()
		}
	}
	for h, tk := range g.tickets {
		if tk.principal == principal {
			delete(g.tickets, h)
		}
	}
}

// Close drops every session, ticket and proxied connection and stops the
// gateway. Dev servers are independent processes and survive untouched.
func (g *Gateway) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	close(g.done)
	for h, s := range g.sessions {
		delete(g.sessions, h)
		s.shutdown()
	}
	g.tickets = make(map[string]*ticket)
	return nil
}
