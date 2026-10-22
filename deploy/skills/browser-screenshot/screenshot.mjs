// Screenshot a page through the browser overlay and save it to the
// project's Assets panel.
//
//   node screenshot.mjs <url> [name] [--width <pixels>]
import { mkdir, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";

// The image copies this client from the same lockfile install the browser
// sidecar runs; Playwright refuses to connect across a version mismatch.
const CLIENT = "/opt/noise-browser/node_modules/playwright";
const ASSETS = path.join(".deck", "assets");
const HEIGHT = 800;
const USAGE = "usage: screenshot.mjs <url> [name] [--width <pixels>]";

function fail(message, code = 1) {
  console.error(message);
  process.exit(code);
}

function firstLine(err) {
  return String(err?.message ?? err).split("\n")[0];
}

function parseArgs(argv) {
  const positional = [];
  let width = 1280;
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    let value;
    if (arg === "--width") {
      value = argv[++i];
    } else if (arg.startsWith("--width=")) {
      value = arg.slice("--width=".length);
    } else if (arg.startsWith("-")) {
      fail(`unknown option ${arg}\n${USAGE}`, 2);
    } else {
      positional.push(arg);
      continue;
    }
    if (!/^\d+$/.test(value ?? "") || Number(value) < 200 || Number(value) > 4000) {
      fail(`--width takes a whole number of pixels from 200 to 4000\n${USAGE}`, 2);
    }
    width = Number(value);
  }
  if (positional.length < 1 || positional.length > 2) fail(USAGE, 2);
  return { target: positional[0], name: positional[1], width };
}

// A bare host gets a scheme: plain HTTP for loopback, where app previews
// run without TLS, and HTTPS for everything else.
function pageURL(target) {
  let text = target;
  if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(text)) {
    const loopback = /^(localhost|127(\.\d{1,3}){3}|\[::1\])(:\d+)?(\/|$)/i.test(text);
    text = `${loopback ? "http" : "https"}://${text}`;
  }
  let url;
  try {
    url = new URL(text);
  } catch {
    fail(`not a URL: ${target}`, 2);
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    fail(`only http and https pages can be captured, not ${url.protocol}`, 2);
  }
  return url;
}

function fileName(name, url) {
  if (name === undefined) {
    const pad = (n) => String(n).padStart(2, "0");
    const d = new Date();
    const stamp = `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
    const host = url.host.replace(/[^A-Za-z0-9.-]+/g, "-").replace(/^-+|-+$/g, "");
    return `${host}-${stamp}.png`;
  }
  const base = name.replace(/\.png$/i, "");
  if (!/^[A-Za-z0-9_-][A-Za-z0-9._-]*$/.test(base)) {
    fail("the name must be a plain file name: letters, digits, dot, dash and underscore", 2);
  }
  return `${base}.png`;
}

const argv = process.argv.slice(2);
if (argv.includes("-h") || argv.includes("--help")) {
  console.log(USAGE);
  process.exit(0);
}

const endpoint = process.env.NOISE_BROWSER_WS ?? "";
if (endpoint === "") {
  fail("The Deck browser overlay is not enabled (NOISE_BROWSER_WS is unset), so there is no browser to take a screenshot with.");
}

const { target, name, width } = parseArgs(argv);
const url = pageURL(target);
const file = path.join(ASSETS, fileName(name, url));

let chromium;
try {
  ({ chromium } = createRequire(import.meta.url)(CLIENT));
} catch {
  fail(`The Playwright client is missing at ${CLIENT}; this Deck image does not ship the browser screenshot client.`);
}

let browser;
try {
  browser = await chromium.connect(endpoint, { timeout: 15_000 });
} catch (err) {
  fail(`Could not reach the browser at ${endpoint}: ${firstLine(err)}`);
}

let status;
let image;
try {
  const context = await browser.newContext({ viewport: { width, height: HEIGHT } });
  const page = await context.newPage();
  const response = await page.goto(url.href, { waitUntil: "load", timeout: 30_000 });
  status = response?.status();
  image = await page.screenshot();
} catch (err) {
  await browser.close();
  fail(`Could not capture ${url.href}: ${firstLine(err)}`);
}
await browser.close();

await mkdir(ASSETS, { recursive: true });
await writeFile(file, image);

console.log(`Saved ${path.resolve(file)} (${width}x${HEIGHT}).`);
console.log(`It appears in the Assets panel as ${file}.`);
if (status !== undefined && status >= 400) {
  console.log(`The page answered HTTP ${status}, so the screenshot shows an error page.`);
}
