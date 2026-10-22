// Generates the preview fixture's images under src/app/fixture-images/: flat
// drawings of a page, a phone screen and an icon, at the pixel sizes the
// preview listing claims, so the image viewer decodes, measures and fits
// real files. Run after changing a drawing:
//
//   node tools/generate-fixture-images.mjs
//
// Output is deterministic (no timestamps, fixed compression), so a rerun
// on an unchanged drawing leaves the checked-in files byte-identical.
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { deflateSync } from "node:zlib";

const here = dirname(fileURLToPath(import.meta.url));
const out = join(here, "..", "src", "app", "fixture-images");

const INK = "#1E2633";
const PAPER = "#F3F1EA";
const RULE = "#A3A196";
const CARD = "#FFFFFF";
const BLUE = "#2F5BD3";

// Each shape is [left%, top%, width%, height%, colour, radius]; a radius
// in px is scaled with the drawing, "50%" makes an ellipse.
const PAGE = [
  [0, 0, 100, 8, INK], [0, 8, 17, 92, "#E3DFD3"], [23, 15, 38, 5, INK, 2], [23, 24, 58, 2, RULE],
  [23, 28.5, 46, 2, RULE], [23, 37, 22, 32, CARD, 3], [48, 37, 22, 32, CARD, 3], [73, 37, 22, 32, CARD, 3],
  [23, 75, 13, 6, BLUE, 3],
];
const PHONE = [
  [0, 0, 100, 7, INK], [7, 11, 60, 4, INK, 2], [7, 18, 86, 1.6, RULE], [7, 21, 70, 1.6, RULE],
  [7, 27, 86, 22, CARD, 3], [7, 52, 86, 22, CARD, 3], [7, 84, 86, 6, BLUE, 4],
];
const ICON = [[14, 14, 72, 72, BLUE, "50%"], [34, 34, 32, 32, PAPER, "50%"]];

const IMAGES = [
  { name: "home.png", width: 1440, height: 900, ground: PAPER, shapes: PAGE },
  { name: "home-mobile.png", width: 393, height: 852, ground: PAPER, shapes: PHONE },
  { name: "icon-512.png", width: 512, height: 512, ground: null, shapes: ICON },
];

function rgb(hex) {
  return [1, 3, 5].map((i) => Number.parseInt(hex.slice(i, i + 2), 16));
}

// coverage is how much of the pixel at (x, y) a shape covers, sampled
// 4×4 so rounded corners and circles are antialiased.
function coverage(shape, x, y, width, height) {
  const [l, t, w, h, , radius] = shape;
  const x0 = (l / 100) * width;
  const y0 = (t / 100) * height;
  const x1 = x0 + (w / 100) * width;
  const y1 = y0 + (h / 100) * height;
  let hits = 0;
  for (let sy = 0; sy < 4; sy++) {
    for (let sx = 0; sx < 4; sx++) {
      const px = x + (sx + 0.5) / 4;
      const py = y + (sy + 0.5) / 4;
      if (px < x0 || px >= x1 || py < y0 || py >= y1) continue;
      if (radius === "50%") {
        const cx = (x0 + x1) / 2;
        const cy = (y0 + y1) / 2;
        const dx = (px - cx) / ((x1 - x0) / 2);
        const dy = (py - cy) / ((y1 - y0) / 2);
        if (dx * dx + dy * dy <= 1) hits++;
        continue;
      }
      const r = (radius ?? 0) * (width / 720);
      const cx = Math.min(Math.max(px, x0 + r), x1 - r);
      const cy = Math.min(Math.max(py, y0 + r), y1 - r);
      if ((px - cx) ** 2 + (py - cy) ** 2 <= r * r + 1e-9) hits++;
    }
  }
  return hits / 16;
}

function draw({ width, height, ground, shapes }) {
  const pixels = new Float64Array(width * height * 4);
  const base = ground === null ? [0, 0, 0, 0] : [...rgb(ground), 255];
  for (let i = 0; i < width * height; i++) pixels.set(base, i * 4);
  for (const shape of shapes) {
    const [r, g, b] = rgb(shape[4]);
    const x0 = Math.max(0, Math.floor((shape[0] / 100) * width));
    const y0 = Math.max(0, Math.floor((shape[1] / 100) * height));
    const x1 = Math.min(width, Math.ceil(((shape[0] + shape[2]) / 100) * width));
    const y1 = Math.min(height, Math.ceil(((shape[1] + shape[3]) / 100) * height));
    for (let y = y0; y < y1; y++) {
      for (let x = x0; x < x1; x++) {
        const a = coverage(shape, x, y, width, height);
        if (a === 0) continue;
        const i = (y * width + x) * 4;
        const da = pixels[i + 3] / 255;
        const oa = a + da * (1 - a);
        for (const [c, v] of [[0, r], [1, g], [2, b]]) {
          pixels[i + c] = oa === 0 ? 0 : (v * a + pixels[i + c] * da * (1 - a)) / oa;
        }
        pixels[i + 3] = oa * 255;
      }
    }
  }
  return pixels;
}

const CRC_TABLE = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});

function crc32(bytes) {
  let c = 0xffffffff;
  for (const byte of bytes) c = CRC_TABLE[(c ^ byte) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function chunk(type, data) {
  const head = Buffer.alloc(8);
  head.writeUInt32BE(data.length, 0);
  head.write(type, 4, "ascii");
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(Buffer.concat([head.subarray(4), data])), 0);
  return Buffer.concat([head, data, crc]);
}

// png encodes RGBA, or RGB when every pixel is opaque, so an opaque
// drawing decodes as a file with no alpha channel at all.
function png(width, height, pixels) {
  let opaque = true;
  for (let i = 3; i < pixels.length; i += 4) if (Math.round(pixels[i]) < 255) opaque = false;
  const channels = opaque ? 3 : 4;
  const raw = Buffer.alloc((width * channels + 1) * height);
  for (let y = 0; y < height; y++) {
    const row = y * (width * channels + 1);
    for (let x = 0; x < width; x++) {
      for (let c = 0; c < channels; c++) {
        raw[row + 1 + x * channels + c] = Math.round(pixels[(y * width + x) * 4 + c]);
      }
    }
  }
  const header = Buffer.alloc(13);
  header.writeUInt32BE(width, 0);
  header.writeUInt32BE(height, 4);
  header[8] = 8;
  header[9] = opaque ? 2 : 6;
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", header),
    chunk("IDAT", deflateSync(raw, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

await mkdir(out, { recursive: true });
for (const image of IMAGES) {
  const bytes = png(image.width, image.height, draw(image));
  await writeFile(join(out, image.name), bytes);
  // A copy cut off before its first pixel: its signature says PNG, so the
  // image route serves it, and the browser has nothing to decode — the
  // viewer's undecodable state. A cut anywhere after the first image data
  // still decodes in Chromium, as a partly drawn picture.
  if (image.name === "home.png") {
    await writeFile(join(out, "capture-partial.png"), bytes.subarray(0, 33));
  }
  console.log(`${image.name}: ${image.width}×${image.height}, ${bytes.length} bytes`);
}
