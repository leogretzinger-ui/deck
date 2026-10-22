# Project previews

A project preview opens the app your agent is building in its own browser
tab, served by the project's dev server.

For a loopback install, enable private previews without DNS or TLS:

```sh
docker compose -f compose.yaml -f compose.previews.yaml up -d
```

Enable a preview in the project menu, start its declared dev server with
`noise deck preview run`, then choose **Open preview**. Each app opens on
its own loopback address, such as `http://127.0.0.2:8840`, with support
for assets, routes, APIs and WebSocket hot reload. Use a browser on the
same machine. Linux supports the addresses by default, while macOS needs
loopback aliases that you configure before use.

For a hosted install, you configure a separate preview domain, HTTPS
ingress and local app endpoints. See
[Private project app previews](project-app-previews.md).

Dev servers remain part of the unconfined workspace. Previews do not
sandbox them.
