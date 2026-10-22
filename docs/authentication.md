# Authentication

## First sign-in

Password authentication is the default. Open the setup link from startup
output to create your password. The link expires after 30 minutes or when
you save your password. Deck stores only an adaptive password hash and an
independent session-signing secret in `auth-state.json` beside the database.
The private `setup-url` file in that directory contains the link so your
launcher can open it. Deck deletes the file after you create your password.
Protect access to this file and retained startup output. An install agent
may read and show you the link once; it must never read or relay a password.

If the link expires, open it again. Deck issues a fresh link in the same output
as the startup link and updates the private `setup-url` file. Open that new link
to continue. To change your password, choose **Change password** in Session
settings and enter your current password.
Changing your password signs out other browser sessions and keeps your current
browser signed in.
If you forget it, reset your password with Deck stopped. For the default
Compose install, run these commands from your source checkout, using the same
environment file and Compose overlays as your install:

```sh
docker compose stop app
docker compose run --rm --no-deps app noise-deck password reset
docker compose start app
```

The throwaway container uses the app's image, service user, configuration
and data volume. Open the printed setup link after Deck starts.

For a native install, stop your service and run the command as its service
user with the same environment and configuration file:

```sh
noise-deck password reset --config /path/to/your/config.yaml
```

Start your service and open the printed link. Reset signs out existing browser
sessions. The binary may be installed as `noisedeck`; `noise deck password reset`
also works through the plugin. No reset command accepts a password argument.

If you pin a hash with `NOISEDECK_AUTH_PASSWORD_HASH`, remove that setting
before resetting, or remove it and restart before changing your password.
A new or changed password needs at least 8 characters and at most 72 bytes.
Existing passwords, including unused bootstrap passwords from older releases,
continue to sign in.

The tutorial opens on first load. Its **Get started** lessons set up a
first project, and **Explore Deck** introduces the other features. Closing
the tutorial saves your place. Reopen it with **Show tutorial** in the
global three-dot menu.

To connect GitHub, choose **Connect GitHub** during Git identity setup or
**GitHub connection…** in the global menu. See
[Connect GitHub](github-connection.md). Each agent CLI signs in to its
provider through its own native flow, and Deck cannot bypass enterprise
account policy.

## Authentication modes

`NOISEDECK_AUTH_MODE` selects one of:

- `password` (default).
- `oidc`: Google OpenID Connect for a single account.
- `trusted_proxy`: an authenticating reverse proxy owns login.
- `none`: no sign-in, admitted only when every declared browser origin is
  loopback.

There is no fallback between modes. Incomplete configuration of a
protected mode fails startup. The base Compose file does not forward the
trusted-proxy settings or a pinned password hash; you must supply those in
your own Compose overlay.
