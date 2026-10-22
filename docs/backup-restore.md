# Backup and restore

The tested procedure for the SQLite control database.

## What a backup contains

Control metadata only: projects, sessions and their recorded lifecycle,
controller leases, task runs, lifecycle/audit events, and the
`schema_migrations` ledger. The database never stores terminal input or
output, prompt bodies, keystroke logs, or provider transcripts — Deck owns no
durable input surface, nothing typed is ever queued or replayed, and logs
carry identifiers and state transitions only — so a backup cannot contain them
either.

A backup is still credential-sensitive: it carries fencing tokens and
session/audit records, which are private to the control domain and belong
neither in logs nor in source control. Backup files are written owner-only
(0600) and must live outside project workspaces and source control — a
workspace is browsable through the console's files panel, and a committed
backup leaks the control state into git history. Keep them on a local
filesystem with SQLite-compatible locking, same as the live database.

## Taking a backup (backend running)

```sh
noise-deck backup \
  -config /etc/noisedeck/config.yaml \
  -dest /var/backups/noisedeck/control-$(date -u +%Y%m%dT%H%M%SZ).db
```

The backup is online and WAL-safe: it uses SQLite's `VACUUM INTO`, which
reads through the WAL rather than around it, so no committed row still in the
`-wal` sidecar is missed and the service — and every agent session — keeps
running. The command does not take the exclusive backend lock; a backup
reader is not a second backend.

The result is a single self-contained, checkpointed file with no sidecars,
consistent at one point in time. The destination must not already exist: the
command refuses to overwrite, because whatever sits there is an older
recovery point or a file you put there. A timestamped name (as above) makes
that refusal a feature rather than an inconvenience.

### Encrypting the backup

A backup that leaves the host should be encrypted. Pass an age X25519
public key and the file is written sealed to that recipient:

```sh
noise-deck backup \
  -config /etc/noisedeck/config.yaml \
  -recipient age1... \
  -dest /var/backups/noisedeck/control-$(date -u +%Y%m%dT%H%M%SZ).db.age
```

With a recipient, the plaintext copy stages beside the live database, under
the same directory and therefore the same encrypted state root, and the
destination directory receives ciphertext and nothing else: a complete
sealed file, or, if the process dies mid-write, an owner-only partial that
is also ciphertext. The no-replace rule is unchanged. Without a recipient
the command writes the plain file and says so once on stderr, so a
forgotten flag is noticed before the copy leaves the host. Generate a key
pair with `age-keygen`; the identity file it writes is what restore needs.

## Restoring

1. **Stop the service.** `systemctl stop noisedeck`, send the process
   SIGTERM, or `docker compose stop` for the Compose deployment. The restore
   command refuses while a backend holds the exclusive lock, so the service
   must be stopped first; a clean stop also checkpoints the WAL and removes
   the `-wal`/`-shm` sidecars. Whether agent processes survive this step
   depends on deployment topology. With a standalone backend and an external
   tmux namespace, stopping only the backend leaves sessions alive — tmux
   holds the provider process, not the Go service.
   The current Docker Compose canary co-locates the Go service, tmux, and
   providers in one container, so `docker compose stop`, restart, replacement,
   or recreation terminates every session process. Persistent control state
   and provider-native home survive, but live PIDs, PTYs, and terminal screens
   do not. Cross-host migration is therefore a cold cutover: stop, back up,
   and restore on the new host.
2. **Run the restore command.**

   ```sh
   noise-deck restore \
     -config /etc/noisedeck/config.yaml \
     -from /var/backups/noisedeck/control-20260101T000000Z.db
   ```

   An encrypted backup needs the matching age identity file:

   ```sh
   noise-deck restore \
     -config /etc/noisedeck/config.yaml \
     -from /var/backups/noisedeck/control-20260101T000000Z.db.age \
     -identity /etc/noisedeck/backup-identity.txt
   ```

   The backup is decrypted into an owner-only staging file beside the live
   database and verified there exactly as a plain backup would be. A wrong
   identity, an altered or truncated file, or an encrypted backup given
   without `-identity` is refused before anything is fenced or replaced, and
   the live database is untouched.

   The command does the following, in order:
   - refuses while a backend holds the exclusive lock, and holds the lock for
     its own duration;
   - verifies the backup's `schema_migrations` ledger exactly matches the
     migrations embedded in this binary — every applied row known and
     unmodified, and every embedded migration applied. An incompatible or
     missing backup changes nothing on the host;
   - empties the current database's write-ahead log, so every committed row
     lives in the main file and no leftover `-wal`/`-shm` sidecar can be
     replayed over the restored database. A checkpoint blocked by another
     connection refuses the restore; a current database that cannot be
     opened at all is logged and replaced, and sidecars without a database
     are removed;
   - fences the entire tmux namespace — kills the tmux server on the
     dedicated socket, terminating every session and workload in it,
     including sessions started after the backup that the restored database
     has no rows for;
   - installs the backup atomically and owner-only (0600).

   Arbitrary manual database replacement is unsupported: a
   restored database must not be opened beside workloads it cannot account
   for, which is why the fence is part of the command rather than your
   responsibility.
3. **Start the service.** Startup is the verification, and it fails closed:
   - opening the database re-verifies every recorded migration's checksum
     against the binary; a backup that does not match refuses startup and
     the log names the mismatch;
   - the startup reconciliation pass compares restored rows against tmux and
     process reality, because status is observed and never taken from the
     last write. A restored `running` session whose pane did not survive — a
     host reboot, a lost tmux namespace — comes back `interrupted`, never
     presented as running;
   - nothing is replayed. Restore triggers no automatic prompt replay, and a
     legacy `needs_attention` row converges in place before the first response
     (passive chrome, and one automatic
     successor only if its recorded generation qualifies), with no manual resolution.
4. **Check the result.** `/readyz` returns 200, and the session list matches
   what tmux actually holds.

## Scope: pre-Continuity

This release's schema holds no Continuity grants, credentials, or confined
project-policy state. The restore command above fences the tmux namespace
and replaces the database, and that fence is the whole procedure for this
release. The supported restore for an installation with Continuity
state adds the remaining steps — rotating the runtime-incarnation secret and
invalidating restored grants and credentials — before normal service resumes.
Those steps land here with the feature.
