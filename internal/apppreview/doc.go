// Package apppreview gates authenticated browser access to explicitly
// registered local dev servers.
//
// The gateway owns one http.Handler for a listener of its own: it mounts no
// Deck API or static assets, matches exact configured Host values, and
// forwards everything else opaquely to that host's loopback dev server with
// net/http/httputil. It never interprets app traffic, rewrites HTML, follows
// an upstream redirect, or consults an environment proxy.
//
// Authority is the host-only preview cookie alone. It is issued only by
// spending a single-use, 60-second ticket on the reserved entry path, in a
// POST that must carry one of Deck's exact origins, and it lives for thirty minutes
// or until the issuing principal logs out. Tickets and sessions are bounded
// in-memory state: a restart invalidates all of them, and closing the
// gateway closes its proxied connections — including upgraded WebSockets —
// without signalling the dev servers, which are independent processes.
//
// Configuration carries the user's authority. A registration names a bare loopback
// IP:port upstream and an exact external HTTPS or distinct loopback HTTP
// origin; duplicates and
// anything else are rejected at construction. See docs/project-app-previews.md
// for the deployment boundary this package implements.
package apppreview
