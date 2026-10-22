// Package config loads and validates the backend's configuration.
//
// Precedence is fixed: compiled defaults, then the optional YAML file named
// by -config, then NOISEDECK_* environment variables. Environment names are the
// YAML path uppercased with underscores under a NOISEDECK_ prefix —
// server.bind becomes NOISEDECK_SERVER_BIND — so the file documents the whole
// surface and nothing needs a second naming table. Lists arrive
// comma-separated, except project_previews.targets, whose environment value
// is a JSON array of registrations. The oidc session secret is deliberately env-only
// (NOISEDECK_AUTH_SESSION_SECRET): it has no YAML key, so it cannot end up in a
// config file that gets committed or backed up with the rest of the state.
//
// Validation is fail-closed and collects every violation before reporting,
// so you fix a broken config in one round trip. The rules encode
// requirements, not taste:
//
//   - a non-loopback bind is refused unless authentication is actually
//     configured for the selected mode, and the bind host must be an IP
//     literal — a hostname could resolve anywhere, which defeats the
//     loopback check;
//   - trusted-proxy mode requires the proxy addresses and identity header,
//     since an identity header is accepted only from a configured proxy;
//   - WebSocket origins are strict URLs with no wildcards;
//   - project roots are required, absolute, and allowlisted;
//   - the reconcile interval may not exceed fifteen seconds, the longest
//     period that still keeps displayed state fresh (shorter is stricter
//     and allowed, floored at 1s to prevent a busy loop);
//   - provider executables, when set, are absolute (the descriptor layer
//     rejects anything else), and a configured retrieved-identity provider
//     (Codex, Kimi, Kilo, omp, opencode) requires its hook config path — identity
//     capture is impossible without it, and the launch-time
//     verify-and-refuse policy needs a file to verify.
//
// Provider executables left unset are resolved from PATH once at load; a
// provider absent from PATH stays unconfigured, which the provider layer
// marks unavailable with a direct "executable or config is missing"
// diagnostic rather than config treating it as fatal — a missing kimi must
// not stop claude.
//
// Unknown YAML keys are rejected rather than ignored, so a typo like
// `sesion_prefix` fails startup instead of silently applying a default.
package config
