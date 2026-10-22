package apppreview

import (
	"bufio"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
)

// newTargetProxy builds the opaque forwarder for one registered preview.
// The whole path and query stay exactly as the browser sent them: root
// assets, client-side routing and /api are the app's namespace, and the
// dev server supplies its own SPA fallback. Upstream redirects are passed
// to the browser, never followed.
func newTargetProxy(t *target, rt http.RoundTripper) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			// Strip everything a client could use to assert an identity or
			// a forwarding topology; the gateway constructs that metadata
			// itself from configuration. App cookies and the app's own
			// Authorization header stay.
			stripIdentityHeaders(out.Header)
			filterForwardedCookies(out.Header)
			out.URL.Scheme = "http"
			out.URL.Host = t.upstream
			out.Host = t.publicHost
			out.Header.Set("X-Forwarded-Proto", t.scheme)
			out.Header.Set("X-Forwarded-Host", t.publicHost)
		},
		Transport: rt,
		ModifyResponse: func(res *http.Response) error {
			sanitizeUpstreamResponse(res, t)
			// Trailers arrive after header inspection. Suppress them so they cannot
			// reintroduce cookies or authority headers after the body is copied.
			res.Trailer = nil
			if res.StatusCode != http.StatusSwitchingProtocols {
				res.Body = &withoutTrailers{ReadCloser: res.Body, response: res}
			}
			return nil
		},
		// The dev server is started and stopped by hand, independently of
		// this process. Its absence is the ordinary state, so answer with a
		// useful unavailable message without logging app request details.
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "Preview unavailable. Start the project's dev server and reload.", http.StatusBadGateway)
		},
		// ReverseProxy logs transport failures through ErrorLog; upstream
		// errors echo request paths and bodies, which never belong in a
		// log.
		ErrorLog: log.New(io.Discard, "", 0),
		// Flush each write immediately so HMR and streaming responses
		// cross the gateway as they cross the dev server.
		FlushInterval: -1,
	}
}

// hijackCapture wraps the ResponseWriter the proxy answers on so the
// hijacked client connection of an upgraded WebSocket joins the session's
// tracked set. Session teardown then closes both ends of the proxy pipe —
// the hijacked side here, the upstream side through its tracked transport.
type hijackCapture struct {
	http.ResponseWriter
	s *session
	t *target
}

func (h *hijackCapture) WriteHeader(code int) {
	// ReverseProxy sends informational responses before ModifyResponse runs.
	// Apply the same policy there so early hints cannot carry reserved cookies.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		sanitizeUpstreamResponse(&http.Response{Header: h.Header(), StatusCode: code}, h.t)
	}
	h.ResponseWriter.WriteHeader(code)
}

func (h *hijackCapture) Unwrap() http.ResponseWriter { return h.ResponseWriter }

func (h *hijackCapture) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(h.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	return h.s.track(conn), rw, nil
}

func (h *hijackCapture) Flush() {
	_ = http.NewResponseController(h.ResponseWriter).Flush()
}

// sanitizeUpstreamResponse installs the gateway's rules on what the app may
// send back: no permissive CORS (all legitimate preview traffic is
// same-origin, so widening is pure attack surface), no caching, no cookie
// the gateway or Deck would honor, and nothing smuggled through
// Connection-named headers.
func sanitizeUpstreamResponse(res *http.Response, t *target) {
	h := res.Header
	// On a 101 the Connection/Upgrade pair must survive for the protocol
	// switch to complete; any other Connection-named header still dies.
	stripConnectionNamedHeaders(h, res.StatusCode == http.StatusSwitchingProtocols)
	sanitizeSetCookies(h, t.hostname)
	for _, name := range corsResponseHeaders {
		h.Del(name)
	}
	h.Set("Cache-Control", "no-store")
	h.Del("Clear-Site-Data")
	h.Del("Service-Worker-Allowed")
}

var corsResponseHeaders = []string{
	"Access-Control-Allow-Origin",
	"Access-Control-Allow-Credentials",
	"Access-Control-Allow-Methods",
	"Access-Control-Allow-Headers",
	"Access-Control-Expose-Headers",
	"Access-Control-Max-Age",
}

// stripConnectionNamedHeaders deletes every header the Connection header
// names. A response could otherwise carry edge credentials or extra
// Set-Cookie lines past inspection by listing them as hop-by-hop; the
// request direction gets the same treatment inside ReverseProxy before
// Rewrite runs.
func stripConnectionNamedHeaders(h http.Header, keepUpgrade bool) {
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			if keepUpgrade && strings.EqualFold(tok, "Upgrade") {
				continue
			}
			h.Del(tok)
		}
	}
	if !keepUpgrade {
		h.Del("Connection")
	}
}

// identityHeaders are removed from every forwarded request. They carry
// client-asserted identity or forwarding topology; an app must never be
// able to mistake a browser-supplied value for an edge assertion.
var identityHeaders = []string{
	"Forwarded",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Forwarded-Port",
	"X-Forwarded-Server",
	"X-Forwarded-User",
	"X-Forwarded-Email",
	"X-Forwarded-Groups",
	"X-Forwarded-Client-Cert",
	"X-Real-Ip",
	"X-Original-Forwarded-For",
	"X-Original-Url",
	"X-Auth-Request-User",
	"X-Auth-Request-Email",
	"X-Remote-User",
	"X-Remote-Email",
	"X-Remote-Groups",
	"X-Remote-Name",
	// Hop-by-hop credentials the edge may have accepted. ReverseProxy
	// drops these before Rewrite runs; listed again so the rule is stated
	// in one place and survives refactoring.
	"Proxy-Authorization",
	"Proxy-Connection",
}

func stripIdentityHeaders(h http.Header) {
	for name := range h {
		lower := strings.ToLower(name)
		for _, prefix := range []string{"x-forwarded-", "x-auth-request-", "x-remote-", "x-original-"} {
			if strings.HasPrefix(lower, prefix) {
				h.Del(name)
				break
			}
		}
	}
	for _, name := range identityHeaders {
		h.Del(name)
	}
}

// The transport fills response trailers when the body reaches EOF.
type withoutTrailers struct {
	io.ReadCloser
	response *http.Response
}

func (b *withoutTrailers) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.response.Trailer = nil
	return n, err
}
