import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import tailwindcss from "@tailwindcss/vite";
import { installationFonts } from "./src/styles/fonts/installation.ts";

const embedBuild = process.env.NOISEDECK_EMBED_BUILD === "1";

// Dev-preview mode. The backend can serve this dev server same-origin at
// /__dev/ behind the browser authentication (experiments.dev_preview), so a
// frontend change is visible on a running deployment without rebuilding the
// embedded assets. The backend does NOT strip the prefix — Vite owns the
// whole subtree and must emit its asset and HMR URLs under it, or they would
// collide with the compiled app's own static surface.
//
// Off (the default), everything below is inert and `pnpm dev` behaves exactly
// as it always has: base "/" on http://127.0.0.1:5173.
const devPreviewBase = process.env.NOISEDECK_DEV_PREVIEW_BASE ?? "";
// The origin the user's browser uses, which behind TLS is not this
// server's. Vite's HMR client derives its WebSocket URL from where the page
// was served, so through a proxy it has to be told the public port and
// scheme or it dials 5173 directly and never connects.
const devPreviewHost = process.env.NOISEDECK_DEV_PREVIEW_HOST ?? "";
const devPreviewTLS = process.env.NOISEDECK_DEV_PREVIEW_TLS === "1";

const fontsDir = new URL("./src/styles/fonts/", import.meta.url);
const licensedSheet = new URL("./private/fonts/licensed.css", import.meta.url);
const { fontSet, plugin: fontsPlugin } = installationFonts(
  process.env.NOISEDECK_FONTS !== "free" && existsSync(licensedSheet) ? readFileSync(licensedSheet, "utf8") : undefined,
  fileURLToPath(new URL("./public", import.meta.url)),
  process.env.NOISEDECK_FONTS,
  process.env.NOISEDECK_LICENSED_FONTS_DIR,
);
const fontsSheet = fileURLToPath(fontSet === "licensed" ? licensedSheet : new URL("free.css", fontsDir));

// The backend binds 127.0.0.1:8837 by default (configs/config.example.yaml).
// In development Vite proxies API and WebSocket paths there, so the browser
// talks to one origin and the strict-origin posture holds in both
// modes. Production serving is the backend's job (static assets task), not
// Vite's.
export default defineConfig({
  plugins: [tailwindcss(), fontsPlugin],
  ...(devPreviewBase ? { base: devPreviewBase } : {}),
  define: {
    // The font bench reads this to know which faces are the build's own
    // reference (src/bench/data.ts).
    __NOISEDECK_FONTS__: JSON.stringify(fontSet),
  },
  resolve: {
    alias: [{ find: /^@fonts$/, replacement: fontsSheet }],
  },
  server: {
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
    // Behind the backend's proxy the Host header is the deployment's public
    // hostname, which Vite rejects by default as a DNS-rebinding guard.
    ...(devPreviewHost ? { allowedHosts: [devPreviewHost] } : {}),
    ...(devPreviewBase
      ? {
          hmr: {
            // A path of its own under the proxied subtree, so the upgrade is
            // unambiguous to both the backend's route and Vite's own router.
            // RELATIVE to base: Vite joins the two, so a leading base here
            // would resolve as /__dev/__dev/@vite-hmr.
            path: "@vite-hmr",
            ...(devPreviewHost ? { host: devPreviewHost } : {}),
            ...(devPreviewTLS ? { protocol: "wss", clientPort: 443 } : {}),
          },
        }
      : {}),
    // In dev-preview mode the browser is already on the backend's origin, so
    // it reaches /api and /auth directly and these never fire. They remain
    // for the plain loopback workflow, where Vite is the only origin.
    proxy: {
      "/api": { target: "http://127.0.0.1:8837", changeOrigin: false, ws: true },
      "/auth": { target: "http://127.0.0.1:8837", changeOrigin: false },
    },
  },
  build: {
    outDir: "dist",
    // Deployable binaries carry neither the visual preview nor source maps.
    // Normal local builds retain both for design and debugging work.
    sourcemap: !embedBuild,
    rollupOptions: {
      // The design preview stays out of deployable binaries. The font bench
      // ships in every build because its experiment flag serves it.
      input: embedBuild
        ? { main: "index.html", bench: "bench.html" }
        : { main: "index.html", preview: "preview.html", bench: "bench.html" },
    },
  },
});
