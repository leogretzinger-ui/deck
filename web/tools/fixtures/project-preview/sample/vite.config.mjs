// The sample app's dev-server configuration. It has no imports beyond
// node:crypto so it keeps working from the harness scratch copy, where no
// node_modules tree exists above it — the harness runs the repo's own Vite
// binary with this directory as root.
//
// The HMR client uses the public gateway host and port explicitly. Without
// clientPort, Vite retries its private listener when a gateway connection
// fails, including the deliberate authorization revocations in this probe.
//
// The plugin adds the sample's own /api endpoints (JSON, cookies, SSE) and
// a /ws WebSocket echo. Vite's HMR upgrade listener only claims requests
// carrying the vite-hmr subprotocol, so a separate upgrade listener for
// /ws coexists with it on the same server.
import { createHash, randomBytes } from "node:crypto";

const assignment = process.env.DECK_PREVIEW_ORIGIN === undefined ? null : new URL(process.env.DECK_PREVIEW_ORIGIN);
const host = assignment?.hostname ?? process.env.PREVIEW_SAMPLE_HOST;
const port = Number(assignment === null ? process.env.PREVIEW_SAMPLE_PORT : process.env.DECK_PREVIEW_PORT);
const publicPort = Number(assignment === null ? process.env.PREVIEW_SAMPLE_PUBLIC_PORT : process.env.DECK_PREVIEW_ORIGIN_PORT);
const scheme = assignment === null ? "https" : process.env.DECK_PREVIEW_SCHEME;
if (assignment !== null && (scheme + ":" !== assignment.protocol || publicPort !== Number(assignment.port || (scheme === "https" ? 443 : 80)))) {
  throw new Error("the launcher must export the assigned preview scheme and origin port");
}
const name = process.env.PREVIEW_SAMPLE_NAME;
if (host === undefined || !Number.isInteger(port) || !Number.isInteger(publicPort) || publicPort < 1 || publicPort > 65535 || name === undefined) {
  throw new Error("vite.config.mjs needs PREVIEW_SAMPLE_HOST, PREVIEW_SAMPLE_PORT, PREVIEW_SAMPLE_PUBLIC_PORT and PREVIEW_SAMPLE_NAME");
}

export default {
  server: {
    host: assignment === null ? "127.0.0.1" : process.env.DECK_PREVIEW_HOST,
    port,
    strictPort: true,
    // The gateway preserves the browser's Host, which is the public preview
    // hostname — Vite's DNS-rebinding guard would refuse it otherwise.
    allowedHosts: [host],
    hmr: { protocol: scheme === "https" ? "wss" : "ws", host, clientPort: publicPort },
  },
  plugins: [{
    name: "sample-app-api",
    configureServer(server) {
      server.middlewares.use(sampleApi);
      server.httpServer?.on("upgrade", (req, socket) => {
        if (req.url === "/ws" || req.url?.startsWith("/ws?")) {
          upgradeEcho(req, socket);
        }
        // Everything else is Vite's own HMR upgrade; not ours to answer.
      });
    },
  }],
};

function sampleApi(req, res, next) {
  if (!req.url.startsWith("/api/")) {
    next();
    return;
  }
  res.setHeader("Cache-Control", "no-store");
  const path = req.url.split("?")[0];
  if (req.method === "GET" && path === "/api/hello") {
    json(res, 200, { app: name, scheme, originPort: publicPort, port });
    return;
  }
  if (req.method === "GET" && path === "/api/whoami") {
    // What the app server actually received. The harness asserts the
    // gateway forwarded neither Deck's cookies nor trusted identity
    // headers, so this endpoint reports exactly those.
    json(res, 200, {
      cookie: req.headers.cookie ?? "",
      xForwardedUser: req.headers["x-forwarded-user"] ?? null,
      host: req.headers.host ?? "",
    });
    return;
  }
  if (req.method === "GET" && path === "/api/visit") {
    // A host-only app cookie, set and read back through the gateway.
    const had = /(?:^|;\s*)sample_token=/.test(req.headers.cookie ?? "");
    if (!had) {
      res.setHeader("Set-Cookie", `sample_token=${randomBytes(8).toString("hex")}; Path=/;${scheme === "https" ? " Secure;" : ""} SameSite=Lax`);
    }
    json(res, 200, { visits: had ? 2 : 1 });
    return;
  }
  if (req.method === "GET" && path === "/api/stream") {
    res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-store" });
    let n = 0;
    const timer = setInterval(() => {
      n += 1;
      res.write(`data: tick ${n}\n\n`);
      if (n >= 20) {
        clearInterval(timer);
        res.end();
      }
    }, 250);
    req.on("close", () => clearInterval(timer));
    return;
  }
  if (req.method === "POST" && path === "/api/echo") {
    let body = "";
    req.setEncoding("utf8");
    req.on("data", (chunk) => { body += chunk; });
    req.on("end", () => {
      try {
        json(res, 200, { ...JSON.parse(body), app: name });
      } catch {
        json(res, 400, { error: "bad json" });
      }
    });
    return;
  }
  json(res, 404, { error: "not found" });
}

function json(res, status, body) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

// A minimal RFC 6455 echo server: enough handshake, text frames and
// close/ping handling for the harness's echo probe. Only the sample app's
// own tiny messages ever ride it.
const WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";

function upgradeEcho(req, socket) {
  const key = req.headers["sec-websocket-key"];
  if (typeof key !== "string" || key === "") {
    socket.destroy();
    return;
  }
  const accept = createHash("sha1").update(key + WS_GUID).digest("base64");
  socket.write(
    "HTTP/1.1 101 Switching Protocols\r\n" +
    "Upgrade: websocket\r\n" +
    "Connection: Upgrade\r\n" +
    `Sec-WebSocket-Accept: ${accept}\r\n\r\n`,
  );
  socket.on("data", (chunk) => {
    const message = decodeFrame(chunk);
    if (message === null) return;
    if (message === "<<closed>>") {
      socket.write(Buffer.from([0x88, 0x00]));
      socket.end();
      return;
    }
    const payload = Buffer.from(`echo:${message}`, "utf8");
    socket.write(Buffer.concat([Buffer.from([0x81, payload.length]), payload]));
  });
  socket.on("error", () => socket.destroy());
}

function decodeFrame(chunk) {
  if (chunk.length < 2) return null;
  const opcode = chunk[0] & 0x0f;
  const masked = (chunk[1] & 0x80) !== 0;
  let length = chunk[1] & 0x7f;
  let offset = 2;
  if (length === 126) {
    if (chunk.length < 4) return null;
    length = chunk.readUInt16BE(2);
    offset = 4;
  }
  if (opcode === 0x8) return "<<closed>>";
  if (opcode === 0x9) return null; // ping: tiny apps, nothing to answer
  if (opcode !== 0x1) return null;
  const mask = masked ? chunk.subarray(offset, offset + 4) : null;
  offset += mask === null ? 0 : 4;
  if (chunk.length < offset + length) return null;
  const payload = chunk.subarray(offset, offset + length);
  if (mask !== null) {
    for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i % 4];
  }
  return payload.toString("utf8");
}
