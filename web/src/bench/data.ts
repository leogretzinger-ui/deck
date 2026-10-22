// The font-substitution bench's fixed data: the candidate list and role
// mappings from the design prototype. A licensed build's mono role uses
// Operator Mono SSm, which is also the compare reference.

export type Role = "display" | "ui" | "prose" | "mono";
export type TrimKey = "s" | "l" | "t";
export type Licence = "licensed" | "ofl" | "apache" | "system" | "unverified";

export const ROLES: Role[] = ["display", "ui", "prose", "mono"];

export const LICENSED: Record<Role, string> = {
  display: "Euclid Flex XL",
  ui: "Euclid Flex",
  prose: "Baskerville 10 Pro",
  mono: "Operator Mono SSm",
};

// The free type set (src/styles/fonts/free.css): what a build without the
// licensed webfonts ships, and so its reference.
export const FREE: Record<Role, string> = {
  display: "Jost",
  ui: "Jost",
  prose: "EB Garamond",
  mono: "IBM Plex Mono",
};

// The faces this build actually carries. The bench compares candidates
// against the reference the app renders in, and lists only faces that can
// render: a licensed face named in a free build would resolve to the
// generic fallback and every number read off it would describe that
// fallback instead.
export const BUILD_FONTS: "licensed" | "free" =
  typeof __NOISEDECK_FONTS__ === "string" ? __NOISEDECK_FONTS__ : "licensed";
export const REFERENCE: Record<Role, string> = BUILD_FONTS === "free" ? FREE : LICENSED;
export const REFERENCE_GROUP = BUILD_FONTS === "free" ? "Deck · free set" : "Deck · licensed";

export const GENERIC: Record<Role, string> = {
  display: "sans-serif",
  ui: "sans-serif",
  prose: "serif",
  mono: "monospace",
};

export const FONTVAR: Record<Role, string> = {
  display: "--font-display",
  ui: "--font-ui",
  prose: "--font-prose",
  mono: "--font-mono",
};

// Which tokens each role owns. Trims rewrite these on the specimen container
// only, so the tool's own chrome keeps the real faces and the real ramp.
export const TOK: Record<Role, Record<TrimKey, string[]>> = {
  display: {
    s: ["--type-display-1-size", "--type-display-2-size", "--type-display-3-size"],
    l: ["--type-display-1-lh", "--type-display-2-lh", "--type-display-3-lh"],
    t: ["--type-display-1-ls", "--type-display-2-ls", "--type-display-3-ls"],
  },
  ui: {
    s: ["--type-title-1-size", "--type-title-2-size", "--type-title-3-size", "--type-body-size", "--type-body-sm-size", "--type-caption-size", "--type-micro-size", "--type-label-size", "--type-label-sm-size", "--type-button-size", "--type-control-size", "--type-control-sm-size"],
    l: ["--type-title-1-lh", "--type-title-2-lh", "--type-title-3-lh", "--type-body-lh", "--type-body-sm-lh", "--type-caption-lh", "--type-micro-lh"],
    t: ["--type-title-1-ls", "--type-title-2-ls", "--type-title-3-ls", "--type-body-ls", "--type-body-sm-ls", "--type-caption-ls", "--type-micro-ls", "--type-label-ls", "--type-button-ls"],
  },
  prose: {
    s: ["--type-prose-lead-size", "--type-prose-size", "--type-prose-sm-size"],
    l: ["--type-prose-lead-lh", "--type-prose-lh", "--type-prose-sm-lh"],
    t: ["--tc-prose-ls"],
  },
  mono: {
    s: ["--type-mono-size", "--type-mono-xh-ratio"],
    l: ["--type-mono-lh"],
    t: ["--type-mono-tracking"],
  },
};

export interface TrimSpec {
  key: TrimKey;
  min: string;
  max: string;
  step: string;
  def: string;
}

export const TRIMS: TrimSpec[] = [
  { key: "s", min: "0.75", max: "1.3", step: "0.01", def: "1" },
  { key: "l", min: "0.8", max: "1.5", step: "0.01", def: "1" },
  { key: "t", min: "-0.04", max: "0.12", step: "0.002", def: "0" },
];

export interface Candidate {
  n: string;
  q: string;
  r: string;
  lic: "ofl" | "apache";
}

// Free candidates, with the exact fonts.googleapis.com query each family
// supports. A wrong query 400s, so every load falls back to the bare family —
// worst case you get Regular only.
const V = "ital,wght@0,300..700;1,300..700";
export const CATALOGUE: Candidate[] = [
  { n: "Work Sans", q: V, r: "display ui", lic: "ofl" },
  { n: "Public Sans", q: V, r: "display ui", lic: "ofl" },
  { n: "Figtree", q: V, r: "display ui", lic: "ofl" },
  { n: "Plus Jakarta Sans", q: V, r: "display ui", lic: "ofl" },
  { n: "Hanken Grotesk", q: V, r: "display ui", lic: "ofl" },
  { n: "Schibsted Grotesk", q: "ital,wght@0,400..700;1,400..700", r: "display ui", lic: "ofl" },
  { n: "Instrument Sans", q: "ital,wght@0,400..700;1,400..700", r: "display ui", lic: "ofl" },
  { n: "Archivo", q: V, r: "display ui", lic: "ofl" },
  { n: "Inter", q: V, r: "display ui", lic: "ofl" },
  { n: "Manrope", q: "wght@300..700", r: "display ui", lic: "ofl" },
  { n: "Jost", q: "ital,wght@0,300..700;1,300..700", r: "display ui", lic: "ofl" },
  { n: "Outfit", q: "wght@300..700", r: "display", lic: "ofl" },
  { n: "Space Grotesk", q: "wght@300..700", r: "display", lic: "ofl" },
  { n: "Onest", q: "wght@300..700", r: "display ui", lic: "ofl" },
  { n: "Geist", q: "wght@300..700", r: "display ui", lic: "ofl" },
  { n: "Bricolage Grotesque", q: "wght@300..700", r: "display", lic: "ofl" },
  { n: "Libre Baskerville", q: "ital,wght@0,400;0,700;1,400", r: "prose", lic: "ofl" },
  { n: "EB Garamond", q: "ital,wght@0,400..700;1,400..700", r: "prose", lic: "ofl" },
  { n: "Source Serif 4", q: V, r: "prose", lic: "ofl" },
  { n: "Crimson Pro", q: V, r: "prose", lic: "ofl" },
  { n: "Newsreader", q: V, r: "prose", lic: "ofl" },
  { n: "Literata", q: V, r: "prose", lic: "ofl" },
  { n: "Lora", q: "ital,wght@0,400..700;1,400..700", r: "prose", lic: "ofl" },
  { n: "Spectral", q: "ital,wght@0,300;0,400;0,500;0,700;1,400", r: "prose", lic: "ofl" },
  { n: "Faustina", q: V, r: "prose", lic: "ofl" },
  { n: "Petrona", q: V, r: "prose", lic: "ofl" },
  { n: "Vollkorn", q: "ital,wght@0,400..700;1,400..700", r: "prose", lic: "ofl" },
  { n: "Gelasio", q: "ital,wght@0,400..700;1,400..700", r: "prose", lic: "ofl" },
  { n: "JetBrains Mono", q: V, r: "mono", lic: "ofl" },
  { n: "Victor Mono", q: "ital,wght@0,300..700;1,300..700", r: "mono", lic: "ofl" },
  { n: "IBM Plex Mono", q: "ital,wght@0,400;0,500;0,700;1,400", r: "mono", lic: "ofl" },
  { n: "Source Code Pro", q: V, r: "mono", lic: "ofl" },
  { n: "DM Mono", q: "ital,wght@0,300;0,400;0,500;1,400", r: "mono", lic: "ofl" },
  { n: "Space Mono", q: "ital,wght@0,400;0,700;1,400", r: "mono", lic: "ofl" },
  { n: "Fira Code", q: "wght@300..700", r: "mono", lic: "ofl" },
  { n: "Geist Mono", q: "wght@300..700", r: "mono", lic: "ofl" },
  { n: "Red Hat Mono", q: "ital,wght@0,300..700;1,300..700", r: "mono", lic: "ofl" },
  { n: "Roboto Mono", q: "ital,wght@0,300..700;1,300..700", r: "mono", lic: "apache" },
];

export const INSTALLED_PROBE: string[] = ["SF Pro", "SF Pro Display", "SF Pro Text", "SF Mono", "New York", "Helvetica", "Helvetica Neue", "Arial", "Avenir", "Avenir Next", "Futura", "Gill Sans", "Optima", "Verdana", "Tahoma", "Trebuchet MS", "Lucida Grande", "Charter", "Georgia", "Palatino", "Iowan Old Style", "Hoefler Text", "Baskerville", "Times New Roman", "Didot", "Bodoni 72", "Cochin", "American Typewriter", "Courier New", "Menlo", "Monaco", "Andale Mono", "PT Sans", "PT Serif", "PT Mono", "Athelas", "Superclarendon", "Rockwell", "Big Caslon", "Segoe UI", "Calibri", "Cambria", "Consolas", "Constantia", "Corbel", "Candara", "Cascadia Code", "Cascadia Mono", "Selawik", "Aptos", "Euclid Flex", "Euclid Flex XL", "Baskerville 10 Pro", "Dank Mono", "Operator Mono", "Berkeley Mono", "MonoLisa", "Söhne", "Graphik", "Tiempos Text", "Canela", "Untitled Sans", "Proxima Nova", "Gotham", "Circular Std", "Iosevka", "Hack", "Inconsolata", "Merriweather", "Playfair Display", "Nunito Sans", "Rubik", "Karla", "Barlow", "Mulish", "Sora", "Urbanist", "Satoshi", "General Sans", "Switzer", "Cabinet Grotesk", "Roboto", "Roboto Condensed", "Roboto Slab", "Open Sans", "Lato", "Source Sans 3", "Source Sans Pro", "Source Serif Pro", "Source Serif 4", "Noto Sans", "Noto Serif", "Fira Sans", "Fira Mono", "Ubuntu", "Ubuntu Mono", "DejaVu Sans", "DejaVu Serif", "DejaVu Sans Mono", "Liberation Sans", "Liberation Serif", "Liberation Mono", "Arial Narrow", "Arial Black", "Century Gothic", "Garamond", "Impact", "Lucida Console", "Lucida Sans Unicode", "Comic Sans MS", "Copperplate", "Geneva", "Skia", "Chalkboard", "Marker Felt", "Apple Chancery", "Snell Roundhand", "Inter Tight", "Inter Display", "Poppins", "Montserrat", "Raleway", "Oswald", "Quicksand", "Josefin Sans", "Jost", "DM Sans", "DM Serif Display", "Space Grotesk", "Work Sans", "Public Sans", "Figtree", "Manrope", "Outfit", "Archivo", "Libre Franklin", "Libre Baskerville", "EB Garamond", "Crimson Text", "Crimson Pro", "Bitter", "Cardo", "Alegreya", "Zilla Slab", "Recursive", "Literata", "Newsreader", "Lora", "Spectral", "JetBrains Mono", "IBM Plex Sans", "IBM Plex Serif", "IBM Plex Mono", "Victor Mono", "Fira Code", "Source Code Pro", "Geist", "Geist Mono", "Anonymous Pro", "Comic Code", "Input Mono", "PragmataPro", "Triplicate", "Nitti", "Whitney", "Knockout", "Sentinel", "Chronicle Text", "Mercury Text", "Publico", "Founders Grotesk", "GT America", "GT Walsheim", "Neue Haas Grotesk", "Suisse Int'l", "ABC Diatype", "Aeonik", "Basis Grotesque", "Favorit", "Maison Neue", "National", "Larsseit", "Lyon Text", "Recoleta", "Reckless", "Editorial New", "Neue Montreal", "PP Neue Machina", "Migra", "Right Grotesk"];

export const LIC_TEXT: Record<Licence, string> = { licensed: "Licensed", ofl: "Free · OFL", apache: "Free · Apache", system: "System", unverified: "Unverified" };
export const LIC_CLASS: Record<Licence, string> = { licensed: "bp-badge--evidence", ofl: "bp-badge--success", apache: "bp-badge--success", system: "", unverified: "bp-badge--warning" };
export const CTX_TITLE: Record<string, string> = { answer: "in one answer", interface: "in an interface", machine: "as machine voice", specimen: "as a specimen" };
// A real substitution replaces one role and leaves the rest of the system
// standing, so each context starts on the role it is actually about. The
// choice is remembered per context.
export const CTX_TARGET: Record<string, string> = { answer: "prose", interface: "ui", machine: "mono", specimen: "all" };
export const TARGET_NOUN: Record<string, string> = { display: "display faces", ui: "interface faces", prose: "prose faces", mono: "monos", all: "typefaces" };
// The control names the weight, not an offset from one: a relative scale asks
// for two numbers per face and the base differs per face, so the same label
// named a different weight in each pane.
export const WNAME: Record<number, string> = { 100: "Thin", 200: "ExtraLight", 300: "Light", 400: "Regular", 500: "Medium", 600: "Semibold", 700: "Bold", 800: "ExtraBold", 900: "Black" };
export const GENERIC_STEPS = [200, 300, 400, 500, 600, 700, 800];
// Deck's --type-mono-weight:700 is NOT a neutral setting to test a face at:
// it exists as a correction for a light reference cut, so applying it to a
// candidate measures the system's compensation rather than the candidate. A
// face renders at ITS OWN weight: the declared one where known, 400 otherwise.
export const NATURAL_W: Record<string, number> = { "operator mono": 400, "operator mono ssm": 400 };
// The mono baseline is the reference's original setting, not the live token.
// 14.5px / 1.45 / -0.01em are per-face corrections; measuring candidates
// against a correction for one of them is circular, so 1x trims mean
// 13px / 1.62 / 0em — the 21.06px line box every other face has to earn.
export const MONO_REF: Record<string, string> = { "--type-mono-size": "13px", "--type-mono-lh": "1.62", "--type-mono-tracking": "0em" };
// Cuts actually on disk, where known. A shifted ramp is snapped to these so
// the token never names a weight the face cannot draw.
export const CUTS: Record<string, number[]> = { "operator mono": [300, 400, 500, 700], "operator mono ssm": [300, 400, 500, 700], "dank mono": [400, 700] };
// Plain ASCII on purpose: a control character here is legal in the DOM but
// breaks page serialisation for screenshots.
export const CUSTOM = "__tc_custom";
// .bp-source__quote is prose-family but takes no weight token, so it has to
// be named here or the prose weight visibly skips the source list.
export const PROSE_SEL = '.bp-prose,.bp-source__quote,[data-wrole="prose"]';
export const PALETTES = ["cobalt", "neutral", "warm", "teal", "violet"];
