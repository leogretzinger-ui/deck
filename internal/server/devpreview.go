package server

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// DevPreviewPrefix is the single path subtree the Vite dev server is served
// under. It is same-origin with the app by design: Deck's session cookie is
// host-only (the __Host- prefix in production forbids a Domain attribute
// outright) and Path=/, so a second hostname receives neither the session
// cookie nor the OIDC transaction cookie: it would need its own sign-in, and
// under oidc its own registered callback, since auth.google.redirect_url is a
// single value. Staying on the origin costs none of that.
const DevPreviewPrefix = "/__dev/"

// devPreviewCSP relaxes the static policy for the DEV PREVIEW ROUTE ONLY. A
// module bundler in development is not a compiled build: Vite injects an
// inline preamble, rewrites modules on the fly, and reaches back over a
// WebSocket to push updates. That needs 'unsafe-inline' and 'unsafe-eval'
// under script-src and the ws/wss schemes under connect-src — the exact
// widening staticCSP exists to deny the app's own compiled pages, which keep
// it. This route is gated behind a default-off experiment and the browser
// authentication precisely because it is this permissive.
const devPreviewCSP = "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; img-src 'self' data: blob:; font-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"

// DevPreviewHandler reverse-proxies the dev-preview subtree to a Vite dev
// server on the loopback interface. It is mounted only when the dev-preview
// experiment is enabled, and only behind the ordinary browser authentication:
// the dev server serves project source and executes it with the live
// signed-in user's authority, so an unauthenticated route here would hand both
// away. StaticHandler refuses the prefix unconditionally, so with the
// experiment off the path 404s exactly like any unknown one.
//
// The prefix is NOT stripped. Vite is configured with a matching base, so it
// owns the whole subtree and emits its own asset and HMR URLs under it; a
// stripped prefix would make Vite emit "/assets/..." references that collide
// with the compiled app's own static surface.
//
// Upgrade requests need no special handling: httputil.ReverseProxy switches
// to a raw byte pipe when the upstream answers 101, which is what carries
// HMR.
func DevPreviewHandler(upstream *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	// Every security header this route owns is installed HERE, on the final
	// response, and not on the ResponseWriter before proxying. An upstream
	// informational response (103 Early Hints) is forwarded downstream, and
	// net/http clears the header map once it has been sent — so headers set
	// before ServeHTTP are gone by the time the real 200 is written, and the
	// preview would run with no policy at all. Setting them on the response
	// the proxy is about to copy is immune to that, and Set() also replaces
	// any upstream policy rather than appending a second one for the browser
	// to enforce alongside ours.
	proxy.ModifyResponse = func(res *http.Response) error {
		res.Header.Del("Content-Security-Policy-Report-Only")
		setStaticSecurityHeaders(res.Header)
		res.Header.Set("Content-Security-Policy", devPreviewCSP)
		res.Header.Set("Cache-Control", "no-store")
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		// ErrorHandler bypasses ModifyResponse, so this response installs the
		// same headers itself.
		setStaticSecurityHeaders(w.Header())
		w.Header().Set("Content-Security-Policy", devPreviewCSP)
		w.Header().Set("Cache-Control", "no-store")
		// The dev server is started and stopped by hand, independently of
		// this process. Its absence is the ordinary state, not a fault of
		// the backend, so say so plainly rather than logging an error.
		http.Error(w, "dev preview upstream is not running: start it with `cd web && pnpm dev`", http.StatusBadGateway)
	}
	return proxy
}
