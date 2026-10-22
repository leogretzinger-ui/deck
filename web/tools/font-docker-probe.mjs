// Packaging/cache regression only: OFL bytes stand in for licensed filenames.
// The actual licensed visual gate uses locally supplied licensed assets.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { resolve, join } from "node:path";
import { licensedFiles } from "../src/styles/fonts/select.ts";

const root = resolve(import.meta.dirname, "../..");
mkdirSync(join(root, "build"), { recursive: true });
const scratch = mkdtempSync(join(root, "build/docker-fonts-"));
const image = `deck-font-probe:${process.pid}`;
const files = licensedFiles(readFileSync(join(root, "web/private/fonts/licensed.css"), "utf8")).map(p => p.slice(7));
const fixture = readFileSync(join(root, "web/public/fonts/free/jost-latin-wght-normal.woff2"));
for (const file of files) writeFileSync(join(scratch, file), fixture);
function build(mode, supplied, count, failure) {
  const args = ["build", "--target", "web-build", "--tag", image, "--build-arg", `NOISEDECK_FONTS=${mode}`];
  if (supplied) args.push("--build-context", `licensed-fonts=${scratch}`);
  args.push(".");
  const run = () => execFileSync("docker", args, { cwd: root, encoding: "utf8", stdio: "pipe", maxBuffer: 16 * 1024 * 1024 });
  if (failure) assert.throws(run, error => failure.test(String(error.stderr)));
  else {
    try { run(); } catch (error) { process.stderr.write(String(error.stderr)); throw error; }
    const actual = execFileSync("docker", ["run", "--rm", image, "node", "-e", 'console.log(require("node:fs").readdirSync("dist/fonts").filter(f=>f.endsWith(".woff2")).length)'], { encoding: "utf8" }).trim();
    assert.equal(Number(actual), count);
  }
  console.log(`${mode}, context=${supplied}: ${failure ? "refused" : `${count} external font files`}`);
}
try {
  build("auto", false, 0);
  build("auto", true, 42);
  // Reuse the same builder/cache: removing inputs must select free again.
  build("auto", false, 0);
  rmSync(join(scratch, files[0]));
  build("auto", true, 0, /Incomplete licensed font set/);
  build("licensed", false, 0, /requires assets/);
  build("free", true, 0);
} finally {
  rmSync(scratch, { recursive: true, force: true });
  try { execFileSync("docker", ["image", "rm", image], { stdio: "ignore" }); } catch { /* Build may have failed before producing an image. */ }
}
