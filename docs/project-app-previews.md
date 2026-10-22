# Private project app previews

The gateway and managed registration API are implemented.
The managed API adds no new UI.
The static-registration design below describes the original foundation;
managed registrations add one-time domain/pool setup and runtime changes.
No deployment is authorized.

Deck users build their own applications. An agent starts that project's dev
server; you choose **Open preview** from the project menu and open
the application in a separate browser tab. Source edits reach that server
immediately, including hot reload, without rebuilding or redeploying Deck.

## Smallest supported boundary

The first version provides authenticated browser access to explicitly
registered local dev servers in the existing **unconfined workspace**. It does
not sandbox a server process, agent, filesystem, or network. You must
trust the processes sharing that workspace. Running hostile server processes
requires separately enforced host isolation. Preview configuration
is a setting you make, never authority derived from a project file or output.

Each registered app has a project ID, an exact HTTPS public origin and a
loopback IP/port upstream. You assign distinct ports and configure
dev servers to fail if their assigned port is occupied (for example Vite's
`strictPort`). Duplicate origins, project registrations and upstream endpoints
are rejected. There is no automatic port discovery and no arbitrary destination
parameter. A registration authorizes that endpoint, not a claim about which
process currently owns it. Stop the registration before reassigning its port.

No hostname, identity provider, owner name, or hosting vendor is built in.
A dedicated registrable preview domain separate from Deck's domain is required
for the initial implementation. Each app gets its own hostname under it.
Different origins alone do not make sibling subdomains different sites.

## Existing components

`internal/server/devpreview.go` already uses Go's HTTP reverse proxy for HTTP
and WebSocket upgrades. Reuse that standard-library transport, not its handler:
`/__dev/` intentionally serves Deck development code with Deck's authority.
The new feature does not enable or extend that experiment.

`internal/auth` supplies the authenticated Deck principal and CSRF enforcement.
Production Deck cookies already use the host-only `__Host-` prefix. The
terminal WebSocket handler already requires an exact allowed origin. Those
protections remain in place; preview origins never join Deck's origin allowlist.

## Entry and authorization

1. From Deck's project menu, an authenticated, CSRF-protected POST requests
   entry for one configured project. The backend verifies the project still
   exists. Authentication mode `none` cannot enable previews.
2. Deck returns a random, single-use entry ticket valid for 60 seconds, bound
   to that project and exact preview origin. The browser submits it in a POST
   form to the preview gateway's reserved entry path in the newly opened tab.
   No credential goes in a URL, referrer, terminal, or access log. The new tab
   has no opener authority.
3. The gateway requires the exact Deck Origin on that entry POST, consumes
   the ticket atomically, sets a host-only Secure HttpOnly preview cookie and
   redirects to `/`. The cookie has a short absolute lifetime (30 minutes)
   and authorizes only this registered preview. Reopening through Deck renews
   access. Gateway restart invalidates outstanding tickets and sessions.
4. Every HTTP request and WebSocket upgrade checks that preview session.
   Missing or expired authorization returns refusal, never app content or an
   upstream redirect. Unknown hosts have no route. WebSocket requests require
   the exact app Origin, including when cookies are valid. Active upgraded
   connections close on session expiry, revocation and gateway shutdown.

Entry tickets and preview sessions are bounded in-memory state, not new
durable project state. Logout revokes your preview sessions;
gateway shutdown closes proxy connections without signalling dev servers.
Trusted-proxy mode must bind entry to the authenticated principal; its external
logout cannot promise an immediate callback to Deck, so the absolute preview
lifetime is the stated upper bound in that mode.

## Proxy and browser isolation

The gateway is a separate listener in Deck’s single backend process, sharing
in-memory tickets and sessions. It never mounts Deck APIs or static assets. It matches exact configured Host values and dials only the configured
loopback endpoint without environment proxies or request-controlled DNS.
It forwards the whole app path/query, methods, streaming responses and upgrade
traffic. Root assets, client-side routing and `/api` remain the app's own
namespace. The app's dev server supplies its SPA fallback. External HTTPS and
WSS URLs are configured in the dev server where required; the gateway does not
rewrite HTML or guess a framework's HMR protocol.

The gateway consumes its cookie, strips Deck cookies and identity/forwarding
headers, and constructs forwarding metadata from configured origins. App
cookies remain available to the app. Upstream responses cannot set the gateway
cookie or parent-domain cookies; accepted app cookies are host-only. App code
can still write parent-domain JavaScript cookies, so those cookies must never
establish gateway authority. Preview auth uses `__Host-` exclusively.

The gateway refuses cross-origin fetches and cross-preview WebSocket access,
and does not pass through permissive credentialed CORS headers. Same-origin
app requests continue normally. Top-level GET navigation is allowed only after
preview authentication, preserving ordinary app links and OAuth callbacks.
Deck API access from a preview must be refused server-side, including safe
methods, rather than merely hidden by CORS. Deck's exact-origin mutation and
WebSocket checks remain mandatory. Cross-site API navigation and embedding are
also refused, without breaking the separate `/auth/callback` login route.

The initial preview feature does not support service workers: registration
requests are refused so project workers cannot intercept the reserved entry
flow or outlive the dev server as an apparently live preview. Ordinary assets,
fetch, SPA routes and HMR do not require service workers.

## Cookies and OAuth

Deck login occurs only on Deck's origin with its existing configuration; no
additional Google callback or provider dependency is introduced by previews.
Apps use their own OAuth clients, secrets, state and PKCE checks and register
their exact preview callback URLs. A stable configured app hostname supports
that registration. The preview cookie uses SameSite=Lax to support top-level
GET OAuth callbacks. Cross-site POST callbacks are outside the first version;
do not weaken preview cookies to make them work. App session cookies should
use host-only names, preferably `__Host-` with Secure on HTTPS.

## Repository delivery

The implementation includes the bounded gateway, config validation and lifecycle,
authenticated entry API, project-menu action, matching preview fixture, tests
and a runnable sample-app acceptance harness. UI copy and browser probes
change together. Changes require different-model formal review, passing checks
and approval before merge.

## Rollout handoff

These are deployment prerequisites, not agent-authorized host changes:

1. Choose a dedicated preview domain distinct from Deck's registrable domain.
   Allocate one exact hostname per app, or wildcard DNS/TLS for convenience.
2. Route those HTTPS hosts to the private preview listener. Preserve Host,
   support WebSocket Upgrade and streaming, and reject unknown hosts. Route
   Deck's hostname only to Deck's ordinary listener. Do not cache preview
   traffic, entry responses, or authentication responses. Do not log bodies,
   cookies, authorization values or entry tickets.
3. Keep both the preview listener and all dev-server ports off public
   interfaces/firewall rules. Only the TLS reverse proxy reaches the gateway;
   only explicitly registered loopback endpoints are proxied. In Compose,
   loopback means the gateway's container network namespace, not the host.
4. Configure each project ID, app origin and unique port. Start the dev server
   with that port, strict binding, the configured allowed host and external
   WSS origin if its framework requires it. Keep app API calls same-origin or
   use the app's own dev-server API proxy; Deck is not an API destination.
5. Deploy the reviewed Deck version once, after approval. Later app source
   edits require no Deck rebuild or redeploy. Registration changes are
   configuration changes and initially require a Deck restart.
6. Run the acceptance harness through actual HTTPS ingress before enabling
   access. A local harness is not evidence about host DNS, TLS or firewall
   enforcement. Roll back by removing preview ingress and disabling preview
   configuration; stopping Deck must not kill project dev servers.

## Acceptance evidence required

Use two synthetic apps and an isolated Deck database/tmux namespace. Test the
actual browser through the pinned Playwright resolver, never your own sessions.

- Authenticated Open preview succeeds; fresh browsers cannot fetch pages,
  assets, API responses or upgrade WebSockets. Tickets expire, cannot replay,
  and cannot enter a different preview host.
- CSS, JavaScript, images, nested SPA routes, API GET/POST, streaming and a real
  dev-server HMR edit work through the gateway, with two simultaneous previews.
- App cookies round-trip; gateway/Deck cookies and trusted identity headers
  never reach app servers. Parent-domain and reserved-cookie response attacks
  cannot change gateway authorization.
- A malicious preview attempts Deck API reads, writes, forms, navigation and
  terminal WebSockets, plus credentialed requests to the other preview. None
  obtains Deck or sibling authority. Test forged Host/forwarding headers,
  arbitrary destinations, duplicate ports and disallowed origins.
- Preview session expiry/logout closes existing WebSockets. A stopped dev
  server produces an unavailable response. Gateway shutdown drains and closes
  its connections; both app processes and unrelated Deck sessions survive.
- OAuth-style top-level GET callback works with app state/cookies; a cross-site
  POST callback is explicitly refused. Service-worker registration is refused.

Report local and deployed results separately. No end-to-end result is claimed
until the corresponding probe has actually run.

The verification gate also covers gateway-restart invalidation of tickets and
sessions, authentication-mode `none` refusal, principal-bound revocation and
absolute session expiry. You must check ingress logs with synthetic
credentials to verify that neither ticket bodies nor cookies are recorded.
