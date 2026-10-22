# Installation fonts

Public builds use Jost, EB Garamond and IBM Plex Mono. Their OFL license files
ship in `web/public/fonts/free`.

Run `pnpm build` from `web` to build with the bundled fonts. You can select them
explicitly with `NOISEDECK_FONTS=free pnpm build`. Docker builds accept the same
setting through `--build-arg NOISEDECK_FONTS=free`.

Font selection happens at build time. Rebuild after changing the setting.
Runtime environment variables cannot change the fonts in an existing image.

The default `auto` setting uses the bundled fonts unless you supply a complete
licensed set. `web/private/fonts/licensed.css` names the 42 WOFF2 files that set
needs. You supply them yourself and need permission to serve them to your users.
Keep them in one directory outside the checkout and set
`NOISEDECK_LICENSED_FONTS_DIR` to its absolute path for `pnpm build`. Docker
builds take the directory as `--build-context licensed-fonts=<directory>`, and
Compose builds add `compose.licensed-fonts.yaml`. Keep a licensed build, its
image and its build cache private.

`free` always selects the bundled fonts. `licensed` fails unless you supply the
complete licensed set. Both `auto` and `licensed` refuse an incomplete set
rather than mix licensed and bundled faces.
