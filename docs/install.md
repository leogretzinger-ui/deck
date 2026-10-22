# Installing by hand

Follow these steps to install Deck without a coding agent.

Download the source archive from the latest
[release](https://github.com/noisefloor-oss/deck/releases), or clone the
repository. Deck builds its image locally; no prebuilt image is published.
On the Linux machine where Deck will run, open the source directory and run:

```sh
docker compose up --build -d
docker compose logs app
```

Before the first build, check the [requirements](settings.md#requirements)
and that the [settings](settings.md) fit your host, especially the runtime
user and group and the projects directory. On a Mac, follow the
[installer skill](../skills/install-deck/SKILL.md) first. The first boot
prints a setup link in the logs. Read it yourself, then open it to create
your password; see [Authentication](authentication.md). To update, follow the
[update skill](../skills/update-deck/SKILL.md).
