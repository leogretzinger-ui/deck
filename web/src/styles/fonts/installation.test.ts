import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { installationFonts } from "./installation.ts";

const sheet = '@font-face{src:url("/fonts/Test.woff2")}';

test("old public fonts cannot leak through Vite's public-directory copy in any mode", () => {
  const root = mkdtempSync(join(tmpdir(), "deck-fonts-"));
  try {
    mkdirSync(join(root, "fonts", "free"), { recursive: true });
    assert.equal(installationFonts(sheet, root).fontSet, "free");
    assert.equal(installationFonts(sheet, root, "free", "/missing").fontSet, "free");
    for (const location of ["fonts", "fonts/legacy", "legacy"]) {
      mkdirSync(join(root, location), { recursive: true });
      const asset = join(root, location, "EuclidFlex-Regular-WebS.woff2");
      writeFileSync(asset, "old licensed asset");
      for (const mode of [undefined, "auto", "free", "licensed"]) {
        assert.throws(() => installationFonts(sheet, root, mode), /Move licensed fonts out of web\/public\/fonts/);
      }
      rmSync(asset);
    }
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test("a truncated WOFF2 with its magic intact is rejected", () => {
  const root = mkdtempSync(join(tmpdir(), "deck-fonts-"));
  try {
    mkdirSync(join(root, "public", "fonts", "free"), { recursive: true });
    const file = "Test.woff2";
    const woff = readFileSync(new URL("../../../public/fonts/free/jost-latin-wght-normal.woff2", import.meta.url));
    writeFileSync(join(root, file), woff.subarray(0, 48));
    const manifest = `@font-face{src:url("/fonts/${file}")}`;
    assert.throws(() => installationFonts(manifest, join(root, "public"), "auto", root), /Incomplete licensed font set/);
    writeFileSync(join(root, file), woff.subarray(0, woff.length - 1));
    assert.throws(() => installationFonts(manifest, join(root, "public"), "auto", root), /Incomplete licensed font set/);
    writeFileSync(join(root, file), woff);
    assert.equal(installationFonts(manifest, join(root, "public"), "auto", root).fontSet, "licensed");
  } finally { rmSync(root, { recursive: true, force: true }); }
});
