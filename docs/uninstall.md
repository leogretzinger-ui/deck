# Uninstall and what data remains

`docker compose down` stops and removes the container and the network.
Afterwards, these remain:

- The named volume `deck-data`: the SQLite control database, the password
  hash, the session-signing secret and the host-binding receipt.
- The named volume `deck-home`: the provider home, holding agent CLI
  credentials, native session history, hooks and container-local GitHub
  CLI state.
- The host directories you mounted (the projects root and the GitHub CLI
  configuration). Deck never deletes them.
- The locally built images (`deck:local` by default).

Delete both volumes with `docker compose down -v`, or one at a time with
`docker volume rm`. Remove the images with `docker rmi`.

Nothing else is written outside those volumes and mounts.
