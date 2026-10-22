# Building from source without Docker

```sh
cd web
pnpm install --frozen-lockfile
cd ..
NOISEDECK_FONTS=free make build
./build/noise-deck serve -config configs/config.example.yaml
```

`make build` compiles the production browser UI and embeds it in the
backend binary. A plain `go build` serves a `503` at `/` until the UI has
been staged.

The build needs Go with cgo and a C toolchain, Node, pnpm and Make. Check
[configs/config.example.yaml](../configs/config.example.yaml) against your
local paths before starting. Running the binary also needs tmux and the
agent CLIs on the host; the binary installs neither.

Run `./build/noise-deck doctor --config configs/config.example.yaml` with the same
configuration and service environment as `serve` to check the tools on Deck's
PATH, the Tasks version, data-directory access, and the listen address.
Use `--json` for a machine-readable report. The command exits with status 1
when a required check fails. Missing optional providers do not fail it.
Slow provider version probes produce warnings. The same report is available at
authenticated `GET /api/doctor`, which reuses reports for 60 seconds and serves
the last report while refreshing it.

## Frontend development

`make dev` runs the backend and the hot-reloading UI together. Open
`http://127.0.0.1:8837/__dev/` through the backend. Its default local mode
has no sign-in and admits only declared loopback origins, so do not expose
it as a public server.

To develop against authenticated mode, run
`NOISEDECK_AUTH_MODE=password make dev`, sign in at `/`, then open
`/__dev/`.
