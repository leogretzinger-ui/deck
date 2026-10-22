// Generates src/ui/icons-data.ts from the installed lucide-static
// package, so every icon's path data comes from the library — never a
// hand-transcribed string. Run after adding a name to ICONS or bumping
// lucide-static:
//
//   node tools/generate-icons.mjs
//
// The generated module exists because the unit suite runs under plain
// `node --test`, which cannot import .svg assets the way the vite build
// can; icons.test.ts re-derives every entry from node_modules and fails
// on any drift, so the checked-in file can never diverge from the
// package it names.
import { readFile, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const require = createRequire(join(here, "..", "package.json"));

// Every icon the app renders, by lucide name. Keep alphabetical.
export const ICONS = [
  "arrow-down",
  "arrow-up",
  "broom",
  "check",
  "chevron-down",
  "chevron-left",
  "chevron-right",
  "chevron-up",
  "circle-check",
  "circle-help",
  "columns-2",
  "corner-down-left",
  "delete",
  "download",
  "ellipsis",
  "eye",
  "file-text",
  "folder",
  "house",
  "info",
  "layers",
  "layout-panel-left",
  "lock",
  "maximize-2",
  "message-square",
  "minimize-2",
  "plus",
  "refresh-cw",
  "search",
  "settings",
  "square",
  "trash-2",
  "triangle-alert",
  "upload",
  "x",
];

// innerSvg strips the <svg> wrapper: the renderer supplies its own root
// element with the app's size, stroke, and accessibility attributes.
export function innerSvg(svg) {
  return svg
    .replace(/^[\s\S]*?<svg[^>]*>/, "")
    .replace(/<\/svg>[\s\S]*$/, "")
    .replace(/\s+/g, " ")
    .replace(/>\s+</g, "><")
    .replace(/\s*\/>/g, "/>")
    .trim();
}

export async function iconEntries() {
  const iconsDir = dirname(require.resolve("lucide-static/icons/x.svg"));
  return Promise.all(ICONS.map(async (name) => {
    const svg = await readFile(join(iconsDir, `${name}.svg`), "utf8");
    return [name, innerSvg(svg)];
  }));
}

const isMain = process.argv[1] === fileURLToPath(import.meta.url);
if (isMain) {
  const { version } = require("lucide-static/package.json");
  const entries = await iconEntries();
  const body = entries
    .map(([name, inner]) => `  "${name}":\n    \`${inner}\`,`)
    .join("\n");
  const out = `// GENERATED FILE — do not edit. Regenerate with:
//   node tools/generate-icons.mjs
// Source: lucide-static ${version} (the version pinned in pnpm-lock.yaml).
// icons.test.ts fails if this file drifts from the installed package.

export const LUCIDE_VERSION = "${version}";

export type IconName =
${ICONS.map((n) => `  | "${n}"`).join("\n")};

export const ICON_PATHS: Record<IconName, string> = {
${body}
};
`;
  await writeFile(join(here, "..", "src", "ui", "icons-data.ts"), out);
  process.stdout.write(`icons-data.ts: ${entries.length} icons from lucide-static ${version}\n`);
}
