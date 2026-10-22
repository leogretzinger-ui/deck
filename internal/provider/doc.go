// Package provider holds the per-provider descriptors for every real CLI Deck
// can launch.
//
// A descriptor is deliberately small: executable and argv construction for new
// and resumed sessions, permitted environment and required config paths,
// native session identity, a version-tested terminal injection contract
// (literal/bracketed paste, submit sequence, multiline, active-turn input),
// and graceful/forced stop behavior.
//
// Descriptors must not parse token streams into a shared event model, must not
// implement provider approvals, questions, tools, or model catalogs, and must
// not discover identity or status by parsing provider transcripts, databases,
// state files, or terminal output. A provider
// upgrade whose launch or resume contract changed still launches, and nothing
// measures it: these contracts are measurements of CLIs Deck does not
// own, and a measurement going stale is not evidence the provider is broken.
// Refusing on it would take a working CLI away from the user on our
// schedule rather than the provider's. Two prerequisites do still refuse,
// because each is proof of absence rather than a measurement: an executable
// that is missing or unrunnable, and an unprovisioned identity hook, without
// which a retrieved-identity session could never be resumed.
//
// # Availability
//
// A supported provider needs a documented way to assign or retrieve an exact
// native session ID, and to resume that session. The resumable providers
// satisfy that by two mechanisms, each verified against its real CLI:
//
//	claude 2.1.220  ASSIGNED   --session-id <uuid> at launch; --resume <uuid>.
//	codex  0.145.0  RETRIEVED  SessionStart hook payload carries session_id;
//	                           codex resume <id>.
//	kimi   0.29.2   RETRIEVED  SessionStart hook payload carries session_id;
//	                           kimi -S <id>.
//	omp    17.2.12  RETRIEVED  session_start hook (injected via --hook) reads
//	                           ctx.sessionManager.getSessionId() eagerly at
//	                           launch, before first submission; omp --resume <uuid>.
//	opencode 1.18.15 RETRIEVED session.created plugin event carries session_id;
//	                           opencode -s <id>.
//	pi     0.84.1   ASSIGNED   --session-id <uuid> at launch;
//	                           --session <uuid>.
//
// In every case an unknown id fails loudly rather than silently starting a
// fresh conversation, which is what makes a successful resume meaningful.
// Codex's exact id is NOT observed launch-time state: 0.145.0 emitted no
// SessionStart event during a ten-second idle window after a fully provisioned
// process became ready. Its first observed event arrived after the first user
// submission and before that turn's reply. The session-launch layer must
// represent that timing honestly; it may not inject a synthetic prompt or
// claim the id exists before the hook delivers it.
//
// omp mints its UUIDv7 at session construction and fires session_start
// before any submission. Both retrieval timings use the same launch rule:
// attach an owned live pane immediately, with its native ID pending until
// the hook delivers it. A missing callback does not mean the process failed.
//
// A hook is not output scraping. Terminal output is presentation that changes
// for cosmetic reasons and gives no signal when it does; a hook is a documented
// callback delivering a named field in structured JSON, and if the field
// disappears the hook stops producing it. That is what keeps retrieval on the
// right side of the no-scraping rule.
//
// # The cost of retrieval
//
// Retrieval depends on a hook present in the user's provider
// configuration — provider state this product does not own. Provisioning (the
// container image or bootstrap that seeds provider configs) installs the hook
// and establishes any provider-native hook trust; Deck never writes the
// user's provider configuration or answers a provider modal. Deck's
// job is to retain the capture grant so a callback can deliver the native
// identity after attachment. A missing or broken hook leaves that identity
// pending and costs automatic resume; it must not block an otherwise working
// launch. IdentityHook names the config file and RequiredConfigPaths records
// what provisioning must preserve. Exact resume separately requires evidence
// that the CLI resumed the requested native identity.
//
// The two hook formats are NOT interchangeable. Codex uses nested JSON arrays
// of hook objects in ~/.codex/hooks.json; Kimi uses flat TOML `[[hooks]]`
// entries in ~/.kimi-code/config.toml and permits only event, matcher, command,
// and timeout — any extra key makes the whole config fail to load. Writing
// carelessly there breaks every Kimi session, not just ours.
//
// # Establishing availability
//
// A provider is declared unavailable only on evidence that it cannot do the
// thing, never on a survey that did not find it. Flags and subcommands are the
// wrong place to look for hook support: Codex advertises it through a
// `hooks = true` feature flag in its own config, which provisioning sets.
// Absence of evidence in the places you thought to
// look is not evidence of absence — the same error this package exists to
// prevent the product from making about provider state.
package provider
