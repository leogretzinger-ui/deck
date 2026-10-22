import assert from "node:assert/strict";
import { createRequire } from "node:module";
import test from "node:test";
import { ICONS, iconEntries } from "../../tools/generate-icons.mjs";
import { ICON_PATHS, LUCIDE_VERSION } from "./icons-data.ts";
import { lucideIcon } from "./icons.ts";

const require = createRequire(import.meta.url);

// The generated module must be exactly what the generator emits from the
// installed package: an edit by hand, a stale regeneration after a
// version bump, or a name missing from the manifest all fail here.
test("icons-data.ts matches the installed lucide-static package", async () => {
  const { version } = require("lucide-static/package.json");
  assert.equal(LUCIDE_VERSION, version,
    "icons-data.ts was generated from a different lucide-static — rerun tools/generate-icons.mjs");
  const entries = await iconEntries();
  assert.deepEqual(Object.keys(ICON_PATHS).sort(), [...ICONS].sort(),
    "the generated record and the generator manifest disagree");
  for (const [name, inner] of entries) {
    assert.equal(ICON_PATHS[name as keyof typeof ICON_PATHS], inner,
      `${name} drifted from lucide-static ${version}`);
  }
});

test("the renderer wraps the library path data in the app's svg root", () => {
  const svg = lucideIcon("x", 14);
  assert.match(svg, /^<svg aria-hidden="true" width="14" height="14" viewBox="0 0 24 24"/);
  assert.match(svg, /stroke-width="1.75"/);
  assert.ok(svg.includes(ICON_PATHS.x));
});
