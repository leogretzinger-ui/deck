# Backend code map

The backend is a Go application that runs native agent CLIs through tmux.
`cmd/noisedeck` parses commands and configuration; `internal/boot` assembles
the application. Packages under `internal/` own the behavior and persistence
for their domains.

## Finding the code

| Package | Responsibility |
| --- | --- |
| `boot` | Startup order, dependency wiring, readiness, and backend shutdown. Start with `Up` in `boot.go`; lifecycle, authentication configuration, providers, and encrypted-state checks have separate files. |
| `server` | HTTP routing, middleware, health endpoints, and embedded browser assets. |
| `config` | Deployment configuration and validation. |
| `session` | Session lifecycle, recorded state, launch, recovery, and stopping. |
| `tmux` | Commands against Deck's dedicated socket: pane launch, observation, removal, input, and scrolling. |
| `terminal` | Browser terminal WebSocket transport and attachment ownership. |
| `reconciler` | Reconciliation of recorded sessions against tmux and process observations. |
| `provider` | Native CLI launch and resume descriptors. |
| `project` | Project registration, repository inspection, cloning, and Git author identity. |
| `tasks`, `taskengine`, `lane` | Integration with the pinned Tasks engine and task worktrees. Task state belongs to the engine-selected repository-family store. |
| `chats`, `harnesslogin`, `githubconnect`, `githubrepos` | Integration-specific clients, services, and HTTP handlers. |
| `apppreview` | Project preview routing, configuration, and listener lifecycle. |
| `auth`, `lease`, `inputgate` | Browser authentication, terminal control leases, and serialized input admission. |
| `db`, `settings`, `audit` | Control database, user preferences, and audit records. |
| `sealedstate`, `workspace*` | Storage qualification and the browser-managed workspace gateway, keys, grants, native runtime, and migration. |
| `projectpolicy`, `supervisor` | Project authority policy and workload launch enforcement. |

Follow a request from its handler to the owning service or manager and then
to its repository or external CLI adapter. Keep HTTP details in handlers and
state transitions in the domain package. A database row records control
state; observed tmux and process facts determine runtime state. Terminal
output remains opaque to status and identity logic.

## Verification

Tests sit beside their packages. tmux-backed tests use temporary sockets and
databases. They must never connect to your default tmux socket.
The tmux, session, terminal, reconciler, tasks, and acceptance suites have
timing-sensitive checks; run them serially as CI does:

```sh
GOMAXPROCS=1 go test -count=1 -p 1 ./internal/tmux ./internal/session \
  ./internal/terminal ./internal/reconciler ./internal/tasks ./internal/acceptance
go test ./internal/boot ./cmd/noisedeck
go vet ./...
make lint
```

Use a short temporary directory when the host's `TMPDIR` is long: Unix socket
paths have a platform length limit. `make verify` runs the portable Go gate;
`make build` builds the browser and embeds its assets in the backend.
Real-CLI release acceptance is a separate gate and requires the configured
provider logins.
