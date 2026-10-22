# Serving beyond loopback

The base Compose file publishes Deck on `127.0.0.1` only. Publishing on
other addresses belongs to a deployment overlay you write yourself.

A deployment reached from other machines goes through your reverse proxy
at its public origin, set explicitly as `NOISEDECK_PUBLIC_ORIGIN`. Deck
never infers trusted origins from forwarded headers. The public origin
becomes the only trusted browser and WebSocket origin. In OIDC mode it is
also the base of the exact callback to register with the identity
provider (`<public-origin>/auth/callback`).

Cookie security follows the origin. HTTPS selects Secure `__Host-`
cookies, while any HTTP origin selects non-Secure cookies, so use HTTPS
beyond loopback.

Choose an authentication mode before you expose Deck; see
[Authentication](authentication.md).
