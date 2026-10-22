# Remote box, Tailscale and phone

Run Deck on a Linux machine that stays on, reach it over Tailscale, and
check on your agents from your laptop or your phone.

## Why a remote box

Deck, tmux and the agent CLIs run together on the Deck machine. Closing the
browser tab does not stop a session, so an agent keeps working while you are
away. If Deck runs on your laptop, its sessions pause when the laptop sleeps and stop when it shuts down.
On a machine that stays on, such as a cloud VM, a home server or a Coder
workspace, they keep going, and you pick up the same session from any device.

The machine needs what any Deck host needs: Linux on arm64 or on x86-64 with
AVX2, a local Docker engine with Compose and BuildKit, and `/etc/machine-id`.
See [Requirements](settings.md#requirements). Deck keeps its database and
provider home in Docker volumes, so use a machine that keeps those volumes
and your projects folder when it restarts. Stopping or recreating Deck's
container ends running sessions; your projects and Deck's records stay. See
[The container and your sessions](settings.md#the-container-and-your-sessions).

## Install on the box with Tailscale

Install [Tailscale](https://tailscale.com/download) on the box and sign in.
Then open a terminal on the box, over SSH or your workspace's terminal, start
a coding agent there and give it the install prompt from the
[README](../README.md). The [install skill](../skills/install-deck/SKILL.md)
checks Tailscale as part of its read-only pre-check. When it finds Tailscale
signed in, it offers "Open Deck from your other devices over Tailscale (your
tailnet only)" with the exact address, and recommends it on a remote or
headless machine.

If you say yes, the agent does this, as described in
[the Tailscale reference](../skills/install-deck/references/tailscale.md):

- It sets `NOISEDECK_PUBLIC_ORIGIN` to your machine's tailnet address, such
  as `https://HOST.TAILNET.ts.net`, in Deck's environment file. That becomes
  Deck's only trusted browser address, and its HTTPS scheme makes the sign-in
  cookies secure.
- It runs `tailscale serve --bg --https=443 http://127.0.0.1:PORT`. Deck still
  listens on 127.0.0.1 only, and Tailscale Serve is the only thing that
  reaches it. Only devices signed in to your tailnet can open the address.
- It checks that the address answers, that the API refuses a request with no
  sign-in, that every port Deck publishes is on 127.0.0.1, and that Funnel is
  off.
- It shows you the setup link, which now uses the tailnet address. Open it on
  any of your tailnet devices to create your password.

Some steps are yours to run, and the agent lists them in its plan:

- If HTTPS certificates are off for your tailnet, turn them on in the
  Tailscale admin console under DNS, HTTPS Certificates. This publishes your
  machine names and tailnet name in public certificate logs.
- If `tailscale serve` says access is denied, run
  `sudo tailscale set --operator="$USER"`. If it prints a link to enable Serve
  for your tailnet, open that link.

Know these before you choose the option:

- The tailnet address becomes Deck's only address. `http://127.0.0.1:PORT`
  stops working for sign-in, even on the box itself.
- Project previews open only in a browser on the Deck machine, so they cannot
  run with this option. Deck refuses to start with loopback previews on and a
  tailnet address set. You choose one or the other.
- Changing the address of a Deck that is already running restarts it and
  ends its running sessions.

The agent never runs `tailscale funnel` and never puts Deck on the public
internet. Deck runs agents with shell access to the box, so anyone who signs
in can run shell commands through its agents.

To turn it off, run `tailscale serve --https=443 off`, remove the
`NOISEDECK_PUBLIC_ORIGIN` line from the environment file, run your Compose
`up -d` command again, and check that `http://127.0.0.1:PORT/readyz` answers.
`tailscale serve reset` clears every Serve mapping on the machine, so use it
only if Deck's was the only one.

## On your phone

1. Install the Tailscale app and sign in to the same tailnet.
2. Open your Deck address, `https://HOST.TAILNET.ts.net`, in the phone's
   browser and sign in with your Deck password.
3. Use your browser's Add to Home Screen. Deck then opens from its own icon,
   full screen, without the browser's address bar.

On a phone-sized screen, Deck changes its layout:

- The **Sessions** button at the top opens your sessions as a sheet from the
  bottom of the screen. You can filter them, start a **New session**, open a
  project, and swipe a session left to close it.
- Below the terminal is a text box with a **Send** button. What you type, or
  dictate, stays in the box until you tap **Send** or press Return. Deck then
  pastes it into the session and presses Enter. Tap **Send** with the box
  empty to press Enter alone, for example to accept a prompt.
- A row of keys above it gives you `esc`, `tab`, a one-shot `ctrl`, an arrow
  pad with Backspace, Clear screen and Return, and `/`, `|` and `~`. Hold an
  arrow to repeat it.
- Drag the terminal up or down to scroll its output. **Jump to live** takes
  you back to the latest output.
- The **Panels** button opens Files, Tasks and the other panels, each across
  the full width.
- Starting a session on a phone does not open the keyboard. Tap the text box
  when you want to type.

## Security

- Deck is on your tailnet only. Never use Tailscale Funnel or any other way
  to put it on the public internet.
- Tailscale does not replace Deck's sign-in. Every device still signs in with
  your Deck password. See [Authentication](authentication.md).
- Deck is not a sandbox. Every session shares one Unix user, one provider
  home, your GitHub CLI login and everything under the projects directory.
  Put on the box only what you are willing to let every agent read and use.
