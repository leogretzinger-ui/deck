# Open Deck from your other devices over Tailscale

This puts Deck on your tailnet only: HTTPS at `https://HOST.TAILNET.ts.net`,
reachable from your signed-in Tailscale devices and nobody else. Deck keeps
listening on 127.0.0.1; `tailscale serve` is the only thing that reaches it.

## Pre-check (read-only)

- Try `command -v tailscale`. On a Mac without that command, also check for
  `/Applications/Tailscale.app/Contents/MacOS/Tailscale` and the same path
  under `~/Applications`. If executable, use its absolute path with
  `TAILSCALE_BE_CLI=1` for every Tailscale command below. No symlink or shell
  configuration change is needed. An app that fails its status check is
  installed but unavailable, not absent; report the error in the plan.
- `tailscale status --json`. Read these fields and nothing else:
  - `BackendState`: `Running` means signed in and connected. `NeedsLogin` or
    `Stopped` means you have to sign in or start it.
  - `Self.DNSName`: this machine's name, with a trailing dot. HOST is that name
    without the dot, so the address is `https://` plus it.
  - `CertDomains`: empty or missing means HTTPS certificates are off for your
    tailnet.
- `tailscale serve status`: note anything already served. Never replace it.
- Battery, SSH and display: what the main pre-check recorded.

## What goes in the plan

- **Running, signed in, certificates on:** offer "Open Deck from your other
  devices over Tailscale (your tailnet only)" and show the exact address.
  Recommend yes when the machine looks remote or headless, no when it has a battery.
- **Installed but signed out or stopped:** steps for you: `tailscale up`, then
  sign in in your browser.
- **Certificates off:** a step for you: in the Tailscale admin console, open the
  DNS page and, under HTTPS Certificates, choose Enable HTTPS. Say that this
  publishes your machine names and tailnet name in public certificate logs.
- **Not installed:** do not raise it unless you asked for remote access. Then
  installing it is a step for you, with admin rights, from
  <https://tailscale.com/download>.
- **`tailscale serve` already uses 443:** stop and ask; never overwrite it. A
  mapping that already points at this Deck's `127.0.0.1:PORT` is fine; skip
  step 1 below and run the checks.
- Always say these three things:
  - Deck's address becomes this one address. `http://127.0.0.1:PORT` no longer
    works for signing in, so you open the Tailscale address on this machine too,
    and you sign in again.
  - Project previews (`compose.previews.yaml`) only open in a browser on the Deck
    machine, and they cannot run with this option. Deck refuses to start when
    the loopback previews are on and its address is not a plain-HTTP loopback
    one. If you chose previews, you choose between the two; the default is the
    option that fits where you sit. The agent never drops the previews overlay
    on its own.
  - Changing the address on a Deck that is already running restarts it and ends
    its running sessions.

## Setup, after you say yes

Docker. For a new install, add
`NOISEDECK_PUBLIC_ORIGIN=https://HOST.TAILNET.ts.net` to the env file in step 4.4
with no trailing slash and no port, and use the same address everywhere below.
For a Deck you are keeping, add that line to its env file, run `DC up -d` after
you agree to the restart, and wait for healthy.

That one value is Deck's only trusted browser address. It sets the allowed
origins and the sign-in callback, and the HTTPS scheme makes the sign-in cookies
secure. Deck refuses to start if the scheme is mixed or the address has a slash
at the end.

Then:

1. `tailscale serve --bg --https=443 http://127.0.0.1:PORT`. If it says access
   is denied, a step for you: `sudo tailscale set --operator="$USER"`. If it
   prints a link to enable Serve for your tailnet, that is a step for you too.
2. Check, and report each result:
   - `curl -sS -o /dev/null -w '%{http_code}' https://HOST.TAILNET.ts.net/readyz`
     prints `200`.
   - `curl -sS -o /dev/null -w '%{http_code}' https://HOST.TAILNET.ts.net/api/providers`
     with no sign-in prints `401` or `403`, not `200`.
   - `DC ps --format '{{.Ports}}'` shows `127.0.0.1:PORT->` for Deck. Every
     other published binding, such as `127.0.0.1:18766->8766` for memory, must
     also start with `127.0.0.1:`. Any other address means stop and report.
   - `tailscale serve status` shows the address going to `127.0.0.1:PORT` and no
     `Funnel on` line.
3. Show the setup link as step 5.3 does. It opens only on devices signed in to
   your tailnet.

To turn it off: `tailscale serve --https=443 off`. `tailscale serve reset`
clears every Serve mapping on this machine, so use it only if Deck's was the
only one. Then remove the `NOISEDECK_PUBLIC_ORIGIN` line, run `DC up -d`, and
check that `http://127.0.0.1:PORT/readyz` answers `200`.

## Never

Never run `tailscale funnel` or anything else that puts Deck on the public
internet. If you ask for public access, explain that Deck runs agents with shell
access to this machine, so anyone who reached it could use them, and stop.

## Native macOS

The same option fits, with these differences. Use the command or app-bundle
executable found in the pre-check above.

- Setup: in the configuration, set
  `allowed_origins: ["https://HOST.TAILNET.ts.net"]`. With your yes, restart
  with `launchctl kickstart -k gui/$(id -u)/LABEL` (see
  [native-macos.md](native-macos.md)), wait for `/readyz` on the loopback
  address, then run `tailscale serve` as in the steps above.
- Checks: the `/readyz` and `/api/providers` checks and the `tailscale serve
  status` check are the same. Instead of `DC ps`, run
  `lsof -nP -iTCP -sTCP:LISTEN -a -p PID` for the launchd job's PID: every
  listener must be on `127.0.0.1`. Read the setup link from the `setup-url`
  file.
- Undo: `tailscale serve --https=443 off`, set `allowed_origins` back to
  `["http://127.0.0.1:PORT"]`, restart with `launchctl kickstart -k`, and check
  that `http://127.0.0.1:PORT/readyz` answers `200`.
