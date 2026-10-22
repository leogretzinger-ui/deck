---
name: update-deck
description: Update a running Noisefloor Deck to a public release with Docker Compose on macOS or Linux. Checks the existing install, proposes one plan, runs it after one go-ahead, and verifies that Deck works. Use when you ask to update or upgrade Deck.
---

# Update Noisefloor Deck

This skill updates a Deck that the [install skill](../install-deck/SKILL.md)
or Docker Compose set up. The new release is cloned beside your checkout and
then moved into the same folder, so Compose sees the same project directory,
relative paths and settings as before. The old checkout stays, renamed, for
a rollback. The update is done when you can use Deck again (step 5).

## Rules that protect you

- The agent never runs `sudo`; anything that needs admin rights goes in the
  plan for you to run. It never reads, stores or relays a password.
- It never widens the port binding, changes `NOISEDECK_AUTH_MODE` or
  `NOISEDECK_PUBLIC_ORIGIN`, runs `tailscale funnel` or puts Deck on the
  public internet.
- It never deletes, resets or re-owns the `deck-data` and `deck-home`
  volumes or the folders Deck mounts, never runs `docker compose down -v`,
  never fetches into the existing checkout and never deletes it.
- A failed check is reported with its fix, never worked around with another
  version, weaker settings or a made-up identity.

## 1. One opening question, only if needed

Ask: "Shall I check your Deck and propose an update plan (recommended), or go
step by step?" unless your prompt says. Step by step pauses at each action.

## 2. Pre-check (read-only, no questions)

- **Docker.** Record `docker context show` as CTX; every `docker` command
  below, Compose included, is `docker --context CTX`.
- **Native macOS.** On a Mac with no Deck container, a running `noise-deck`
  process is a native install: it updates as in the
  [native macOS procedure](../install-deck/references/native-macos.md). Say
  so and stop.
- **Existing Deck.** `docker ps -a --filter label=com.docker.compose.service=app --format '{{.ID}} {{.Label "com.docker.compose.project"}}'`;
  Deck's app container ID mounts the `deck-data` and `deck-home` volumes
  (`docker inspect --format '{{json .Mounts}}' ID`). Its labels
  `com.docker.compose.project`, `.project.working_dir`,
  `.project.environment_file` and `.project.config_files` give the project
  name, the checkout DIR, the env file ENV and the Compose files in order;
  the overlays are the files after `compose.yaml`. Note the sources of its
  bind mounts (Docker Desktop may prefix them with `/host_mnt`).
- **Local files.** Without changing the checkout:
  `GIT_OPTIONAL_LOCKS=0 git -C DIR -c core.fsmonitor=false status --porcelain --ignored`.
  Entries marked `??` or `!!` are untracked: a path ending in `/` is a
  folder, any other a local file to carry, such as ENV or a
  `compose.override.yaml`. Any other entry is a tracked file you changed.
- **Installed version** `vW`: `docker exec ID noise-deck version`, and
  `docker inspect --format '{{.State.Health.Status}}' ID`.
- **Settings.** In ENV, read only `NOISEDECK_BUILD_VERSION`,
  `NOISEDECK_IMAGE_TAG`, `NOISEDECK_HOST_PORT` (PORT, 18082 when unset),
  `NOISEDECK_SOURCE_ORG`, `NOISEDECK_HOST_ID_PATH` and whether
  `NOISEDECK_PUBLIC_ORIGIN` is set. Never print the whole file. If it is
  set, `tailscale serve status` shows the mapping to `127.0.0.1:PORT`.
- **Running sessions.** `docker exec ID tmux -S /run/noisedeck/tmux.sock list-sessions -F x | wc -l`;
  "no server running" means none.
- **Disk.** `df -h` where Docker stores images (on a Mac, its VM disk). A
  full build needs roughly 20 GB.
- **Environment.** `DC` is the Compose command that started Deck, with
  this shell's `NOISE` variables cleared so ENV alone decides:
  `env -u NAME… docker --context CTX compose --project-directory DIR --env-file ENV -p PROJECT -f DIR/compose.yaml`
  plus `-f DIR/<overlay>` per overlay in order, one `-u` per name from
  `awk 'BEGIN { for (k in ENVIRON) if (k ~ /^NOISE/) print k }'` (none, no
  `env`). `CONFIG` is `DC config --format json`. Three checks find what the
  running Deck has that ENV does not give it; compare values privately and
  show and report names only, never values:
  - Settings: `RUNNING` is
    `docker --context CTX inspect ID --format '{{range .Config.Env}}{{println .}}{{end}}' | grep '^NOISEDECK_'`; then
    `comm -23 <(RUNNING | sort) <(CONFIG | jq -r '.services.app.environment | to_entries[] | "\(.key)=\(.value)"' | grep '^NOISEDECK_' | sort) | cut -d= -f1`
    prints each NAME.
  - User: `docker --context CTX inspect ID --format '{{.Config.User}}'` must
    equal `CONFIG | jq -r '.services.app.user // ""'`.
  - Mounts:
    `comm -3 <(docker --context CTX inspect ID --format '{{range .Mounts}}{{if eq .Type "bind"}}{{println .Destination .Source}}{{end}}{{end}}' | sed 's# /host_mnt/# /#' | sort) <(CONFIG | jq -r '.services.app.volumes[] | select(.type=="bind") | "\(.target) \(.source)"' | sed 's# /host_mnt/# /#' | sort) | awk '{print $1}' | sort -u`
    prints the destination of each bind mount that differs.
- **Release.** Unless your prompt names one, the latest stable `vX.Y.Z` from
  `https://api.github.com/repos/OWNER/deck/releases/latest`, with no
  credentials; OWNER is `NOISEDECK_SOURCE_ORG`, else `noisefloor-oss`.
  Every networked `git` is `GIT`: it runs in a new empty folder,
  `T=$(mktemp -d)`, ignores all Git config, prompts, redirects and Git
  variables (behind a proxy, also pass `HTTPS_PROXY`), and its arguments
  replace `…`:
  `(cd "$T" && env -i PATH="$PATH" GIT_CEILING_DIRECTORIES="${T%/*}" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0 git -c http.followRedirects=false …)`.
  Prove the tag's commit with
  `GIT ls-remote https://github.com/OWNER/deck.git 'refs/tags/vX.Y.Z' 'refs/tags/vX.Y.Z^{}'`:
  the `^{}` line if present, else the tag line, one 40-character SHA.

## 3. The plan

One message; every line carries a default you can change.

- **Version** `vW` to `vX.Y.Z` and its commit. Already on it: check that the
  app is healthy and stop. An older target is not an update; stop.
- **When.** "Updating stops Deck, which ends every running session; N are
  running now. Deck stays down while the new release builds. Your projects,
  settings, logins and history are kept." Default: now.
- **Backup first**, as in [docs/backup-restore.md](../../docs/backup-restore.md),
  to `/data/backups/before-X.Y.Z-<UTC time>.db` in the `deck-data` volume.
- **Folders.** `vX.Y.Z` is cloned into NEW, `DIR.new-X.Y.Z`. Once Deck
  stops, DIR becomes PREV, `DIR.previous-vW`, and NEW becomes DIR. Remove
  PREV yourself once the new version works for you.
- **Local files**, each named, copied unchanged from PREV into DIR. If the
  new release already has one of those paths, the update stops before Deck
  does. Untracked folders, each named, stay in PREV; move them after the
  update if you want them.
- **Env file.** Only `NOISEDECK_BUILD_VERSION` and `NOISEDECK_IMAGE_TAG`
  change, to `X.Y.Z`. An ENV outside DIR is kept as `ENV.before-X.Y.Z`.
- **Same command**: project, env file and overlays in order.
- **What you run first**, only if needed: free disk, or start Docker (Mac:
  the agent runs `open -a Docker`; Linux: `sudo systemctl start docker`).
- **Stop and ask** instead of planning when: no Deck or several; an unknown
  checkout, env file or overlay; a bind mount source in DIR (with
  `NOISEDECK_PROJECTS_HOST_PATH` unset your projects live in the checkout,
  and the move would hide them); a tracked file you changed; or anything
  the environment check prints. Show each name with the setting it needs
  (the user: `NOISEDECK_RUNTIME_UID` and `NOISEDECK_RUNTIME_GID`;
  `/workspace`: `NOISEDECK_PROJECTS_HOST_PATH`; `/home/agent/.config/gh`:
  `NOISEDECK_GH_CONFIG_HOST_PATH`; `/run/noisedeck-host-machine-id`:
  `NOISEDECK_HOST_ID_PATH`; an overlay's mount: the variable in its
  `source:`), point to [Settings](../../docs/settings.md), and stop; the
  update starts again once ENV holds them. Never guess.

## 4. One go-ahead, then run it end to end

`DC` is the Compose command from step 2.

1. `GIT clone --branch vX.Y.Z --depth 1 https://github.com/OWNER/deck.git NEW`
   (an absolute path); `git -C NEW rev-parse HEAD` must equal the proved
   SHA. Each overlay that is not a local file must exist in NEW, PREV must
   not exist, and for each local file F `test -e NEW/F` must fail and
   `test -d "NEW/$(dirname F)"` pass; otherwise stop and show what failed.
2. Mac with `NOISEDECK_HOST_ID_PATH`: `sh NEW/skills/install-deck/scripts/macos-host-id.sh HOST_ID_FILE`
   only compares, and prints nothing.
3. `docker --context CTX exec ID noise-deck backup -dest /data/backups/before-X.Y.Z-$(date -u +%Y%m%dT%H%M%SZ).db`
   must report success.
4. At the moment from the plan: `DC stop`.
5. `mv DIR PREV && mv NEW DIR`.
6. Each local file F: `cp -p PREV/F DIR/F`.
7. If ENV is outside DIR, `cp -p ENV ENV.before-X.Y.Z`. Then set
   `NOISEDECK_BUILD_VERSION` and `NOISEDECK_IMAGE_TAG` to `X.Y.Z`, one
   `NAME` at a time, keeping mode 600 and printing nothing:
   `(umask 077 && { grep -v '^NAME=' ENV; printf "%s='%s'\n" NAME X.Y.Z; } > ENV.new) && mv ENV.new ENV`.
8. `DC up --build -d`. Wait for `DC ps` to show app healthy;
   `DC exec -T app noise-deck version` must print `X.Y.Z`. The database
   migrates on this first start.

## 5. Done, checked by the agent (report each result)

1. `curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:PORT/` answers.
2. `DC exec -T app noise-deck doctor` reports no required failures.
3. Tailscale only: the checks in step 2 of the
   [Tailscale setup](../install-deck/references/tailscale.md#setup-after-you-say-yes).
4. You open Deck (your tailnet address with Tailscale) and sign in with
   your existing password; your projects are there. A setup page means no
   password existed yet: show the setup link as step 5.3 of the
   [install skill](../install-deck/SKILL.md) says.
5. You start a session and give your agent one tiny task, such as "reply
   with OK", without signing in again. Your yes is the check.

Report a failure with its step, its output (no secrets) and the fix.

## Roll back

Only when you ask, or when step 4 or 5 fails and you choose it. Before step
4.4 Deck still runs as it was; just remove NEW. Rolling back restarts Deck.

1. Put the old checkout back as far as step 4.5 got; step 4.1 proved PREV
   did not exist before it. Run each command on its own, so one that fails
   cannot skip the next:
   - Before the swap (`test -d PREV` fails, for example `DC stop` failed):
     DIR is still the old checkout, nothing moves.
   - After only the first rename: `test -d PREV && ! test -d DIR && mv PREV DIR`.
   - After the full swap, before startup (step 4.6 or 4.7 failed): Deck is
     already stopped, and `DC` may not load, so restore directly:
     `test -d PREV && test -d DIR && mv DIR DIR.failed-X.Y.Z`, then
     `test -d PREV && ! test -d DIR && mv PREV DIR`.
   - After the full swap, once step 4.8 ran: if
     `docker --context CTX ps -q --filter label=com.docker.compose.project=PROJECT`
     lists anything, stop it with the old checkout's files: `DC stop` with
     PREV in place of DIR in every path, the env file included. Whether or
     not that works, then run the two commands of the case above.
2. If ENV is outside DIR: `test -f ENV.before-X.Y.Z && cp -p ENV.before-X.Y.Z ENV`.
3. `DC up -d` (add `--build` if `docker image inspect deck:OLD_TAG` fails,
   with OLD_TAG the old `NOISEDECK_IMAGE_TAG`, or `local` when unset). Wait
   for healthy; `DC exec -T app noise-deck version` prints `W`.
4. If it stays unhealthy because the new version migrated the database,
   restore the backup from step 4.3. That loses changes made since, so say
   so and wait for your yes. Then `DC stop`,
   `DC run --rm --no-deps --entrypoint /usr/local/bin/noise-deck app restore -from /data/backups/FILE`,
   `DC up -d` and wait for healthy.
5. Run the checks in step 5. `DIR.failed-X.Y.Z` or NEW stays for a later look.
