// Pure pieces of the font-substitution bench: trim storage math, licence
// resolution, the Google Fonts query URL and its fallback, weight snapping,
// and the match solver. Everything here is measurable-in, numbers-out so the
// unit suite can hold the rules without a browser.
import {
  CATALOGUE, CUTS, GENERIC_STEPS, LICENSED, LIC_CLASS, LIC_TEXT, MONO_REF,
  NATURAL_W, ROLES, TRIMS,
  type Licence, type Role, type TrimKey,
} from "./data.ts";

/* ---------- licence ---------- */

// licOf answers the question the tool exists for: which faces are you still
// paying for. The licensed faces are checked before any loadability probe,
// because a probe reports true for anything loadable and would label a
// licensed face "system" on a machine that has it — an invitation to ship it.
export function licOf(name: string, validFamily?: (f: string) => boolean): Licence {
  const f = (name || "").trim().toLowerCase();
  if (ROLES.some((r) => LICENSED[r].toLowerCase() === f)) return "licensed";
  const hit = CATALOGUE.find((c) => c.n.toLowerCase() === f);
  if (hit) return hit.lic;
  if (validFamily && validFamily(name)) return "system";
  return "unverified";
}

export function licBadge(lic: Licence): { text: string; className: string } {
  return { text: LIC_TEXT[lic], className: "bp-badge bp-badge--sm " + LIC_CLASS[lic] };
}

/* ---------- webfont loading ---------- */

// The exact query a family supports, or the bare family when none is known.
// A wrong query 400s at fonts.googleapis.com, so the caller must fall back to
// bareFamilyHref rather than loading nothing.
export function googleFontsHref(name: string, query?: string): string {
  const base = "https://fonts.googleapis.com/css2?family=" +
    encodeURIComponent(name).replace(/%20/g, "+");
  return base + (query ? ":" + query : "") + "&display=swap";
}

export function catalogueQuery(name: string): string | undefined {
  const hit = CATALOGUE.find((c) => c.n.toLowerCase() === (name || "").trim().toLowerCase());
  return hit?.q;
}

// The Google Fonts query for a candidate, or undefined when the family is
// one this build already ships. A free build bundles Jost, EB Garamond and
// IBM Plex Mono with the size-adjust and metric overrides that make them the
// app's reference; the same names sit in the catalogue, and a remote sheet
// for them would add competing @font-face rules without those descriptors,
// which can win matching and move every number the bench reads off the
// reference. What the build supplies is never fetched.
export function remoteQuery(name: string, supplied: Iterable<string>): string | undefined {
  const key = (name || "").trim().toLowerCase();
  for (const s of supplied) if (s.toLowerCase() === key) return undefined;
  return catalogueQuery(name);
}

/* ---------- weights ---------- */

export function monoBase(font: string): number {
  const k = String(font || "").trim().toLowerCase();
  const natural = NATURAL_W[k];
  if (natural !== undefined) return natural;
  if (LICENSED.mono.toLowerCase() === k) return 400;
  return 400;
}

export function baseWeight(role: Role, font: string): number {
  if (role === "mono") return monoBase(font);
  return role === "display" ? 300 : 400;
}

// Absolute weights. A face with declared cuts lists exactly those, so the
// picker cannot ask for a weight the browser would have to synthesise.
export function weightOptionsFor(font: string): number[] {
  return CUTS[String(font || "").trim().toLowerCase()] ?? GENERIC_STEPS;
}

// A shifted ramp is snapped to the face's real cuts so the token never names
// a weight the face cannot draw: asking for a missing cut gets a synthesised
// smear while the DOM claims the number.
export function snapWeight(font: string, n: number): number {
  const raw = Math.max(100, Math.min(900, n));
  const cuts = CUTS[String(font || "").trim().toLowerCase()];
  if (!cuts) return raw;
  return cuts.reduce((a, b) => (Math.abs(b - raw) < Math.abs(a - raw) ? b : a));
}

/* ---------- trims ---------- */

export type TrimScope = "showcase" | "compare";

// Trims are keyed by TYPEFACE, never by slot or pane: a trim is a fact about
// the face's metrics against a baseline, so it must carry into every pane the
// family appears in. Size/lead/track are additionally scoped per mode because
// the two modes have different baselines (the showcase shows the system's own
// tokens; compare measures against the mono reference); weight ("w:<role>")
// is a property of the face within a role and is shared across modes.
export type TrimStore = Record<string, Record<string, string>>;

export function trimOf(store: TrimStore, font: string, key: string, scope: TrimScope): string {
  const t = store[font];
  if (key.startsWith("w")) return t && t[key] !== undefined ? String(t[key]) : "";
  const k = key + "@" + scope;
  const def = TRIMS.find((x) => x.key === key)?.def ?? "";
  return t && t[k] !== undefined ? String(t[k]) : def;
}

export function setTrim(store: TrimStore, font: string, key: string, v: string, scope: TrimScope): TrimStore {
  if (!font) return store;
  if (key.startsWith("w") && !(parseInt(v, 10) >= 100)) return store;
  const k = key.startsWith("w") ? key : key + "@" + scope;
  const next = { ...store, [font]: { ...(store[font] ?? {}), [k]: String(v) } };
  return next;
}

// Stored weights are absolute; anything under 100 is a leftover from an old
// relative scale and reads as "unset" — a shift and a weight are not
// convertible without knowing which base it was taken against.
export function weightFor(store: TrimStore, role: Role, font: string): number {
  const stored = parseInt(trimOf(store, font, wKey(role), "showcase"), 10);
  return stored >= 100 ? stored : baseWeight(role, font);
}

// Keyed by font AND role: one face can be substituted into several roles at
// once and each role has its own base, so a single per-font value would mean
// four different weights.
export function wKey(role: Role): string {
  return "w:" + role;
}

/* ---------- readouts ---------- */

export function trimReadout(key: TrimKey, value: number): string {
  if (key === "t") {
    const sign = value > 0 ? "+" : value < 0 ? "−" : "";
    return sign + Math.abs(Math.round(value * 1000));
  }
  return Math.round(value * 100) + "%";
}

/* ---------- match solving ---------- */

export interface FaceMetrics {
  // Ink extent of the anchor glyph per em (x-height for 'x', digit height
  // for '8'), and advance width per em.
  inkPerEm: number;
  advancePerEm: number;
}

export interface MatchSolution {
  s: string;
  l: string;
  t: string;
}

// Solves one pane's trims so it matches the reference face's geometry: size
// so the anchor glyph measures the same, lead to hold the reference LINE BOX
// (lead is a multiplier on the face's own size, so it is the inverse of the
// size trim), tracking to hold the ADVANCE so a 60ch line is the same length
// in every pane and wrap points transfer. Returns null when either face
// yielded no ink — an unmeasurable face must never report numbers.
export function solveMatch(ref: FaceMetrics, face: FaceMetrics): MatchSolution | null {
  const refSize = parseFloat(MONO_REF["--type-mono-size"] ?? "13");
  if (!(ref.inkPerEm > 0) || !(face.inkPerEm > 0)) return null;
  const refInk = ref.inkPerEm * refSize;
  const [sSpec, lSpec, tSpec] = TRIMS;
  if (!sSpec || !lSpec || !tSpec) return null;
  const lo = parseFloat(sSpec.min), hi = parseFloat(sSpec.max);
  const lLo = parseFloat(lSpec.min), lHi = parseFloat(lSpec.max);
  const tLo = parseFloat(tSpec.min), tHi = parseFloat(tSpec.max);
  const want = refInk / face.inkPerEm;
  const mul = Math.min(hi, Math.max(lo, Math.round((want / refSize) * 100) / 100));
  const size = refSize * mul;
  const lMul = Math.min(lHi, Math.max(lLo, Math.round((refSize / size) * 100) / 100));
  const refAdv = ref.advancePerEm * refSize;
  const tRaw = refAdv / size - face.advancePerEm;
  const tAdd = Math.min(tHi, Math.max(tLo, Math.round(tRaw * 500) / 500));
  return { s: String(mul), l: String(lMul), t: String(tAdd) };
}

// The families a pane may pick from, in the order the picker shows them:
// this build's reference faces first, then the catalogue, then what the
// machine and you supplied. One entry per family, by name
// regardless of case, the first group to name it winning — a free build's
// reference faces are catalogue faces too, and display and interface share
// a family, so without this the picker listed Jost three times.
export interface FamilyEntry { name: string; group: string; lic: Licence }

export function familyList(
  reference: string[], referenceGroup: string,
  catalogue: Array<{ name: string; lic: Licence }>,
  installed: string[], custom: string[],
  licenceOf: (name: string) => Licence,
): FamilyEntry[] {
  const list: FamilyEntry[] = [];
  const seen = new Set<string>();
  const add = (name: string, group: string, lic: Licence) => {
    const key = name.trim().toLowerCase();
    if (!key || seen.has(key)) return;
    seen.add(key);
    list.push({ name: name.trim(), group, lic });
  };
  for (const name of reference) add(name, referenceGroup, licenceOf(name));
  for (const c of catalogue) add(c.name, "Free · redistributable", c.lic);
  for (const n of [...installed].sort((a, b) => a.localeCompare(b))) add(n, "On this machine", "system");
  for (const n of custom) add(n, "On this machine", "system");
  return list;
}
