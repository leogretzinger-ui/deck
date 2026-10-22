import { chromium } from "playwright";

const endpoint = "ws://127.0.0.1:3000/";
const server = await chromium.launchServer({
  headless: true,
  host: "127.0.0.1",
  port: 3000,
  wsPath: "/",
});

if (server.wsEndpoint() !== endpoint) {
  await server.close();
  throw new Error(`unexpected browser endpoint: ${server.wsEndpoint()}`);
}

let closing = false;
async function shutdown() {
  if (closing) return;
  closing = true;
  await server.close();
}

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.once(signal, () => {
    shutdown().catch((error) => {
      console.error(error);
      process.exitCode = 1;
    });
  });
}
