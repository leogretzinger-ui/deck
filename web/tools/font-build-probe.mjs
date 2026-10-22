// Exercises real Vite builds and dev serving. Supply a licensed directory to
// additionally verify both licensed modes; no proprietary fixtures are stored.
import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, readdirSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { resolve, join } from "node:path";
import { once } from "node:events";
import { licensedFiles } from "../src/styles/fonts/select.ts";

const root = resolve(import.meta.dirname, "..");
const licensed = process.env.NOISEDECK_LICENSED_FONTS_DIR;
const sheet = join(root, "private/fonts/licensed.css");
const files = existsSync(sheet) ? licensedFiles(readFileSync(sheet, "utf8")) : [];
mkdirSync(join(root, "../build"), { recursive: true });
const scratch = mkdtempSync(join(root, "../build/font-build-"));
const vite = join(root, "node_modules/vite/bin/vite.js");
const env = (mode, directory) => ({ ...process.env, NOISEDECK_FONTS: mode ?? "", NOISEDECK_LICENSED_FONTS_DIR: directory ?? "" });
function build(mode, directory, label, error) {
  const out = join(scratch, label);
  const run = () => execFileSync(process.execPath, [vite, "build", "--outDir", out], { cwd: root, env: env(mode, directory), stdio: "pipe" });
  if (error) {
    assert.throws(run, e => error.test(String(e.stderr)), `${label}: expected build failure`);
    console.log(`${label}: refused as expected`);
    return;
  }
  run();
  assert.equal(existsSync(join(out, "bench.html")), true, `${label}: font bench must ship`);
  const actual = readdirSync(join(out, "fonts")).filter(f => f.endsWith(".woff2")).sort();
  const expected = label.includes("licensed") ? files.map(f => f.slice(7)).sort() : [];
  assert.deepEqual(actual, expected, `${label}: output asset boundary`);
  if (!expected.length) {
    const css = readdirSync(join(out, "assets")).filter(f => f.endsWith(".css")).map(f => readFileSync(join(out, "assets", f), "utf8")).join("\n");
    assert.doesNotMatch(css, /\/fonts\/(Euclid|Operator|Baskerville)/);
  }
  for (const file of expected) assert.deepEqual(readFileSync(join(out, "fonts", file)), readFileSync(join(directory, file)));
  console.log(`${label}: built with expected assets`);
}
try {
  build(undefined, undefined, "default-free");
  build("auto", undefined, "auto-free");
  build("free", licensed ?? "/missing", "explicit-free");
  if (files.length) {
    build("licensed", undefined, "absent", /requires assets/);
    build("auto", join(scratch, "missing"), "missing-directory", /Incomplete licensed font set/);
    const partial = join(scratch, "partial"); mkdirSync(partial);
    build("auto", partial, "empty-directory", /42 missing/);
    writeFileSync(join(partial, files[0].slice(7)), "invalid font");
    build("licensed", partial, "invalid-file", /42 missing/);
  } else {
    build("licensed", undefined, "missing-sheet", /requires the licensed stylesheet/);
    build("licensed", licensed ?? "/missing", "missing-sheet-with-assets", /requires the licensed stylesheet/);
  }
  build("unknown", undefined, "invalid-mode", /must be "auto", "free" or "licensed"/);
  if (licensed && files.length) {
    const partial = join(scratch, "partial");
    writeFileSync(join(partial, files[0].slice(7)), readFileSync(join(licensed, files[0].slice(7))));
    for (const mode of ["auto", "licensed"]) {
      build(mode, partial, `partial-${mode}`, /41 missing/);
      build(mode, licensed, `${mode}-licensed`);
    }
    // Port zero avoids colliding with another lane. Vite prints the chosen URL.
    const server = spawn(process.execPath, [vite, "--port", "0"], { cwd: root, env: { ...env("licensed", licensed), NOISEDECK_DEV_PREVIEW_BASE: "/__dev/" }, stdio: ["ignore", "pipe", "pipe"] });
    try {
      const origin = await new Promise((resolveURL, reject) => {
        let text = "";
        const timeout = setTimeout(() => reject(new Error("Vite did not start")), 15000);
        server.stdout.on("data", data => { text += data; const m = text.match(/http:\/\/127\.0\.0\.1:\d+/); if (m) { clearTimeout(timeout); resolveURL(m[0]); } });
        server.once("error", error => { clearTimeout(timeout); reject(error); });
        server.once("exit", code => { clearTimeout(timeout); reject(new Error(`Vite exited ${code}`)); });
      });
      for (const file of files) {
        const response = await fetch(origin + "/__dev" + file);
        assert.equal(response.status, 200, file);
        assert.deepEqual(Buffer.from(await response.arrayBuffer()), readFileSync(join(licensed, file.slice(7))), file);
      }
      console.log("dev preview: all 42 licensed fonts served unchanged");
    } finally {
      if (server.exitCode === null && server.signalCode === null) {
        const exited = once(server, "exit");
        server.kill();
        await exited;
      }
    }
  } else console.log("Licensed success paths skipped: supply NOISEDECK_LICENSED_FONTS_DIR to exercise them");
} finally { rmSync(scratch, { recursive: true, force: true }); }
