import { readFileSync, readdirSync } from "node:fs";
import { basename, join, resolve } from "node:path";
import type { Plugin } from "vite";
import { selectFontSet } from "./select.ts";

export function installationFonts(sheet: string | undefined, publicDir: string, mode?: string, directory?: string) {
  // Vite copies publicDir wholesale, including files unused by the stylesheet.
  // Refuse old in-tree assets so even a free build cannot redistribute them.
  function checkPublic(directory: string) {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name);
      if (path === join(publicDir, "fonts", "free")) continue;
      if (entry.isSymbolicLink()) throw new Error("Public asset symlinks can copy unapproved fonts; use regular files instead");
      if (entry.isDirectory()) checkPublic(path);
      else if (/\.(woff2?|otf|ttf)$/i.test(entry.name)) {
        throw new Error("Move licensed fonts out of web/public/fonts and other public directories; supply NOISEDECK_LICENSED_FONTS_DIR instead (see docs/fonts.md)");
      }
    }
  }
  checkPublic(publicDir);
  const assets = new Map<string, Buffer>();
  const fontSet = selectFontSet({
    override: mode,
    licensedSheet: sheet,
    supplied: Boolean(directory),
    present(url) {
      try {
        const bytes = readFileSync(join(resolve(directory!), basename(url)));
        // Check the WOFF2 header and declared length, catching truncated copies.
        // Font decoding remains the browser's job; this is not a font sanitizer.
        if (bytes.length <= 48 || bytes.toString("ascii", 0, 4) !== "wOF2" ||
            bytes.readUInt32BE(8) !== bytes.length || bytes.readUInt16BE(12) === 0 ||
            bytes.readUInt32BE(20) === 0 || bytes.readUInt32BE(20) > bytes.length - 48) return false;
        assets.set(url, bytes);
        return true;
      } catch {
        return false;
      }
    },
  });
  const plugin: Plugin = {
    name: "installation-fonts",
    // These URLs are emitted below, rather than resolved from publicDir.
    config() {
      return { build: { rollupOptions: { external: [...assets.keys()] } } };
    },
    generateBundle() {
      for (const [url, source] of assets) this.emitFile({ type: "asset", fileName: url.slice(1), source });
    },
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const url = new URL(req.url ?? "/", "http://localhost").pathname;
        const base = server.config.base;
        const source = assets.get(base !== "/" && url.startsWith(base) ? `/${url.slice(base.length)}` : url);
        if (!source) return next();
        res.setHeader("Content-Type", "font/woff2");
        res.end(source);
      });
    },
  };
  return { fontSet, plugin };
}
