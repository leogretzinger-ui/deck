// Real-browser acceptance proof for private project app previews
// (docs/project-app-previews.md). Everything here is the real stack: the
// real Go backend (boot.Up, password auth, the preview gateway), the real
// Deck UI served by the real dev Vite server, two real sample Vite apps,
// and one real browser through the pinned Playwright resolver. Nothing is
// mocked, no route is fulfilled, and no signed-in session is touched.
//
// Topology (all synthetic, all inside one scratch directory in this
// worktree, all cleaned up on exit):
//
//   browser ─CONNECT proxy (fixed synthetic hosts only, everything else refused)
//         └─ TLS edge (self-signed cert, one listener)
//              ├─ deck.example.com  /api,/auth,/livez → Go backend
//             │                     everything else   → Deck UI dev Vite
//              ├─ one.example.net   → preview gateway → sample-one Vite
//              └─ two.example.net   → preview gateway → sample-two Vite
//
// The proxy is the DNS story: the browser never resolves the synthetic
// names, it CONNECTs through the proxy, which maps exactly the three fixed
// hosts to the TLS edge and refuses every other destination — so a Vite
// HMR fallback that tried to dial a dev server directly cannot succeed
// silently. The edge terminates TLS exactly like the deployment's ingress
// and forwards bytes with Host, Origin, Sec-Fetch-* and cookies untouched.
//
// Prerequisites (the harness reports missing ones instead of improvising):
//
//   go build ./web/tools/fixtures/project-preview   (it does this itself)
//   web/node_modules installed (pnpm install in web/)
//   the browser sidecar via NOISE_BROWSER_WS, or a local Chromium
//
// Run it from web/:  node tools/project-app-preview-probe.mjs
// Add --managed to allocate both previews through authenticated APIs, prove
// deterministic CLI launches, disable/re-enable revocation, and restart
// against the same database.
// Add --loopback for managed previews on distinct 127.0.0.x addresses over
// HTTP, without the CONNECT proxy or
// TLS edge. Deck's Vite dev server proxies its API to the fixture backend.
import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import http from "node:http";
import https from "node:https";
import net from "node:net";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { eventuallyEqual } from "./eventually.mjs";

if (process.env.PLAYWRIGHT_BROWSERS_PATH === undefined &&
    existsSync("/workspace/tools/ms-playwright")) {
  process.env.PLAYWRIGHT_BROWSERS_PATH = "/workspace/tools/ms-playwright";
}
const { browser: getBrowser } = await import("./browser.mjs");

const webRoot = fileURLToPath(new URL("..", import.meta.url));
const repoRoot = fileURLToPath(new URL("../..", import.meta.url));
const fixtureDir = join(webRoot, "tools", "fixtures", "project-preview");
const viteBin = join(webRoot, "node_modules", "vite", "bin", "vite.js");

// The synthetic credential and markers, shared with the Go fixture. They
// unlock nothing outside the scratch run and are never printed.
const OPERATOR_PASSWORD = "preview-acceptance-operator";
const loopback = process.argv.includes("--loopback");
const managed = loopback || process.argv.includes("--managed");
const DECK_HOST = loopback ? "127.0.0.1" : "deck.example.com";
const scheme = loopback ? "http" : "https";
const wsScheme = loopback ? "ws" : "wss";
const previewCookieName = loopback ? "deck_preview" : "__Host-deck_preview";
const PREVIEW_BASE_DOMAIN = "apps.example.net";
const PREVIEW_HOSTS = loopback ? ["127.0.0.2", "127.0.0.3"] : ["one.example.net", "two.example.net"];
const ENTRY_PATH = "/__deck_preview/enter";
const CSS_MARKER_BEFORE = "rgb(11, 22, 33)";
const CSS_MARKER_AFTER = "rgb(44, 55, 66)";
const ONE_PX_PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==",
  "base64",
);

// Headers that belong to one hop, never to the edge's forwarded request.
const HOP_BY_HOP = ["connection", "keep-alive", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade"];
// The edge listener's port, shared with the direct-request helper below.
const ports = { edge: 0, publicByHost: new Map() };
const hostHeader = (host) => {
  const port = ports.publicByHost.get(host) ?? ports.edge;
  return port === 443 ? host : `${host}:${port}`;
};

const runDir = mkdtempSync(join(repoRoot, ".project-preview-harness-"));
const children = [];
let browser = null;
let edgeServer = null;
let proxyServer = null;
let deckVite = null;
let failure = null;
const sockets = new Set();
const networkResponses = [];
const trackSocket = (socket) => {
  sockets.add(socket);
  socket.once("close", () => sockets.delete(socket));
  return socket;
};

let cleanupPromise;
process.once("SIGTERM", () => { void cleanup().finally(() => process.exit(143)); });
process.once("SIGINT", () => { void cleanup().finally(() => process.exit(130)); });
// Detached dev servers need cleanup even when an asynchronous callback fails.
process.on("unhandledRejection", (err) => {
  console.error("unhandled rejection:", err);
  void cleanup().finally(() => process.exit(1));
});

const issueTicket = (pg, projectID) => pg.evaluate(async (id) => {
  const m = document.cookie.match(/(?:^|;\s*)(?:__Host-)?noisedeck_csrf=([^;]+)/);
  const r = await fetch(`/api/projects/${encodeURIComponent(id)}/preview`, {
    method: "POST",
    headers: m === null ? {} : { "X-CSRF-Token": m[1] },
  });
  return { status: r.status, entry: await r.json() };
}, projectID);

const navHeaders = (origin) => ({
  origin,
  "sec-fetch-site": "same-origin",
  "sec-fetch-mode": "navigate",
  "sec-fetch-dest": "document",
  "upgrade-insecure-requests": "1",
  accept: "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
});

try {
  // ---------------------------------------------------------------- setup
  const go = spawnSync("go", ["version"], { encoding: "utf8" });
  if (go.status !== 0) {
    throw new Error(`the harness needs the Go toolchain to build its fixture; "go version" failed: ${go.stderr ?? go.error}`);
  }
  if (!existsSync(viteBin)) {
    throw new Error(`the harness needs the repo's Vite install at ${viteBin}; run pnpm install in web/`);
  }
  const [edgePort, proxyPort, deckVitePort, staticOnePort, staticTwoPort, backendPort, gatewayPort] =
    await Promise.all([freePort(), freePort(), freePort(), freePort(), freePort(), freePort(), freePort()]);
  ports.edge = edgePort;
  const loopbackPort = loopback ? (await freePortRange(1, [deckVitePort, backendPort]))[0] : 0;
  if (loopback) {
    for (const host of PREVIEW_HOSTS) {
      const reservation = net.createServer();
      await listen(reservation, loopbackPort, host);
      await new Promise(resolve => reservation.close(resolve));
    }
  }
  const DECK = `${scheme}://${DECK_HOST}:${loopback ? deckVitePort : edgePort}`;
  let ONE = `${scheme}://${PREVIEW_HOSTS[0]}:${loopback ? loopbackPort : edgePort}`;
  let TWO = `${scheme}://${PREVIEW_HOSTS[1]}:${loopback ? loopbackPort : edgePort}`;
  let onePort = staticOnePort;
  let twoPort = staticTwoPort;
  const portRange = managed ? await freePortRange(2, [edgePort, proxyPort, deckVitePort, backendPort, gatewayPort, loopbackPort]) : null;

  // The TLS edge's self-signed certificate lives only in the scratch run.
  const fixtureBin = join(runDir, "project-preview-fixture");
  const launcherBin = join(runDir, "noisedeck");
  const metadataSocket = join(runDir, "preview.sock");
  const certPath = join(runDir, "edge.crt");
  const keyPath = join(runDir, "edge.key");
  const build = spawnSync("go", ["build", "-o", fixtureBin, "./web/tools/fixtures/project-preview"],
    { cwd: repoRoot, encoding: "utf8" });
  if (build.status !== 0) {
    console.error("blocked: the backend fixture could not be built (the preview gateway package may still be landing):");
    console.error(build.stderr);
    throw new Error("backend fixture build failed");
  }
  if (managed) {
    const launcherBuild = spawnSync("go", ["build", "-o", launcherBin, "./cmd/noisedeck"],
      { cwd: repoRoot, encoding: "utf8" });
    assert.equal(launcherBuild.status, 0, `preview launcher build failed: ${launcherBuild.stderr}`);
  }
  // Two sample Vite apps, copied from the fixture's template and configured
  // per instance through the environment the template reads.
  const samples = [
    { name: "sample-one", host: PREVIEW_HOSTS[0], port: onePort, origin: ONE },
    { name: "sample-two", host: PREVIEW_HOSTS[1], port: twoPort, origin: TWO },
  ];
  for (const sample of samples) {
    const dir = join(runDir, sample.name);
    cpSync(join(fixtureDir, "sample"), dir, { recursive: true });
    mkdirSync(join(dir, "public"), { recursive: true });
    writeFileSync(join(dir, "public", "dot.png"), ONE_PX_PNG);
    if (managed) {
      mkdirSync(join(dir, ".deck"));
      writeFileSync(join(dir, ".deck", "preview.json"), JSON.stringify({
        version: 1, command: [process.execPath, viteBin, "--strictPort"],
      }));
      writeFileSync(join(dir, ".deck", "refusal-probe.json"), JSON.stringify({
        version: 1, command: [process.execPath, "-e", "process.stdout.write('launched')"],
      }));
    }
  }
  const launchOptions = (sample) => ({
    cwd: join(runDir, sample.name),
    env: { ...process.env, NOISEDECK_PROJECT_ID: sample.projectID, PREVIEW_SAMPLE_NAME: sample.name },
  });
  const launchArgs = ["preview", "run", "--socket", metadataSocket];
  const assertLaunchRefused = (sample, reason) => {
    const result = spawnSync(launcherBin, [...launchArgs, "--config", ".deck/refusal-probe.json"], {
      ...launchOptions(sample), encoding: "utf8", timeout: 5000,
    });
    assert.equal(result.error, undefined, "a refused launch must return promptly without starting the dev server");
    assert.notEqual(result.status, 0, "the launcher must refuse unavailable assignments or occupied ports");
    assert.match(result.stderr, reason);
  };
  const startSample = (sample) => {
    // Each Vite process group owns its helper processes and listening socket.
    sample.child = managed ? spawn(launcherBin, launchArgs, {
      ...launchOptions(sample), detached: true, stdio: ["ignore", "ignore", "pipe"],
    }) : spawn(process.execPath, [viteBin, "--strictPort"], {
      cwd: join(runDir, sample.name), detached: true, stdio: ["ignore", "ignore", "pipe"],
      env: {
        ...process.env,
        PREVIEW_SAMPLE_HOST: sample.host,
        PREVIEW_SAMPLE_PORT: String(sample.port),
        PREVIEW_SAMPLE_PUBLIC_PORT: new URL(sample.origin).port || "443",
        PREVIEW_SAMPLE_NAME: sample.name,
      },
    });
    children.push(sample.child);
    sample.child.stderr.on("data", (d) => process.stderr.write(`[${sample.name}] ${d}`));
  };
  if (!managed) samples.forEach(startSample);
  // The Deck UI, served by the real dev Vite server. The edge routes the
  // deck host's API and auth paths to the Go backend and everything else
  // here, so the browser sees one origin exactly as production does.
  const { createServer } = await import("vite");
  deckVite = await createServer({
    root: webRoot, cacheDir: join(runDir, "vite-cache"), configLoader: "runner",
    server: {
      port: deckVitePort, strictPort: true, allowedHosts: [DECK_HOST],
      // API preflights must reach Deck's origin checks instead of Vite's CORS responder.
      cors: false,
      ...(loopback ? {
        proxy: Object.fromEntries(["/api", "/auth"].map(path => [path, {
          target: `http://127.0.0.1:${backendPort}`, changeOrigin: false, ws: true,
        }])),
      } : {}),
    },
  });
  await deckVite.listen();

  await Promise.all([
    ...(!managed ? samples.map(s => waitForHTTP(`http://127.0.0.1:${s.port}/`, body => body.includes("data-sample"), `${s.name} Vite never served its page`)) : []),
    waitForHTTP(`http://127.0.0.1:${deckVitePort}/`, () => true, "the Deck UI dev server never served its page"),
  ]);

  // The real backend, through the real startup path, with the samples
  // registered as plain projects and the preview gateway configured.
  const planPath = join(runDir, "plan.json");
  writeFileSync(planPath, JSON.stringify({
    run: runDir,
    backend: `127.0.0.1:${backendPort}`,
    preview_bind: loopback ? "" : `127.0.0.1:${gatewayPort}`,
    loopback, loopback_port: loopbackPort,
    preview_metadata_socket: metadataSocket,
    deck_origin: DECK,
    ...(managed ? { base_domain: loopback ? "" : PREVIEW_BASE_DOMAIN, port_min: portRange[0], port_max: portRange[1] } : {}),
    samples: samples.map((s) => ({
      name: s.name, dir: join(runDir, s.name), origin: s.origin, upstream: `127.0.0.1:${s.port}`,
    })),
  }));
  const startFixture = async () => {
    const child = spawn(fixtureBin, ["serve", "-plan", planPath],
      { cwd: repoRoot, stdio: ["ignore", "pipe", "pipe"] });
    children.push(child);
    const state = { child, output: "", projects: [] };
    child.stdout.on("data", (d) => { state.output += d; });
    child.stderr.on("data", (d) => process.stderr.write(`[backend] ${d}`));
    await waitFor(() => {
      if (child.exitCode !== null) {
        throw new Error(`the backend fixture exited with code ${child.exitCode} during boot`);
      }
      return state.output.includes('"ready":true');
    }, () => `the backend fixture never reported ready. Output so far:\n${state.output || "(none)"}`, 45000);
    state.projects = JSON.parse(state.output.slice(0, state.output.indexOf("\n"))).projects;
    await waitForHTTP(`http://127.0.0.1:${backendPort}/livez`, () => true, "the backend never became live");
    return state;
  };
  let fixture = await startFixture();
  const [oneID, twoID] = fixture.projects;
  for (const [i, id] of fixture.projects.entries()) samples[i].projectID = id;
  if (managed) {
    assertLaunchRefused(samples[0], /enable|registered|disabled/i);
    for (const [i, id] of fixture.projects.entries()) {
      if (!loopback) PREVIEW_HOSTS[i] = `p-${createHash("sha256").update(id).digest("hex").slice(0, 20)}.${PREVIEW_BASE_DOMAIN}`;
      samples[i].host = PREVIEW_HOSTS[i];
      samples[i].origin = loopback ? `http://${PREVIEW_HOSTS[i]}:${loopbackPort}` : `https://${PREVIEW_HOSTS[i]}`;
    }
    [ONE, TWO] = samples.map(s => s.origin);
  }
  ports.publicByHost.set(DECK_HOST, loopback ? deckVitePort : edgePort);
  for (const host of PREVIEW_HOSTS) ports.publicByHost.set(host, loopback ? loopbackPort : managed ? 443 : edgePort);
  const connectRefused = [];
  if (!loopback) {
    const cert = spawnSync(fixtureBin, ["cert", certPath, keyPath, DECK_HOST, ...PREVIEW_HOSTS], { encoding: "utf8" });
    if (cert.status !== 0) {
      throw new Error(`fixture could not generate the TLS edge certificate: ${cert.stderr}`);
    }

    // ---------------------------------------------------------- TLS edge
    const edgeUpstream = (hostHeader, path) => {
      const hostname = (hostHeader ?? "").split(":")[0].toLowerCase();
      if (hostname === DECK_HOST) {
        if (path === "/api" || path.startsWith("/api/") ||
            path === "/auth" || path.startsWith("/auth/") ||
            path === "/livez" || path === "/readyz") return backendPort;
        return deckVitePort;
      }
      if (hostname === PREVIEW_HOSTS[0] || hostname === PREVIEW_HOSTS[1]) return gatewayPort;
      return null;
    };
    edgeServer = https.createServer({ key: readFileSync(keyPath), cert: readFileSync(certPath) }, (req, res) => {
      const port = edgeUpstream(req.headers.host, req.url ?? "/");
      if (port === null) {
        res.writeHead(421, { "content-type": "text/plain" });
        res.end("unknown host");
        return;
      }
      const headers = { ...req.headers };
      for (const h of HOP_BY_HOP) delete headers[h];
      const upstream = http.request(
        { host: "127.0.0.1", port, method: req.method, path: req.url, headers },
        (up) => {
          networkResponses.push({ host: req.headers.host, path: req.url, method: req.method, status: up.statusCode });
          const out = { ...up.headers };
          for (const h of HOP_BY_HOP) delete out[h];
          if ((req.headers.host ?? "").startsWith(DECK_HOST + ":")) out["referrer-policy"] = "no-referrer";
          res.writeHead(up.statusCode ?? 502, out);
          up.pipe(res);
        });
      upstream.on("error", (err) => {
        if (!res.headersSent) res.writeHead(502, { "content-type": "text/plain" });
        res.end(`edge upstream error: ${err.code ?? err.message}`);
      });
      req.pipe(upstream);
    });
    edgeServer.on("upgrade", (req, socket, head) => {
      const port = edgeUpstream(req.headers.host, req.url ?? "/");
      if (port === null) {
        socket.destroy();
        return;
      }
      const upstream = trackSocket(net.connect(port, "127.0.0.1", () => {
        // The upgrade rides the browser's own headers verbatim — Host,
        // Origin, Sec-Fetch-* and the WebSocket keys all reach the gateway
        // or the backend exactly as the browser sent them.
        let raw = `${req.method} ${req.url} HTTP/1.1\r\n`;
        for (let i = 0; i < req.rawHeaders.length; i += 2) {
          raw += `${req.rawHeaders[i]}: ${req.rawHeaders[i + 1]}\r\n`;
        }
        upstream.write(raw + "\r\n");
        if (head.length > 0) upstream.write(head);
        socket.pipe(upstream);
        upstream.pipe(socket);
      }));
      upstream.on("error", () => socket.destroy());
      socket.on("error", () => upstream.destroy());
    });
    edgeServer.on("connection", trackSocket);
    await listen(edgeServer, edgePort);

    // ---------------------------------------------------- CONNECT proxy
    const allowedConnect = new Set([DECK_HOST, ...PREVIEW_HOSTS].map((h) => `${h}:${ports.publicByHost.get(h)}`));
    proxyServer = http.createServer((req, res) => {
      res.writeHead(405, { "content-type": "text/plain" });
      res.end("this proxy only tunnels the harness's fixed synthetic hosts");
    });
    proxyServer.on("connect", (req, socket) => {
      if (!allowedConnect.has(req.url ?? "")) {
        connectRefused.push(req.url ?? "(none)");
        socket.destroy();
        return;
      }
      const upstream = trackSocket(net.connect(edgePort, "127.0.0.1", () => {
        socket.write("HTTP/1.1 200 Connection Established\r\n\r\n");
        socket.pipe(upstream);
        upstream.pipe(socket);
      }));
      upstream.on("error", () => socket.destroy());
      socket.on("error", () => upstream.destroy());
    });
    proxyServer.on("connection", trackSocket);
    await listen(proxyServer, proxyPort);
  }

  // -------------------------------------------------------- browser
  const { browser: connected, via } = await getBrowser();
  browser = connected;
  const contextOptions = loopback ? {} : { proxy: { server: `http://127.0.0.1:${proxyPort}` }, ignoreHTTPSErrors: true };
  const pageSockets = new Map();
  const pageConsole = new Map();
  const pageNavigations = new Map();
  const wire = (page) => {
    if (loopback) {
      // CDP retains server responses even when CORS hides them from fetch.
      void page.context().newCDPSession(page).then(async session => {
        const requests = new Map();
        session.on("Network.requestWillBeSent", ({ requestId, request }) => requests.set(requestId, request));
        session.on("Network.responseReceivedExtraInfo", ({ requestId, statusCode }) => {
          const request = requests.get(requestId);
          if (request === undefined) return;
          const url = new URL(request.url);
          networkResponses.push({ host: url.host, path: url.pathname, method: request.method, status: statusCode });
        });
        await session.send("Network.enable");
      });
    }
    pageSockets.set(page, []);
    pageConsole.set(page, []);
    pageNavigations.set(page, 0);
    page.on("websocket", (ws) => pageSockets.get(page).push(ws.url()));
    page.on("console", (m) => pageConsole.get(page).push(m.text()));
    page.on("framenavigated", (f) => {
      if (f === page.mainFrame()) pageNavigations.set(page, pageNavigations.get(page) + 1);
    });
  };

  // ================= 1. A fresh browser gets nothing =================
  const anon = await browser.newContext(contextOptions);
  anon.on("page", wire);
  const anonPage = await anon.newPage();
  const anonDoc = await anonPage.goto(`${ONE}/`, { waitUntil: "domcontentloaded" });
  assert.ok(anonDoc !== null && anonDoc.status() >= 400,
    `an unauthenticated browser must be refused, got status ${anonDoc?.status()}`);
  assert.ok(!(await anonPage.content()).includes("data-sample"),
    "the refusal page must not carry app content");
  const anonFetches = await anonPage.evaluate(async (origin) => {
    const out = {};
    for (const path of ["/src/main.js", "/src/style.css", "/dot.png", "/api/hello"]) {
      try {
        const r = await fetch(origin + path);
        out[path] = r.status;
      } catch {
        out[path] = 0;
      }
    }
    return out;
  }, ONE);
  for (const [path, status] of Object.entries(anonFetches)) {
    assert.ok(status >= 400, `unauthenticated fetch of ${path} must be refused, got ${status}`);
  }
  assert.equal((await wsProbe(anonPage, `${ONE}/ws`)).opened, false,
    "an unauthenticated WebSocket upgrade must not open");
  if (managed) {
    await anonPage.goto(`${DECK}/`, { waitUntil: "domcontentloaded" });
    for (const method of ["GET", "PUT", "DELETE"]) {
      const denied = await previewSettings(anonPage, oneID, method);
      assert.equal(denied.status, 401, `anonymous ${method} preview settings must be refused`);
    }
    assert.equal((await checkPreview(anonPage, oneID)).status, 401,
      "anonymous availability checks must be refused");
  }
  await anon.close();

  // ================= 2. The user signs in ======================
  const context = await browser.newContext(contextOptions);
  context.on("page", wire);
  const page = await context.newPage();
  await page.goto(`${DECK}/`, { waitUntil: "domcontentloaded" });
  await page.locator("[data-login-password]").waitFor({ timeout: 30000 });
  await page.locator("[data-login-password-input]").fill(OPERATOR_PASSWORD);
  await page.locator("[data-login-password] button[type='submit']").click();
  await page.waitForFunction(() => document.querySelector("[data-region]") !== null, null, { timeout: 30000 });
  await page.locator(`[data-project-menu="${oneID}"]`).waitFor({ timeout: 30000 });
  let assignments = [];
  const previewDialog = (deckPage) => deckPage.locator("[data-project-preview-dialog]");
  const openPreviewSettings = async (deckPage, projectID) => {
    await deckPage.locator(`[data-project-toggle="${projectID}"]`).hover();
    await deckPage.locator(`[data-project-menu="${projectID}"]`).click();
    await deckPage.locator("[data-project-menu-preview-settings]").click();
    await previewDialog(deckPage).waitFor();
  };
  const enableFromUI = async (deckPage, projectID, reachable = false) => {
    await openPreviewSettings(deckPage, projectID);
    const response = deckPage.waitForResponse(r => new URL(r.url()).pathname === `/api/projects/${projectID}/preview/settings` && r.request().method() === "PUT");
    await previewDialog(deckPage).getByRole("button", { name: "Enable preview", exact: true }).click();
    const enabled = await response;
    const settings = await enabled.json();
    await previewDialog(deckPage).getByRole("heading", { name: "Preview enabled", exact: true }).waitFor();
    await previewDialog(deckPage).locator("[data-preview-connectivity]").filter({ hasText: "Not checked yet" }).waitFor();
    await previewDialog(deckPage).getByRole("button", { name: "Check connectivity", exact: true }).click();
    await previewDialog(deckPage).locator("[data-preview-connectivity]").filter({ hasText: reachable ? "Accepting connections" : "Not responding" }).waitFor();
    await previewDialog(deckPage).getByRole("button", { name: "Done", exact: true }).click();
    await previewDialog(deckPage).waitFor({ state: "detached" });
    return { status: enabled.status(), settings };
  };
  const disableFromUI = async (projectID) => {
    await openPreviewSettings(page, projectID);
    await previewDialog(page).getByRole("button", { name: "Disable access…", exact: true }).click();
    await previewDialog(page).getByRole("heading", { name: "Disable preview access?", exact: true }).waitFor();
    assert.match(await previewDialog(page).innerText(), /URL and port stay reserved/);
    const response = page.waitForResponse(r => new URL(r.url()).pathname === `/api/projects/${projectID}/preview/settings` && r.request().method() === "DELETE");
    await previewDialog(page).getByRole("button", { name: "Disable access", exact: true }).click();
    const disabled = await response;
    await previewDialog(page).getByRole("heading", { name: "Preview access disabled", exact: true }).waitFor();
    await previewDialog(page).getByRole("button", { name: "Done", exact: true }).click();
    await previewDialog(page).waitFor({ state: "detached" });
    return { status: disabled.status(), settings: await disabled.json() };
  };
  if (managed) {
    console.log("  enabling two managed previews through the real settings UI");
    for (const id of [oneID, twoID]) {
      const initial = await previewSettings(page, id);
      assert.equal(initial.status, 200);
      assert.equal(initial.settings.configured, true);
      assert.equal(initial.settings.self_service, true);
      assert.equal(initial.settings.enabled, false);
      assert.equal(initial.settings.source, undefined, "reading settings must not create a registration");
    }
    for (const method of ["PUT", "DELETE"]) {
      assert.equal((await previewSettings(page, oneID, method, { csrf: false })).status, 403,
        `${method} preview settings must require CSRF authorization`);
    }
    assert.equal((await checkPreview(page, oneID, false)).status, 403,
      "availability checks must require CSRF authorization");
    assert.equal((await previewSettings(page, oneID, "PUT", { body: { upstream: "127.0.0.1:22" } })).status, 400,
      "the browser must not choose an arbitrary proxy upstream");
    const secondDeckPage = await context.newPage();
    await secondDeckPage.goto(`${DECK}/`, { waitUntil: "domcontentloaded" });
    await secondDeckPage.locator(`[data-project-menu="${twoID}"]`).waitFor();
    const [first, second] = loopback
      ? [await enableFromUI(page, oneID), await enableFromUI(secondDeckPage, twoID)]
      : await Promise.all([enableFromUI(page, oneID), enableFromUI(secondDeckPage, twoID)]);
    await secondDeckPage.close();
    const [duplicate, repeat] = await Promise.all([
      previewSettings(page, oneID, "PUT", { body: {} }), previewSettings(page, oneID, "PUT"),
    ]);
    assert.equal(first.status, 200);
    assert.equal(duplicate.status, 200);
    assert.equal(second.status, 200);
    assert.equal(repeat.status, 200);
    assert.equal(repeat.settings.port, duplicate.settings.port, "concurrent repeat enables must preserve the assignment");
    assert.equal(first.settings.origin, duplicate.settings.origin, "concurrent enables must retain one URL");
    assert.equal(first.settings.port, duplicate.settings.port, "concurrent enables must retain one port");
    assignments = [first.settings, second.settings];
    assert.notEqual(assignments[0].port, assignments[1].port, "two projects must receive distinct reserved ports");
    for (const [i, settings] of assignments.entries()) {
      assert.equal(settings.source, "managed");
      assert.equal(settings.enabled, true);
      assert.equal(settings.origin, samples[i].origin, "managed URLs must be stable and project-specific");
      assert.ok(settings.port >= portRange[0] && settings.port <= portRange[1], "allocated port must be inside the installation pool");
      assert.equal(settings.upstream, `127.0.0.1:${settings.port}`);
      assert.equal(settings.agent_instructions, undefined, "launch metadata must not be delivered as copied agent instructions");
      samples[i].port = settings.port;
      const unavailable = await checkPreview(page, fixture.projects[i]);
      assert.equal(unavailable.status, 200);
      assert.equal(unavailable.result.reachable, false, "registration must not imply a running dev server");
      assert.ok(Number.isFinite(Date.parse(unavailable.result.checked_at)), "availability must report when it was observed");
    }
    [onePort, twoPort] = samples.map(s => s.port);
    console.log(`  assigned ${ONE} and ${TWO}; launching sample Vite apps with the CLI`);
    samples.forEach(startSample);
    await Promise.all(samples.map(s => waitForHTTP(`http://127.0.0.1:${s.port}/`, body => body.includes("data-sample"), `${s.name} never started on its allocated port`)));
    for (const sample of samples) {
      const launched = await (await fetch(`http://127.0.0.1:${sample.port}/api/hello`)).json();
      assert.equal(launched.scheme, scheme, "the CLI must export DECK_PREVIEW_SCHEME");
      assert.equal(launched.originPort, Number(new URL(sample.origin).port || 443), "the CLI must export DECK_PREVIEW_ORIGIN_PORT");
      assert.equal(launched.port, sample.port, "Vite must listen on the assigned port");
    }
    for (const id of [oneID, twoID]) {
      assert.equal((await checkPreview(page, id)).result.reachable, true,
        "an explicit check must observe a listening dev server");
    }
    assertLaunchRefused(samples[0], /occupied|in use|listening/i);
    assert.equal((await fetch(`http://127.0.0.1:${onePort}/api/hello`)).status, 200,
      "a refused duplicate launch must leave the existing dev server running");
    // Before allocation the host was unknown. Recheck after registration
    // and startup so refusal proves authentication, not a missing route.
    const liveAnon = await browser.newContext(contextOptions);
    liveAnon.on("page", wire);
    const liveAnonPage = await liveAnon.newPage();
    for (const origin of [ONE, TWO]) {
      assert.equal((await liveAnonPage.goto(`${origin}/`)).status(), 403,
        "a registered and running managed preview must refuse anonymous navigation");
      const statuses = await liveAnonPage.evaluate(async () => Promise.all(
        ["/src/main.js", "/src/style.css", "/dot.png", "/api/hello"].map(async path => (await fetch(path)).status),
      ));
      assert.deepEqual(statuses, [403, 403, 403, 403], "running managed assets and APIs must require authentication");
      assert.equal((await wsProbe(liveAnonPage, `${origin}/ws`)).opened, false,
        "running managed WebSockets must require authentication");
    }
    await liveAnon.close();
  }

  // ================= 3. Open preview through the real UI ===========
  // The sidebar re-renders on its periodic project refresh, and a re-render
  // closes the row menu; a click racing that refresh lands on a detached
  // popover. Short timeouts plus bounded retries ride out the refresh
  // instead of pretending the race does not exist.
  const openPreview = async (projectID) => {
    let lastError = null;
    for (let attempt = 0; attempt < 4; attempt++) {
      const entryPosts = [];
      const onRequest = (r) => {
        if (r.method() === "POST" && new URL(r.url()).pathname === ENTRY_PATH) {
          entryPosts.push(r.url());
        }
      };
      context.on("request", onRequest);
      const popupPromise = context.waitForEvent("page", { timeout: 8000 });
      popupPromise.catch(() => { /* an abandoned attempt's timeout is not a failure */ });
      try {
        await page.locator(`[data-project-toggle="${projectID}"]`).hover({ timeout: 5000 });
        await page.locator(`[data-project-menu="${projectID}"]`).click({ timeout: 5000 });
        await page.locator("[data-project-menu-popover]").waitFor({ timeout: 5000 });
        await page.locator("[data-project-menu-preview]").click({ timeout: 5000 });
        const popup = await popupPromise;
        // The tab exists from window.open, but its ticket POST only leaves
        // after the entry API answers; keep listening until it does.
        await waitFor(() => entryPosts.length > 0,
          () => "the new tab never POSTed its ticket to the reserved entry path",
          15000);
        return { popup, entryPosts };
      } catch (err) {
        lastError = err;
        console.error(`[openPreview attempt ${attempt} failed] ${String(err.message).split("\n")[0]}`);
        try {
          const modalText = await page.locator('[data-region="modals"]').innerText({ timeout: 1000 });
          if (modalText.trim() !== "") console.error(`  open modal: ${modalText.trim().slice(0, 200).replace(/\n/g, " | ")}`);
        } catch { /* region absent */ }
        await page.keyboard.press("Escape").catch(() => {});
      } finally {
        context.off("request", onRequest);
      }
    }
    throw new Error(`Open preview never produced a tab for ${projectID}: ${lastError}`);
  };

  const postEntry = (host, ticket) => previewRequest({
    host, method: "POST", path: ENTRY_PATH,
    headers: {
      "content-type": "application/x-www-form-urlencoded",
      origin: DECK,
      "sec-fetch-site": "cross-site",
      "sec-fetch-dest": "document",
      "sec-fetch-mode": "navigate",
    },
    body: `ticket=${encodeURIComponent(ticket)}`,
  });

  let one = await openPreview(oneID);
  await one.popup.waitForURL((u) => u.origin === ONE, { timeout: 30000 });
  await one.popup.locator("[data-sample-name]").waitFor({ timeout: 30000 });
  let two = await openPreview(twoID);
  await two.popup.waitForURL((u) => u.origin === TWO, { timeout: 30000 });
  await two.popup.locator("[data-sample-name]").waitFor({ timeout: 30000 });
  if (managed) {
    console.log("  checking disable, re-enable and backend restart preserve assignments but revoke authority");
    const initialCookie = (await context.cookies(ONE)).find(c => c.name === previewCookieName);
    assert.ok(initialCookie, "managed preview must issue its own cookie");
    const unused = await issueTicket(page, oneID);
    assert.equal(unused.status, 200);
    await holdProbeSocket(one.popup);
    const disabled = await disableFromUI(oneID);
    assert.equal(disabled.status, 200);
    assert.equal(disabled.settings.enabled, false);
    assert.equal(disabled.settings.origin, assignments[0].origin);
    assert.equal(disabled.settings.port, assignments[0].port);
    assertLaunchRefused(samples[0], /enable|disabled/i);
    assert.equal(await socketClosed(one.popup), true, "disabling a preview must close its live WebSocket");
    assert.equal((await checkPreview(page, oneID)).status, 409, "disabled previews cannot be probed");
    assert.ok((await issueTicket(page, oneID)).status >= 400, "disabled previews cannot issue entry tickets");
    assert.ok((await postEntry(PREVIEW_HOSTS[0], unused.entry.ticket)).status >= 400,
      "disabling a preview must invalidate outstanding entry tickets");
    const disabledVisit = await previewRequest({ host: PREVIEW_HOSTS[0], path: "/", headers: {
      ...navHeaders(ONE), cookie: `${initialCookie.name}=${initialCookie.value}`,
    } });
    assert.ok(disabledVisit.status >= 400 && !disabledVisit.text.includes("data-sample"),
      "disabling a preview must refuse the existing browser cookie");
    assert.equal(await two.popup.evaluate(async () => (await fetch("/api/hello")).status), 200,
      "disabling one preview must leave its sibling usable");
    assert.equal((await fetch(`http://127.0.0.1:${onePort}/api/hello`)).status, 200,
      "disabling preview access must not stop the dev server");
    const enabledAgain = await enableFromUI(page, oneID, true);
    assert.equal(enabledAgain.status, 200);
    assert.equal(enabledAgain.settings.origin, assignments[0].origin);
    assert.equal(enabledAgain.settings.port, assignments[0].port);
    const staleCookie = await previewRequest({ host: PREVIEW_HOSTS[0], path: "/", headers: {
      ...navHeaders(ONE), cookie: `${initialCookie.name}=${initialCookie.value}`,
    } });
    assert.equal(staleCookie.status, 403, "re-enabling must not revive a revoked browser cookie");
    assert.ok((await postEntry(PREVIEW_HOSTS[0], unused.entry.ticket)).status >= 400,
      "re-enabling must not revive a revoked entry ticket");
    await one.popup.close();
    one = await openPreview(oneID);
    await one.popup.locator("[data-sample-name]").waitFor({ timeout: 30000 });
    const beforeRestartCookie = (await context.cookies(ONE)).find(c => c.name === previewCookieName);
    const beforeRestartTicket = await issueTicket(page, oneID);
    await holdProbeSocket(one.popup);
    const restartExit = await stopChild(fixture.child);
    assert.equal(restartExit.code, 0, "Deck must drain cleanly before restart");
    assert.ok(fixture.output.includes('"done":true'));
    assert.equal(await socketClosed(one.popup), true, "Deck shutdown must close managed preview sockets");
    assertLaunchRefused(samples[0], /socket|connect|unavailable|assignment/i);
    fixture = await startFixture();
    assert.deepEqual(fixture.projects, [oneID, twoID], "restart must reuse the same project registrations");
    const staleAfterRestart = await previewRequest({ host: PREVIEW_HOSTS[0], path: "/", headers: {
      ...navHeaders(ONE), cookie: `${beforeRestartCookie.name}=${beforeRestartCookie.value}`,
    } });
    assert.equal(staleAfterRestart.status, 403, "restart must not restore old preview cookies");
    assert.ok((await postEntry(PREVIEW_HOSTS[0], beforeRestartTicket.entry.ticket)).status >= 400,
      "restart must not restore outstanding entry tickets");
    for (const sample of samples) {
      // Reuse the same launch context: only Deck and its socket restarted.
      // A port collision proves the helper read the restored assignment;
      // accepting a metadata-unavailable error here would hide a stale socket.
      assertLaunchRefused(sample, /assigned preview port \d+ is unavailable/i);
      assert.equal((await fetch(`http://127.0.0.1:${sample.port}/api/hello`)).status, 200,
        "Deck restart must leave both dev servers running");
    }
    await page.goto(`${DECK}/`, { waitUntil: "domcontentloaded" });
    await page.waitForFunction(() => document.querySelector("[data-login-password], [data-region]") !== null);
    if (await page.locator("[data-login-password]").count() > 0) {
      await page.locator("[data-login-password-input]").fill(OPERATOR_PASSWORD);
      await page.locator("[data-login-password] button[type='submit']").click();
    }
    await page.locator(`[data-project-menu="${oneID}"]`).waitFor({ timeout: 30000 });
    for (const [i, id] of [oneID, twoID].entries()) {
      const restored = await previewSettings(page, id);
      assert.equal(restored.status, 200);
      assert.equal(restored.settings.enabled, true);
      assert.equal(restored.settings.origin, assignments[i].origin, "restart must preserve preview URLs");
      assert.equal(restored.settings.port, assignments[i].port, "restart must preserve reserved ports");
      assert.equal((await checkPreview(page, id)).result.reachable, true);
    }
    await one.popup.close();
    await two.popup.close();
    one = await openPreview(oneID);
    two = await openPreview(twoID);
    await one.popup.locator("[data-sample-name]").waitFor({ timeout: 30000 });
    await two.popup.locator("[data-sample-name]").waitFor({ timeout: 30000 });
  }
  const popupOne = one.popup;
  const popupTwo = two.popup;
  for (const { entryPosts, origin } of [{ entryPosts: one.entryPosts, origin: ONE }, { entryPosts: two.entryPosts, origin: TWO }]) {
    assert.deepEqual(entryPosts, [`${origin}${ENTRY_PATH}`],
      "the UI's new tab must POST its ticket to this preview's reserved entry path, exactly once");
  }

  await eventuallyEqual(() => popupOne.locator("[data-sample-name]").innerText(), "sample-one",
    "preview one did not serve its own app", 30000);
  await eventuallyEqual(() => popupTwo.locator("[data-sample-name]").innerText(), "sample-two",
    "preview two did not serve its own app while preview one was also open", 30000);

  // The preview cookie carries host-only, HttpOnly, short-lived authority.
  const previewCookies = async (origin) => (await context.cookies(origin)).filter((c) => c.name !== "sample_token");
  const oneCookies = await previewCookies(ONE);
  const twoCookies = await previewCookies(TWO);
  assert.equal(oneCookies.length, 1, `preview one should carry exactly the gateway cookie, saw ${oneCookies.map((c) => c.name).join(", ")}`);
  for (const c of [...oneCookies, ...twoCookies]) {
    assert.equal(c.name, previewCookieName);
    assert.equal(c.secure, !loopback, "Secure must match the preview transport");
    assert.ok(c.httpOnly, `${c.name} must be HttpOnly`);
    assert.equal(c.sameSite, "Lax", `${c.name} must stay Lax for top-level OAuth callbacks`);
    assert.equal(c.path, "/", `${c.name} must scope the whole app path space`);
    assert.ok(!c.domain.startsWith("."), `${c.name} must be host-only, got domain ${c.domain}`);
  }
  assert.equal(twoCookies.length, 1, "preview two must carry only its gateway cookie");
  const deckCookies = await context.cookies(DECK);
  assert.ok(deckCookies.some(c => c.name.includes("noisedeck")), "Deck must have real authentication cookies to test isolation");
  for (const cookies of [oneCookies, twoCookies]) {
    assert.ok(cookies.every(c => !deckCookies.some(d => d.name === c.name)), "Deck cookies must be absent from preview tabs");
  }
  await popupOne.evaluate(() => { document.cookie = "preview_one=one; Path=/; SameSite=Lax"; });
  await popupTwo.evaluate(() => { document.cookie = "preview_two=two; Path=/; SameSite=Lax"; });
  const visibleOne = await popupOne.evaluate(() => document.cookie);
  const visibleTwo = await popupTwo.evaluate(() => document.cookie);
  assert.match(visibleOne, /(?:^|;\s*)preview_one=one(?:;|$)/);
  assert.match(visibleTwo, /(?:^|;\s*)preview_two=two(?:;|$)/);
  assert.ok(!visibleOne.includes("preview_two") && !visibleTwo.includes("preview_one"), "preview tabs must not read each other's cookies");
  for (const visible of [visibleOne, visibleTwo]) {
    assert.ok(!visible.includes("noisedeck") && !visible.includes(previewCookieName), "Deck and HttpOnly gateway cookies must not be script-readable in a preview");
  }
  for (const host of PREVIEW_HOSTS) {
    assert.equal((await previewRequest({ host, path: "/", headers: navHeaders(`${scheme}://${hostHeader(host)}`) })).status, 403,
      "a direct GET without a cookie must be refused by the running gateway");
  }
  console.log("  both tabs rendered; cookie isolation, gateway attributes and anonymous GET refusal verified");
  const oneCookie = oneCookies[0];

  // ================= 4. The app works through the gateway ==========
  assert.equal(await popupOne.locator("[data-sample-image]").evaluate((i) => i.naturalWidth), 1,
    "the image asset did not load through the gateway");
  assert.equal(await borderColor(popupOne), CSS_MARKER_BEFORE,
    "the stylesheet did not apply through the gateway");
  await waitFor(() => popupOne.evaluate(() => window.__sample.apiSeen === 1),
    () => "the app's API POST never round-tripped through the gateway");
  await waitFor(() => popupOne.evaluate(() => window.__sample.streamEvents >= 2),
    () => "the SSE stream delivered fewer than two events through the gateway");
  await waitFor(() => popupOne.evaluate(() => window.__sample.wsEcho === true),
    () => "the app's WebSocket echo never answered through the gateway");

  // Nested SPA route: client-side from the page, and a direct load of the
  // deep URL (the dev server's own SPA fallback, through the gateway).
  await popupOne.locator("[data-sample-link]").click();
  await popupOne.waitForURL(`${ONE}/nested/deep`);
  assert.equal(await popupOne.locator("[data-sample-nested]").innerText(), "nested route rendered",
    "the client-side router did not render the nested route");
  await popupOne.goto(`${ONE}/nested/deep`, { waitUntil: "domcontentloaded" });
  await popupOne.locator("[data-sample-name]").waitFor();
  assert.equal(await popupOne.locator("[data-sample-nested]").innerText(), "nested route rendered",
    "a directly loaded nested route did not render");

  // App cookies round-trip; Deck cookies and identity headers do not pass.
  const visits = await popupOne.evaluate(async () => {
    const first = await (await fetch("/api/visit")).json();
    const second = await (await fetch("/api/visit")).json();
    return [first.visits, second.visits];
  });
  assert.deepEqual(visits, [1, 2], "the app's own host-only cookie did not round-trip through the gateway");
  const whoami = await popupOne.evaluate(async () => await (await fetch("/api/whoami")).json());
  assert.ok(!whoami.cookie.includes("noisedeck"),
    `Deck cookie names reached the app server: ${whoami.cookie}`);
  assert.ok(!whoami.cookie.includes(oneCookie.value),
    "the gateway's own session cookie value reached the app server");
  assert.equal(whoami.xForwardedUser, null, "a trusted identity header reached the app server");
  assert.equal(whoami.host, hostHeader(PREVIEW_HOSTS[0]),
    "the gateway did not preserve the browser's Host to the app server");

  // An OAuth-style top-level GET from another site retains the app's
  // Lax cookie while the gateway cookie remains hidden from its server.
  const callbackPage = await context.newPage();
  await callbackPage.goto(`${DECK}/`, { waitUntil: "domcontentloaded" });
  console.log("  checking OAuth callback cookie continuity");
  const callbackResponse = callbackPage.waitForResponse(r => r.url() === `${ONE}/api/whoami?code=synthetic`);
  await callbackPage.evaluate(url => { location.href = url; }, `${ONE}/api/whoami?code=synthetic`);
  const callback = await callbackResponse;
  assert.equal(callback.status(), 200, "top-level OAuth GET callback must retain preview access");
  assert.ok((await callback.json()).cookie.includes("sample_token="), "OAuth callback lost the app's Lax cookie");
  await callbackPage.close();

  console.log("  checking service-worker refusal");
  const swStart = networkResponses.length;
  const sw = await popupOne.evaluate(async () => {
    try { await Promise.race([navigator.serviceWorker.register("/sw.js"), new Promise((_, reject) => setTimeout(() => reject(new Error("registration timeout")), 10000))]); return { registered: true }; }
    catch (err) { return { registered: false, error: String(err) }; }
  });
  assert.equal(sw.registered, false, "preview service workers must not survive authorization revocation");
  if (loopback) {
    // Service-worker script loads are outside the page's CDP network events.
    assert.match(sw.error, /\b403\b/, "the browser must observe a forbidden service-worker script response");
  } else {
    assert.ok(networkResponses.slice(swStart).some(r => r.host === hostHeader(PREVIEW_HOSTS[0]) && r.path === "/sw.js" && r.status === 403), "service-worker script requests must be refused at the gateway");
  }

  // ================= 5. A real HMR edit, no reload =================
  const navigationsBeforeEdit = pageNavigations.get(popupOne);
  const oneCss = join(runDir, "sample-one", "src", "style.css");
  writeFileSync(oneCss, readFileSync(oneCss, "utf8").replace(CSS_MARKER_BEFORE, CSS_MARKER_AFTER));
  await waitFor(async () => (await borderColor(popupOne)) === CSS_MARKER_AFTER,
    async () => `the CSS edit never arrived hot: border-color is ${await borderColor(popupOne)}`);
  assert.equal(pageNavigations.get(popupOne), navigationsBeforeEdit,
    "the preview reloaded instead of hot-updating");

  console.log(`  CSS updated over ${wsScheme} HMR without a page reload`);

  // ================= 6. Routing of every socket ====================
  for (const [pg, urls] of pageSockets) {
    for (const url of urls) {
      const u = new URL(url);
      assert.ok(u.protocol === `${wsScheme}:` && [DECK_HOST, ...PREVIEW_HOSTS].includes(u.hostname) && Number(u.port || 443) === ports.publicByHost.get(u.hostname),
        `a socket bypassed the synthetic origins: ${u.origin}${u.pathname}`);
    }
  }
  const oneSockets = pageSockets.get(popupOne);
  const oneSocketOrigin = ONE.replace(`${scheme}:`, `${wsScheme}:`) + "/";
  assert.ok(oneSockets.some((u) => u.startsWith(oneSocketOrigin)),
    `preview one opened no HMR socket through its gateway origin; sockets: ${oneSockets.map(u => u.split("?")[0]).join(", ") || "(none)"}`);
  assert.deepEqual(oneSockets.filter((u) => !u.startsWith(oneSocketOrigin)), [],
    "preview one opened a socket outside its own gateway origin");
  const consoleText = (pg) => pageConsole.get(pg).join("\n");
  assert.ok(!consoleText(popupOne).includes("Direct websocket connection fallback"),
    "Vite's client fell back to a direct dev-server socket");

  // ============ 7. A hostile preview gets no Deck authority ========
  console.log("  checking hostile browser access");
  const deckReadStart = networkResponses.length;
  const deckRead = await popupOne.evaluate(async (deck) => {
    try {
      const r = await fetch(`${deck}/api/projects`, { credentials: "include" });
      return { ok: r.ok, status: r.status, text: await r.text() };
    } catch {
      return { ok: false, status: 0, text: "" };
    }
  }, DECK);
  await waitFor(() => networkResponses.slice(deckReadStart).some(r => r.host === hostHeader(DECK_HOST) && r.path === "/api/projects" && r.method === "GET" && r.status === 403), () => "hostile read must be refused server-side");
  assert.ok(!deckRead.ok, `a credentialed cross-origin Deck API read succeeded (status ${deckRead.status})`);
  assert.ok(!deckRead.text.includes("sample-"), "Deck's project list leaked to the preview page");

  const deckWriteStart = networkResponses.length;
  const deckWrite = await popupOne.evaluate(async (deck) => {
    try {
      const r = await fetch(`${deck}/api/projects`, {
        method: "POST",
        credentials: "include",
        headers: { "content-type": "application/json", "X-CSRF-Token": "forged" },
        body: JSON.stringify({ name: "pwned", path: "/tmp", plain: true }),
      });
      return { ok: r.ok, status: r.status };
    } catch {
      return { ok: false, status: 0 };
    }
  }, DECK);
  await waitFor(() => networkResponses.slice(deckWriteStart).some(r => r.host === hostHeader(DECK_HOST) && r.path === "/api/projects" && r.method === "OPTIONS" && r.status === 403), () => "hostile write must be refused server-side");
  assert.ok(!deckWrite.ok, `a forged cross-origin Deck API write succeeded (status ${deckWrite.status})`);

  if (managed) {
    for (const [method, suffix] of [["GET", "/settings"], ["PUT", "/settings"], ["DELETE", "/settings"], ["POST", "/check"]]) {
      const start = networkResponses.length;
      const endpoint = `/api/projects/${oneID}/preview${suffix}`;
      const attack = await popupOne.evaluate(async ({ deck, endpoint, method }) => {
        try {
          const r = await fetch(deck + endpoint, { method, credentials: "include" });
          return { ok: r.ok, status: r.status };
        } catch { return { ok: false, status: 0 }; }
      }, { deck: DECK, endpoint, method });
      assert.equal(attack.ok, false, `preview code must not ${method} its own management API`);
      await waitFor(() => networkResponses.slice(start).some(r => r.host === hostHeader(DECK_HOST) && r.path === endpoint && r.status === 403),
        () => `the backend must refuse hostile management ${method}, not depend only on browser CORS`);
    }
    assert.equal((await previewSettings(page, oneID)).settings.enabled, true,
      "hostile management calls must not alter the registration");
  }

  const formResponse = popupOne.waitForResponse((r) => r.url().startsWith(DECK) && r.request().method() === "POST");
  await popupOne.evaluate((deck) => {
    const form = document.createElement("form");
    form.method = "POST";
    form.action = `${deck}/api/projects`;
    const field = document.createElement("input");
    field.name = "name";
    field.value = "form-pwned";
    form.append(field);
    document.body.append(form);
    form.submit();
  }, DECK);
  const refusedForm = await formResponse;
  assert.equal(refusedForm.status(), 403,
    "a top-level form POST from the preview must be refused by the Deck backend");
  assert.ok(!(await refusedForm.text()).includes("sample-"),
    "the refused form navigation still leaked project data");

  // Back to the preview origin first: a committed 403 document would make
  // the next navigation same-origin for Deck, which the guard rightly
  // allows. The cross-site attempt must come from the preview page.
  await popupOne.goto(`${ONE}/`, { waitUntil: "domcontentloaded" });
  await popupOne.locator("[data-sample-name]").waitFor();
  // Top-level GET navigation: the browser may or may not commit the refused
  // cross-site document (the guard's same-origin resource policy can make
  // the load itself fail), so the server's answer is read from the response
  // event — that is where the refusal is enforced.
  const navResponsePromise = popupOne.waitForResponse(
    (r) => r.url() === `${DECK}/api/projects` && r.request().method() === "GET", { timeout: 15000 });
  // A page-initiated navigation, not page.goto: goto is browser-initiated
  // and carries Sec-Fetch-Site: none, which the guard rightly admits —
  // the user's address bar is not a hostile preview.
  await popupOne.evaluate((deck) => {
    location.href = `${deck}/api/projects`;
  }, DECK);
  const refusedNav = await navResponsePromise;
  assert.equal(refusedNav.status(), 403,
    "top-level navigation from a preview to Deck's API must be refused server-side");
  assert.ok(!(await refusedNav.text()).includes("sample-"),
    "the refused navigation still leaked project data");

  await popupOne.goto(`${ONE}/`, { waitUntil: "domcontentloaded" });
  await popupOne.locator("[data-sample-name]").waitFor();

  assert.equal((await wsProbe(popupOne, `${wsScheme}://${hostHeader(DECK_HOST)}/api/sessions/none/terminal`)).opened, false,
    "a preview page must not open a Deck terminal WebSocket");

  const siblingStart = networkResponses.length;
  const siblingFetch = await popupOne.evaluate(async (two) => {
    try {
      const r = await fetch(`${two}/api/hello`, { credentials: "include" });
      return { ok: r.ok, status: r.status };
    } catch {
      return { ok: false, status: 0 };
    }
  }, TWO);
  await waitFor(() => networkResponses.slice(siblingStart).some(r => r.host === hostHeader(PREVIEW_HOSTS[1]) && r.path === "/api/hello" && r.method === "GET" && r.status === 403), () => "hostile sibling request must be refused server-side");
  assert.ok(!siblingFetch.ok && siblingFetch.status !== 200,
    `a credentialed fetch of the sibling preview was not refused (status ${siblingFetch.status})`);
  assert.equal((await wsProbe(popupOne, `${TWO}/ws`)).opened, false,
    "a WebSocket from one preview to its sibling must not open");

  // Back to the app for the remaining phases.
  await popupOne.goto(`${ONE}/`, { waitUntil: "domcontentloaded" });
  await popupOne.locator("[data-sample-name]").waitFor();

  // ================= 8. Ticket discipline ==========================
  const ticketA = await issueTicket(page, oneID);
  assert.equal(ticketA.status, 200, "the authenticated preview entry API refused the user");
  assert.equal(ticketA.entry.url, `${ONE}${ENTRY_PATH}`,
    `the entry URL must be preview one's reserved entry path, got ${ticketA.entry.url}`);
  assert.ok(typeof ticketA.entry.ticket === "string" && ticketA.entry.ticket.length >= 16,
    "the entry ticket is missing or implausibly short");

  const consumed = await postEntry(PREVIEW_HOSTS[0], ticketA.entry.ticket);
  assert.ok(consumed.status >= 300 && consumed.status < 400,
    `a fresh ticket must be exchanged for a redirect, got ${consumed.status}`);
  assert.equal(consumed.headers.location, "/", "the entry redirect must land on the app root");
  const setCookie = consumed.headers["set-cookie"] ?? [];
  assert.ok(setCookie.length > 0, "the entry response must set the preview session cookie");
  for (const line of setCookie) {
    assert.ok(line.startsWith(`${previewCookieName}=`));
    assert.equal(/;\s*Secure(?:;|$)/i.test(line), !loopback);
    assert.ok(line.includes("HttpOnly") && line.includes("Path=/") && line.includes("SameSite=Lax"),
      "the preview cookie response is missing required attributes");
    assert.ok(!/Domain=/i.test(line), "the preview cookie must stay host-only (no Domain attribute)");
  }

  const replay = await postEntry(PREVIEW_HOSTS[0], ticketA.entry.ticket);
  assert.ok(replay.status >= 400, `a consumed ticket must not be replayable, got ${replay.status}`);
  assert.deepEqual(replay.headers["set-cookie"] ?? [], [], "a refused entry must set no cookie");

  const ticketB = await issueTicket(page, oneID);
  const crossHost = await postEntry(PREVIEW_HOSTS[1], ticketB.entry.ticket);
  assert.ok(crossHost.status >= 400, "a ticket for one preview must not enter a different preview host");

  const ticketC = await issueTicket(page, oneID);
  assert.equal(ticketC.status, 200);
  const ticketCIssuedAt = Date.now();

  // Stop one project while the other remains live.
  const sampleExit = await stopChild(samples[0].child);
  assert.notEqual(sampleExit.signal, "SIGKILL", "the launched dev server must stop within its SIGTERM grace period");
  await waitFor(async () => {
    const listener = net.createServer();
    try { await listen(listener, onePort); return true; }
    catch { return false; }
    finally { await new Promise(resolve => listener.close(resolve)); }
  }, () => "the stopped dev server did not release its assigned port");

  // The gateway demands positive same-origin evidence even from an
  // authorized cookie, so the harness's direct requests carry a real
  // browser navigation's headers.
  const deadResponse = await previewRequest({
    host: PREVIEW_HOSTS[0], path: "/",
    headers: { ...navHeaders(ONE), cookie: `${oneCookie.name}=${oneCookie.value}` },
  });
  assert.equal(deadResponse.status, 502,
    `a stopped dev server must surface an unavailable response, got ${deadResponse.status}`);
  assert.ok(!deadResponse.text.includes("data-sample"), "the unavailable response carried app content");
  const deadNav = await popupOne.goto(`${ONE}/`, { waitUntil: "domcontentloaded" });
  assert.equal(deadNav?.status(), 502, "the browser must see the unavailable response, not app content");
  const twoAlive = await previewRequest({ host: PREVIEW_HOSTS[1], path: "/api/hello", headers: { ...navHeaders(TWO), cookie: `${twoCookies[0].name}=${twoCookies[0].value}` } });
  assert.ok(twoAlive.status === 200 && twoAlive.text.includes("sample-two"),
    "the sibling preview must keep working while the other dev server is down");

  if (managed) {
    assert.equal((await checkPreview(page, oneID)).result.reachable, false,
      "availability must observe the stopped dev server");
    assert.equal((await checkPreview(page, twoID)).result.reachable, true,
      "availability must still observe the running sibling");
  }

  // ================= 10. Ticket expiry =============================
  console.log("  waiting for the single-use entry ticket to expire");
  const waitMs = Math.max(0, 61000 - (Date.now() - ticketCIssuedAt));
  await new Promise((r) => setTimeout(r, waitMs));
  const expired = await postEntry(PREVIEW_HOSTS[0], ticketC.entry.ticket);
  assert.ok(expired.status >= 400, `a ticket older than its 60s lifetime must be refused, got ${expired.status}`);
  assert.deepEqual(expired.headers["set-cookie"] ?? [], [], "an expired entry must set no cookie");

  // ================= 11. Logout revokes previews ===================
  await popupTwo.evaluate(() => {
    const ws = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/ws`);
    window.__probeWsEchoed = new Promise((resolve) => {
      ws.onopen = () => { ws.send("logout-probe"); };
      ws.onmessage = () => resolve(true);
    });
    window.__probeWsClosed = new Promise((resolve) => { ws.onclose = () => resolve(true); });
  });
  console.log("  checking logout revokes the live WebSocket");
  assert.equal(await popupTwo.evaluate(() => Promise.race([window.__probeWsEchoed, new Promise(resolve => setTimeout(() => resolve(false), 10000))])), true,
    "the logout probe WebSocket must echo before logout");

  await page.locator("[data-live-more]").click();
  await page.locator("[data-live-logout]").waitFor();
  await page.locator("[data-live-logout]").click();
  await page.locator("[data-login-password]").waitFor({ timeout: 15000 });

  assert.equal(await Promise.race([
    popupTwo.evaluate(() => window.__probeWsClosed),
    new Promise((r) => setTimeout(() => r(false), 10000)),
  ]), true, "logout must close the preview's live WebSocket");

  const revokedNav = await popupTwo.goto(`${TWO}/`, { waitUntil: "domcontentloaded" });
  assert.ok(revokedNav !== null && revokedNav.status() >= 400,
    `an existing preview must be refused after logout, got ${revokedNav?.status()}`);
  assert.ok(!(await popupTwo.content()).includes("data-sample"), "the post-logout response carried app content");
  const twoCookie = twoCookies[0];
  const revived = await previewRequest({ host: PREVIEW_HOSTS[1], path: "/", headers: { cookie: `${twoCookie.name}=${twoCookie.value}` } });
  assert.ok(revived.status >= 400 && !revived.text.includes("data-sample"),
    "the browser-held preview cookie must be worthless after logout");

  // ========= 12. Shutting Deck down kills no dev server ============
  const fixtureExit = await stopChild(fixture.child);
  assert.ok(fixtureExit.code === 0, `the backend fixture must exit cleanly on SIGTERM, got ${JSON.stringify(fixtureExit)}`);
  assert.ok(fixture.output.includes('"done":true'), "the backend fixture did not complete its drain");
  const survivor = await fetch(`http://127.0.0.1:${twoPort}/api/hello`);
  assert.ok(survivor.ok && (await survivor.text()).includes("sample-two"),
    "stopping Deck must not kill the project dev servers");
  if (managed) assertLaunchRefused(samples[0], /socket|connect|unavailable|assignment/i);

  console.log(`project-app-preview probe PASSED (mode: ${loopback ? "loopback" : managed ? "managed" : "static"}; browser: ${via})`);
  console.log(`  two sample Vite apps proxied through ${ONE} and ${TWO} behind the real gateway`);
  console.log(`  entry tickets single-use, host-bound and expiring; cookies host-only ${loopback ? "non-Secure" : "Secure"} HttpOnly SameSite=Lax`);
  console.log(`  hostile preview obtained no Deck API, form, navigation or terminal authority`);
  console.log(`  HMR edit applied without reload; every socket stayed on its gateway origin`);
  console.log(`  stopped dev server answered 502; logout revoked live previews and closed their sockets`);
  console.log(`  Deck shutdown drained cleanly and the remaining dev server survived`);
  if (connectRefused.length > 0) {
    console.log(`  the proxy refused ${connectRefused.length} off-plan CONNECT attempt(s): ${[...new Set(connectRefused)].join(", ")}`);
  }
  await browser.close();
} catch (err) {
  failure = err;
} finally {
  await cleanup();
}
if (failure) {
  console.error(failure.message ?? failure);
  process.exit(1);
}

function cleanup() {
  cleanupPromise ??= cleanupResources();
  return cleanupPromise;
}

async function cleanupResources() {
  try {
    if (browser !== null) await browser.close();
  } catch { /* already gone */ }
  await Promise.all(children.map(child => stopChild(child, 1000)));
  await deckVite?.close();
  for (const socket of sockets) socket.destroy();
  for (const server of [edgeServer, proxyServer]) {
    if (server === null) continue;
    server.closeAllConnections?.();
    await new Promise(resolve => server.close(resolve));
  }
  rmSync(runDir, { recursive: true, force: true });
}

async function listen(server, port, host = "127.0.0.1") {
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, host, resolve);
  });
}

function stopChild(child, graceMs = 15000) {
  if (child.exitCode !== null || child.signalCode !== null) {
    return Promise.resolve({ code: child.exitCode, signal: child.signalCode });
  }
  return new Promise((resolve) => {
    let settled = false;
    const finish = (code, signal) => {
      if (!settled) {
        settled = true;
        resolve({ code, signal });
      }
    };
    child.once("exit", finish);
    signalGroup(child, "SIGTERM");
    setTimeout(() => {
      if (!settled) signalGroup(child, "SIGKILL");
    }, graceMs).unref();
  });
}

// signalGroup signals the child's whole process group when the child leads
// one (the Vite servers), and the child alone otherwise (the fixture). The
// negative-PID kill throws ESRCH once the group is gone, which is the
// outcome the caller wanted anyway.
function signalGroup(child, signal) {
  try {
    process.kill(-child.pid, signal);
  } catch {
    child.kill(signal);
  }
}

// Hosted requests traverse the synthetic TLS edge; loopback requests go
// straight to the assigned address, with no DNS or proxy.
function previewRequest({ host, method = "GET", path, headers = {}, body = null }) {
  return new Promise((resolve, reject) => {
    const req = (loopback ? http : https).request({
      host: loopback ? host : "127.0.0.1",
      port: loopback ? ports.publicByHost.get(host) : ports.edge,
      servername: host,
      method,
      path,
      headers: { host: hostHeader(host), ...headers },
      rejectUnauthorized: false,
    }, (res) => {
      const chunks = [];
      res.on("data", (c) => chunks.push(c));
      res.on("end", () => resolve({
        status: res.statusCode ?? 0,
        headers: res.headers,
        text: Buffer.concat(chunks).toString("utf8"),
      }));
    });
    req.on("error", reject);
    req.setTimeout(20000, () => req.destroy(new Error(`timeout requesting ${scheme}://${hostHeader(host)}${path}`)));
    if (body !== null) req.write(body);
    req.end();
  });
}

function borderColor(page) {
  return page.evaluate(() => getComputedStyle(document.body).borderColor);
}

function previewSettings(page, projectID, method = "GET", { csrf = true, body } = {}) {
  return page.evaluate(async ({ projectID, method, csrf, body }) => {
    const token = document.cookie.match(/(?:^|;\s*)(?:__Host-)?noisedeck_csrf=([^;]+)/);
    const headers = csrf && token !== null ? { "X-CSRF-Token": token[1] } : {};
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const response = await fetch(`/api/projects/${encodeURIComponent(projectID)}/preview/settings`, {
      method, headers, ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    return { status: response.status, settings: response.headers.get("content-type")?.includes("application/json") ? await response.json() : {} };
  }, { projectID, method, csrf, body });
}

function checkPreview(page, projectID, csrf = true) {
  return page.evaluate(async ({ projectID, csrf }) => {
    const token = document.cookie.match(/(?:^|;\s*)(?:__Host-)?noisedeck_csrf=([^;]+)/);
    const response = await fetch(`/api/projects/${encodeURIComponent(projectID)}/preview/check`, {
      method: "POST", headers: csrf && token !== null ? { "X-CSRF-Token": token[1] } : {},
    });
    return { status: response.status, result: response.headers.get("content-type")?.includes("application/json") ? await response.json() : {} };
  }, { projectID, csrf });
}

async function holdProbeSocket(page) {
  await page.evaluate(() => {
    const socket = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/ws`);
    window.__managedProbeEcho = new Promise(resolve => {
      socket.onopen = () => socket.send("managed-preview-lifecycle");
      socket.onmessage = () => resolve(true);
    });
    window.__managedProbeClose = new Promise(resolve => { socket.onclose = () => resolve(true); });
  });
  assert.equal(await page.evaluate(() => Promise.race([
    window.__managedProbeEcho, new Promise(resolve => setTimeout(() => resolve(false), 10000)),
  ])), true, "the lifecycle probe WebSocket must echo before revocation");
}

function socketClosed(page) {
  return page.evaluate(() => Promise.race([
    window.__managedProbeClose, new Promise(resolve => setTimeout(() => resolve(false), 10000)),
  ]));
}

async function wsProbe(page, url) {
  return page.evaluate(async ({ url }) => {
    return await new Promise((resolve) => {
      const ws = new WebSocket(url);
      const out = { opened: false };
      ws.onopen = () => {
        out.opened = true;
        ws.close();
        resolve(out);
      };
      ws.onclose = () => resolve(out);
      setTimeout(() => resolve(out), 5000);
    });
  }, { url });
}

async function waitFor(condition, describe, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if (await condition()) return;
    if (Date.now() > deadline) throw new Error(await describe());
    await new Promise((r) => setTimeout(r, 200));
  }
}

async function waitForHTTP(url, accept = () => true, message, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      const res = await fetch(url);
      if (res.ok && accept(await res.text())) return;
    } catch { /* not listening yet */ }
    if (Date.now() > deadline) throw new Error(message ?? `${url} never became ready`);
    await new Promise((r) => setTimeout(r, 250));
  }
}

function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.on("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const { port } = srv.address();
      srv.close(() => resolve(port));
    });
  });
}

// Hold the whole candidate range while checking it so the test does not
// mistake two independent ephemeral allocations for a contiguous pool.
async function freePortRange(count, excluded) {
  for (let attempt = 0; attempt < 30; attempt++) {
    const first = await freePort();
    if (first <= 18000 || first + count - 1 > 65535 || excluded.some(p => p >= first && p < first + count)) continue;
    const reservations = [];
    try {
      for (let offset = 0; offset < count; offset++) {
        const server = net.createServer();
        reservations.push(server);
        await listen(server, first + offset);
      }
      return [first, first + count - 1];
    } catch { /* try another range if a neighboring port is already bound */ }
    finally {
      await Promise.all(reservations.map(server => new Promise(resolve => server.close(resolve))));
    }
  }
  throw new Error("could not find an unused contiguous managed-preview port pool");
}
