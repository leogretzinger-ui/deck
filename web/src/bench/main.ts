// The font-substitution bench (gated experiment; served at /bench.html only
// when NOISEDECK_EXPERIMENTS_FONT_BENCH is on). Transcribed from the design
// prototype's logic into Deck's vanilla-TS
// idiom; the pure pieces live in logic.ts, the fixed data in data.ts, the
// markup in markup.ts. The prototype's behavior rules are preserved:
//
//   - the tool's own chrome is pinned via --tc-chrome so substitution never
//     re-sets the controls;
//   - substitution re-declares --font-display/-ui/-prose/-mono on the
//     specimen container ONLY — never font-family per element;
//   - trims multiply the live --type-* values read off :root and belong to
//     the TYPEFACE, not the slot;
//   - trims are session-scoped while picks/mode/palette persist under
//     bp.fontsub.*;
//   - document.fonts.load runs per weight/size pair before ANY measurement,
//     then the serif-fallback width assert refuses to report numbers for a
//     face that did not load.
import "../styles/app.css";
import "@fonts";
import "./bench.css";

import {
  CATALOGUE, CTX_TARGET, CTX_TITLE, CUSTOM, FONTVAR, GENERIC, INSTALLED_PROBE,
  MONO_REF, PALETTES, PROSE_SEL, REFERENCE, REFERENCE_GROUP, ROLES, TARGET_NOUN, TOK, TRIMS,
  WNAME, type Role, type TrimKey,
} from "./data.ts";
import {
  baseWeight, familyList, googleFontsHref, licBadge, licOf, monoBase, remoteQuery,
  setTrim, snapWeight, solveMatch, trimOf, trimReadout, weightFor,
  weightOptionsFor, wKey, type TrimScope, type TrimStore,
} from "./logic.ts";
import { pageHtml, paneHtml } from "./markup.ts";

interface Family {
  name: string;
  group: string;
  lic: string;
}

const PANE_COUNT = 3;

class Bench {
  private root: HTMLElement;
  private ctx: string;
  private panes: string[];
  private mode: string;
  private scale: string;
  private families: Family[] = [];
  private familySig = "";
  private loaded: Record<string, boolean> = {};
  private base: Record<string, string> = {};
  private custom: string[] = [];
  private targets: Record<string, string>;
  private trims: TrimStore = {};
  private fonts: Record<Role, string>;
  private installed: string[] = [];
  private focused = false;
  private activeSlider: HTMLInputElement | null = null;
  private raf = 0;
  private rafId = 0;
  private rafT: ReturnType<typeof setTimeout> | undefined;
  private scanT: ReturnType<typeof setTimeout> | undefined;
  private validFamily: (f: string) => boolean = () => false;

  constructor(root: HTMLElement) {
    this.root = root;
    root.innerHTML = pageHtml(
      Array.from({ length: PANE_COUNT }, (_, i) => paneHtml(i + 1)).join("\n"),
    );
    const defaults = [REFERENCE.prose, ...["Libre Baskerville", "Source Serif 4", "EB Garamond"].filter((n) => n !== REFERENCE.prose)];
    let saved: unknown = null;
    try { saved = JSON.parse(this.get("panes", "null")); } catch { saved = null; }
    const panes = Array.isArray(saved) ? (saved as string[]).slice(0, PANE_COUNT) : [];
    while (panes.length < PANE_COUNT) panes.push(defaults[panes.length] ?? REFERENCE.prose);
    this.panes = panes;
    this.ctx = this.get("ctx", "answer");
    this.mode = this.get("mode", "showcase");
    this.scale = this.get("scale", "1");
    try { this.custom = (JSON.parse(this.get("custom", "[]")) as string[]) || []; } catch { this.custom = []; }
    this.targets = { ...CTX_TARGET };
    try { Object.assign(this.targets, JSON.parse(this.get("targets", "{}")) || {}); } catch { /* fresh */ }
    // Trims are SESSION-scoped by design: picks, mode and palette persist;
    // trims do not survive the tab.
    try { this.trims = (JSON.parse(sessionStorage.getItem("bp.fontsub.trims") || "{}") as TrimStore) || {}; } catch { this.trims = {}; }
    this.fonts = { display: "", ui: "", prose: "", mono: "" };
    ROLES.forEach((r) => { this.fonts[r] = this.get("font." + r, REFERENCE[r]); });
  }

  private get(k: string, d: string): string {
    try {
      const v = localStorage.getItem("bp.fontsub." + k);
      return v === null ? d : v;
    } catch { return d; }
  }
  private set(k: string, v: string): void {
    try { localStorage.setItem("bp.fontsub." + k, v); } catch { /* storage denied */ }
  }
  private q<T extends HTMLElement = HTMLElement>(sel: string): T | null {
    return this.root.querySelector<T>(sel);
  }
  private qa<T extends HTMLElement = HTMLElement>(sel: string): T[] {
    return Array.from(this.root.querySelectorAll<T>(sel));
  }

  /* ---------- webfont loading ---------- */
  private loadFamily(name: string, query?: string): void {
    const key = name.toLowerCase();
    if (this.loaded[key]) return;
    this.loaded[key] = true;
    const link = document.createElement("link");
    link.rel = "stylesheet";
    link.href = googleFontsHref(name, query);
    // A wrong query 400s; the fallback is the bare family rather than
    // loading nothing — worst case Regular only.
    link.onerror = () => {
      const plain = document.createElement("link");
      plain.rel = "stylesheet";
      plain.href = googleFontsHref(name);
      document.head.appendChild(plain);
    };
    document.head.appendChild(link);
  }
  private ensureFont(name: string): void {
    const query = remoteQuery(name, Object.values(REFERENCE));
    if (query !== undefined) this.loadFamily((name || "").trim(), query);
  }

  /* ---------- families list ---------- */
  private rebuildFamilies(installed?: string[]): void {
    if (installed) this.installed = installed;
    const list: Family[] = familyList(
      ROLES.map((r) => REFERENCE[r]), REFERENCE_GROUP,
      CATALOGUE_SORTED, this.installed, this.custom,
      (name) => licOf(name, () => true),
    );
    this.families = list;
    this.familySig = list.length + ":" + this.installed.length + ":" + this.custom.length;
    this.syncRoleInputs();
    this.qa<HTMLSelectElement>("[data-pane-font]").forEach((sel, i) => this.fillFontSelect(sel, this.panes[i]));
  }
  private addCustom(name: string): void {
    if (!name) return;
    const k = name.toLowerCase();
    if (this.families.some((x) => x.name.toLowerCase() === k)) return;
    if (this.custom.some((x) => x.toLowerCase() === k)) return;
    this.custom = this.custom.concat([name]);
    this.set("custom", JSON.stringify(this.custom));
    this.rebuildFamilies();
  }

  /* ---------- trims ---------- */
  private setTrimValue(font: string | null, key: string, v: string, scope: TrimScope): void {
    if (!font) return;
    this.trims = setTrim(this.trims, font, key, v, scope);
    try { sessionStorage.setItem("bp.fontsub.trims", JSON.stringify(this.trims)); } catch { /* session denied */ }
    this.queueApply();
  }
  // Dragging fires input at pointer rate; writing ~40 token calc() strings
  // per event made the sliders sticky in the prototype. Coalesce per frame,
  // with a timer floor for throttled or hidden frames.
  private queueApply(): void {
    if (this.raf) return;
    this.raf = 1;
    const run = () => {
      if (!this.raf) return;
      this.raf = 0;
      cancelAnimationFrame(this.rafId);
      clearTimeout(this.rafT);
      this.applyAll();
    };
    this.rafId = requestAnimationFrame(run);
    this.rafT = setTimeout(run, 90);
  }

  /* ---------- token maths ---------- */
  private readBase(): void {
    const cs = getComputedStyle(document.documentElement);
    const all: Record<string, string> = {};
    ROLES.forEach((r) => (["s", "l", "t"] as TrimKey[]).forEach((k) => TOK[r][k].forEach((tok) => {
      all[tok] = (cs.getPropertyValue(tok) || "").trim();
    })));
    all["--tc-prose-ls"] = "0em";
    this.base = all;
  }
  // `scope` is the container being written, not the current mode: applyAll
  // writes the showcase and the compare panes in one pass, and they must not
  // read each other's numbers. The showcase shows the system's real tokens
  // with one face swapped in; the mono reference belongs to compare.
  private writeRole(el: HTMLElement, role: Role, font: string, scale: number | null, scope: TrimScope): void {
    const sMul = parseFloat(trimOf(this.trims, font, "s", scope)) * (scale == null ? parseFloat(this.scale) : scale);
    const lMul = parseFloat(trimOf(this.trims, font, "l", scope));
    const tAdd = parseFloat(trimOf(this.trims, font, "t", scope));
    const useRef = scope === "compare" && role === "mono";
    const bt = (tok: string) => (useRef && MONO_REF[tok]) || this.base[tok];
    TOK[role].s.forEach((tok) => { if (bt(tok)) el.style.setProperty(tok, "calc(" + bt(tok) + " * " + sMul + ")"); });
    TOK[role].l.forEach((tok) => { if (bt(tok)) el.style.setProperty(tok, "calc(" + bt(tok) + " * " + lMul + ")"); });
    TOK[role].t.forEach((tok) => { if (bt(tok)) el.style.setProperty(tok, "calc(" + bt(tok) + " + " + tAdd + "em)"); });
    // The substitution mechanism: re-declare the role's font VARIABLE on the
    // container; every class inside re-sets with zero per-element rules.
    el.style.setProperty(FONTVAR[role], '"' + font + '", ' + GENERIC[role]);
    // The picked weight applies to the role's primary token; the rest of the
    // ramp moves by the same interval and snaps to the face's real cuts.
    const shift = weightFor(this.trims, role, font) - baseWeight(role, font);
    const w = (n: number) => String(snapWeight(font, n + shift));
    if (role === "display") el.style.setProperty("--weight-light", w(300));
    else if (role === "ui") {
      // Only elements that read a weight token move with the tokens; plain
      // body text inherits, so the container carries the shifted regular.
      el.style.fontWeight = w(400);
      el.style.setProperty("--weight-regular", w(400));
      el.style.setProperty("--weight-medium", w(500));
      el.style.setProperty("--weight-semibold", w(600));
      el.style.setProperty("--weight-bold", w(700));
    } else if (role === "mono") {
      // The cursive sits below the code weight so a comment recedes.
      const mb = monoBase(font);
      el.style.setProperty("--type-mono-weight", w(mb));
      el.style.setProperty("--type-mono-cursive-weight", w(Math.min(mb, 400)));
    } else {
      // Prose takes its weight from the element default, not a token, so it
      // is written on the prose blocks themselves.
      const pw = w(400);
      el.querySelectorAll<HTMLElement>(PROSE_SEL).forEach((node) => { node.style.fontWeight = pw; });
    }
  }
  private applyAll(): void {
    if (!this.base["--type-body-size"]) return;
    const show = this.q("[data-showcase]");
    if (show) ROLES.forEach((r) => this.writeRole(show, r, this.fonts[r], null, "showcase"));
    const t = this.target();
    this.qa("[data-pane]").forEach((pane, i) => {
      const font = this.panes[i];
      if (!font) return;
      // The substituted ramp goes on the SPECIMEN, not the pane root: the
      // pane root is also the ancestor of the pane's own header, and writing
      // --type-*-size there scales the tool's labels with the candidate.
      const spec = pane.querySelector<HTMLElement>("[data-panebody]") ?? pane;
      ROLES.forEach((r) => {
        const on = t === "all" || t === r;
        this.writeRole(spec, r, on ? font : this.fonts[r], on ? parseFloat(this.scale) : 1, "compare");
      });
    });
    this.syncSliders();
    this.syncTopbar();
  }
  private syncSliders(): void {
    this.qa<HTMLInputElement>("[data-trim]").forEach((el) => {
      const spec = TRIMS.find((x) => x.key === el.dataset.trim);
      if (!spec) return;
      // Rewriting min/max/step every frame re-clamps the value mid-drag.
      if (el.min !== spec.min) el.min = spec.min;
      if (el.max !== spec.max) el.max = spec.max;
      if (el.step !== spec.step) el.step = spec.step;
      const font = this.fontForControl(el);
      const v = font === null ? spec.def : trimOf(this.trims, font, spec.key, this.scopeForControl(el));
      const live = el === this.activeSlider || document.activeElement === el;
      if (!live && el.value !== v) el.value = v;
      this.paintOut(el);
    });
    this.syncWeightSelects();
  }
  private paintOut(el: HTMLInputElement): void {
    const out = el.nextElementSibling;
    if (!out || !out.hasAttribute("data-out")) return;
    out.textContent = trimReadout(el.dataset.trim as TrimKey, parseFloat(el.value));
  }
  // A control's scope is where it lives, never the current mode: a showcase
  // role slider read against the mode picked up compare's value and showed
  // 115% over type set at 100%.
  private scopeForControl(el: HTMLElement): TrimScope {
    return el.closest("[data-pane]") ? "compare" : "showcase";
  }
  private fontForControl(el: HTMLElement): string | null {
    const role = el.dataset.role as Role | undefined;
    if (role) return this.fonts[role];
    const pane = el.closest("[data-pane]");
    if (!pane) return null;
    return this.panes[this.qa("[data-pane]").indexOf(pane as HTMLElement)] ?? null;
  }

  /* ---------- role fonts ---------- */
  private setRoleFont(role: Role, name: string): void {
    const v = (name || "").trim();
    if (!v) return;
    this.fonts[role] = v;
    this.ensureFont(v);
    this.set("font." + role, v);
    const label = this.q('[data-fname="' + role + '"]');
    if (label) label.textContent = v;
    this.paintLic(role, v);
    if (this.validFamily(v)) this.addCustom(v);
    this.applyAll();
  }
  private paintLic(role: Role, name: string): void {
    const badge = licBadge(licOf(name, this.validFamily));
    this.qa('[data-lic="' + role + '"]').forEach((b) => {
      b.className = badge.className;
      b.textContent = badge.text;
    });
  }

  /* ---------- panes ---------- */
  private syncPanes(): void {
    this.qa<HTMLSelectElement>("[data-pane] [data-pane-font]").forEach((sel, i) => this.fillFontSelect(sel, this.panes[i]));
    this.qa("[data-pane]").forEach((pane, i) => {
      const badge = licBadge(licOf(this.panes[i] ?? "", this.validFamily));
      const b = pane.querySelector<HTMLElement>("[data-pane-lic]");
      if (b) { b.className = badge.className; b.textContent = badge.text; }
      pane.querySelectorAll<HTMLElement>("[data-when]").forEach((sec) => {
        sec.style.display = sec.dataset.when === this.ctx ? "" : "none";
      });
    });
    this.fillWeightSelects();
    this.panes.forEach((f) => this.ensureFont(f));
    const tgt = this.target();
    const sel = this.q<HTMLSelectElement>("[data-target]");
    if (sel && sel.value !== tgt) sel.value = tgt;
    const t = this.q("[data-cmptitle]");
    if (t) t.textContent = "Three " + TARGET_NOUN[tgt] + ", " + CTX_TITLE[this.ctx];
    this.applyAll();
  }
  private target(): string {
    return this.targets[this.ctx] || "all";
  }
  private setTarget(v: string): void {
    this.targets[this.ctx] = v;
    this.set("targets", JSON.stringify(this.targets));
    this.syncPanes();
  }
  private setPane(i: number, name: string): void {
    const v = (name || "").trim();
    if (!v || i < 0) return;
    this.panes = this.panes.slice();
    this.panes[i] = v;
    this.ensureFont(v);
    if (this.validFamily(v)) this.addCustom(v);
    this.set("panes", JSON.stringify(this.panes));
    this.syncPanes();
  }

  /* ---------- font + weight selects ---------- */
  // Native <select>: the OS owns scrolling, keyboard and type-ahead.
  private fillFontSelect(sel: HTMLSelectElement | null, cur: string | undefined): void {
    if (!sel) return;
    const want = cur || "";
    if (sel.options.length && sel.dataset.sig === this.familySig + "|" + want) {
      if (sel.value !== want) sel.value = want;
      return;
    }
    sel.dataset.sig = this.familySig + "|" + want;
    sel.innerHTML = "";
    let group: string | null = null;
    let og: HTMLOptGroupElement | null = null;
    let found = false;
    this.families.forEach((f) => {
      if (f.group !== group) {
        group = f.group;
        og = document.createElement("optgroup");
        og.label = group;
        sel.appendChild(og);
      }
      const o = document.createElement("option");
      o.value = f.name;
      o.textContent = f.name + (f.lic === "ofl" || f.lic === "apache" ? "  · free" : f.lic === "system" ? "  · installed" : "");
      if (f.name.toLowerCase() === want.toLowerCase()) found = true;
      og?.appendChild(o);
    });
    if (want && !found) {
      const o = document.createElement("option");
      o.value = want;
      o.textContent = want + "  · unverified";
      sel.insertBefore(o, sel.firstChild);
    }
    const c = document.createElement("option");
    c.value = CUSTOM;
    c.textContent = "Type a name…";
    sel.appendChild(c);
    sel.value = want;
  }
  private roleForControl(el: HTMLElement): Role {
    const role = el.dataset.role as Role | undefined;
    if (role) return role;
    const t = this.targets[this.ctx] || CTX_TARGET[this.ctx];
    if (t && t !== "all") return t as Role;
    // Substituting every role still leaves one voice carrying the context:
    // code in the machine view, chrome in the specimen sheet.
    return this.ctx === "specimen" ? "ui" : "mono";
  }
  private fillWeightSelects(): void {
    this.qa<HTMLSelectElement>("[data-wsel]").forEach((sel) => {
      const font = this.fontForControl(sel);
      if (!font) return;
      const role = this.roleForControl(sel);
      const opts = weightOptionsFor(font);
      const sig = font + "|" + role + "|" + opts.join(",");
      if (sel.dataset.wsig === sig) return;
      sel.dataset.wsig = sig;
      sel.innerHTML = "";
      const base = baseWeight(role, font);
      opts.forEach((v) => {
        const o = document.createElement("option");
        o.value = String(v);
        o.textContent = v + (WNAME[v] ? "  " + WNAME[v] : "") + (v === base ? "  · own" : "");
        sel.appendChild(o);
      });
    });
  }
  private syncWeightSelects(): void {
    this.fillWeightSelects();
    this.qa<HTMLSelectElement>("[data-wsel]").forEach((sel) => {
      const font = this.fontForControl(sel);
      if (!sel.options.length || !font) return;
      const v = String(weightFor(this.trims, this.roleForControl(sel), font));
      if (sel.value !== v) sel.value = v;
    });
  }
  private paneIndexOf(el: HTMLElement): number {
    const p = el.closest("[data-pane]");
    return p ? this.qa("[data-pane]").indexOf(p as HTMLElement) : -1;
  }
  private commitInput(el: HTMLSelectElement, value?: string): void {
    const v = value == null ? el.value : value;
    const role = el.dataset.font as Role | undefined;
    if (role) this.setRoleFont(role, v);
    else this.setPane(this.paneIndexOf(el), v);
  }
  private pickCustom(el: HTMLSelectElement): void {
    const role = el.dataset.font as Role | undefined;
    const cur = role ? this.fonts[role] : this.panes[this.paneIndexOf(el)];
    const name = (window.prompt("Font family name — any face installed on this machine works.", cur || "") || "").trim();
    if (!name) { this.fillFontSelect(el, cur); return; }
    this.addCustom(name);
    this.commitInput(el, name);
  }

  /* ---------- detection + measurement ---------- */
  private makeProbe(): void {
    const ctx = document.createElement("canvas").getContext("2d");
    if (!ctx) return;
    const SAMPLE = "mmMwlIi10O@# abcXYZ";
    const width = (stack: string) => { ctx.font = "32px " + stack; return ctx.measureText(SAMPLE).width; };
    const base: Record<string, number> = { monospace: width("monospace"), serif: width("serif"), "sans-serif": width("sans-serif") };
    this.validFamily = (f: string) => {
      if (!f) return false;
      const nm = JSON.stringify(String(f));
      return Object.keys(base).some((g) => width(nm + ", " + g) !== base[g]);
    };
  }
  // Ink extent per em for a probe glyph, measured at 520px and divided back.
  // A single-pixel read at the real size rounds every face together, and the
  // metrics exposed to JS do not include x-height at all.
  private inkExtentPerEm(font: string, ch: string): number {
    const px = 520;
    const cv = document.createElement("canvas");
    cv.width = px * (ch.length + 2);
    cv.height = px * 3;
    const cx = cv.getContext("2d", { willReadFrequently: true });
    if (!cx) return 0;
    cx.font = '400 ' + px + 'px "' + font + '", monospace';
    cx.textBaseline = "alphabetic";
    cx.fillStyle = "#000";
    cx.fillText(ch, px * 0.4, px * 2);
    const d = cx.getImageData(0, 0, cv.width, cv.height).data;
    let top = -1;
    let bot = -1;
    for (let y = 0; y < cv.height; y++) {
      for (let x = 0; x < cv.width; x++) {
        if ((d[(y * cv.width + x) * 4 + 3] ?? 0) > 12) { if (top < 0) top = y; bot = y; break; }
      }
    }
    return top < 0 ? 0 : (bot - top + 1) / px;
  }
  // Advance width per em; weight-invariant in a monospace, so 400 is safe.
  private advancePerEm(font: string): number {
    const cx = document.createElement("canvas").getContext("2d");
    if (!cx) return 0;
    cx.font = '400 520px "' + font + '", monospace';
    return cx.measureText("0".repeat(60)).width / 60 / 520;
  }
  // The load-bearing measurement gate: every face is loaded per weight/size
  // pair before ANY measurement, then asserted real by measuring against the
  // serif fallback — identical widths at every generic means nothing loaded,
  // and a face that did not load must REFUSE to report numbers rather than
  // report the fallback's.
  private async assertLoaded(font: string): Promise<boolean> {
    const weight = weightFor(this.trims, "mono", font);
    const refSize = parseFloat(MONO_REF["--type-mono-size"] ?? "13");
    const pairs = [
      weight + ' 520px "' + font + '"',
      weight + " " + refSize + 'px "' + font + '"',
    ];
    try {
      await Promise.all(pairs.map((p) => document.fonts.load(p)));
    } catch { /* an unparseable face name reads as unloaded below */ }
    return this.validFamily(font);
  }
  // Matches every pane to the reference face's anchor glyph. x-height
  // matches how large the text READS; full height anchors on the digit 8
  // deliberately — no descender, no cap-versus-ascender disagreement.
  private async matchMetric(kind: string): Promise<void> {
    const ch = kind === "full" ? "8" : "x";
    const refFont = REFERENCE.mono;
    this.qa("[data-match]").forEach((b) => b.classList.toggle("bp-tab--selected", b.dataset.match === kind));
    if (!(await this.assertLoaded(refFont))) {
      this.say("The reference face did not load — no numbers reported.");
      return;
    }
    const ref = { inkPerEm: this.inkExtentPerEm(refFont, ch), advancePerEm: this.advancePerEm(refFont) };
    const refused: string[] = [];
    for (const font of this.panes) {
      if (!(await this.assertLoaded(font))) {
        refused.push(font);
        continue;
      }
      const solved = solveMatch(ref, { inkPerEm: this.inkExtentPerEm(font, ch), advancePerEm: this.advancePerEm(font) });
      if (solved === null) {
        refused.push(font);
        continue;
      }
      this.trims = setTrim(this.trims, font, "s", solved.s, "compare");
      this.trims = setTrim(this.trims, font, "l", solved.l, "compare");
      this.trims = setTrim(this.trims, font, "t", solved.t, "compare");
    }
    try { sessionStorage.setItem("bp.fontsub.trims", JSON.stringify(this.trims)); } catch { /* session denied */ }
    this.say(refused.length === 0 ? "" : refused.join(", ") + " did not load — no numbers reported.");
    this.syncSliders();
    this.queueApply();
  }
  private say(text: string): void {
    const note = this.q("[data-note]");
    if (note) note.textContent = text;
  }
  private detectInstalled(): number {
    const found = INSTALLED_PROBE.filter((f) => { try { return this.validFamily(f); } catch { return false; } });
    this.rebuildFamilies(Array.from(new Set(this.installed.concat(found))));
    return found.length;
  }
  // The full local listing needs the Local Font Access API and a permitting
  // frame; the canvas probe is the path most people hit, and it only sees
  // names it already knows — hence the long probe list.
  private async scanFonts(): Promise<void> {
    const btn = this.q<HTMLButtonElement>("[data-scan]");
    const settle = (n: number) => {
      if (!btn) return;
      btn.textContent = n + " found";
      clearTimeout(this.scanT);
      this.scanT = setTimeout(() => { btn.textContent = "Scan installed"; }, 3000);
    };
    if (btn) btn.textContent = "Scanning…";
    let fams: string[] | null = null;
    let why = "";
    const queryLocalFonts = (window as { queryLocalFonts?: () => Promise<{ family: string }[]> }).queryLocalFonts;
    if (typeof queryLocalFonts === "function") {
      try {
        const fonts = await queryLocalFonts();
        fams = Array.from(new Set(fonts.map((f) => f.family))).filter(Boolean);
      } catch (e) {
        why = e instanceof DOMException && e.name === "SecurityError"
          ? "The full listing is blocked in this frame — "
          : "Permission declined — ";
      }
    } else {
      why = "The full listing needs Chrome or Edge — ";
    }
    if (fams && fams.length) {
      this.rebuildFamilies(fams);
      this.set("installed", JSON.stringify(fams.slice(0, 1200)));
      this.say(fams.length + " local families loaded, under “On this machine”.");
      settle(fams.length);
      return;
    }
    const n = this.detectInstalled();
    this.set("installed", JSON.stringify(this.installed));
    this.say(why + n + " of " + INSTALLED_PROBE.length + " known faces found here. Anything else: pick “Type a name…”.");
    settle(n);
  }

  /* ---------- chrome ---------- */
  private syncRoleInputs(): void {
    ROLES.forEach((r) => {
      const sel = this.q<HTMLSelectElement>('[data-font="' + r + '"]');
      if (sel) this.fillFontSelect(sel, this.fonts[r]);
      const label = this.q('[data-fname="' + r + '"]');
      if (label) label.textContent = this.fonts[r];
      this.paintLic(r, this.fonts[r]);
    });
    this.fillWeightSelects();
    this.syncWeightSelects();
    this.syncCtxSeg();
  }
  // The context strip is painted from state, not only by the click that
  // changes it: ctx is restored from storage on load.
  private syncCtxSeg(): void {
    this.qa("[data-ctx]").forEach((b) => b.classList.toggle("bp-tab--selected", b.dataset.ctx === this.ctx));
  }
  private setMode(m: string): void {
    this.mode = m;
    this.set("mode", m);
    const show = this.q("[data-showcase]");
    const cmp = this.q("[data-compare]");
    const rail = this.q("[data-rail]");
    if (show) show.style.display = m === "compare" ? "none" : "";
    if (cmp) cmp.style.display = m === "compare" ? "" : "none";
    if (rail) rail.style.display = m === "compare" ? "none" : "";
    this.qa("[data-mode]").forEach((b) => b.classList.toggle("bp-tab--selected", b.dataset.mode === m));
    const ctls = this.q("[data-cmpctls]");
    if (ctls) ctls.style.display = m === "compare" ? "flex" : "none";
    this.syncTopbar();
    if (m === "compare") this.syncPanes();
  }
  private setTheme(dark: boolean): void {
    document.documentElement.setAttribute("data-theme", dark ? "dark" : "light");
    document.documentElement.style.colorScheme = dark ? "dark" : "light";
    this.set("dark", dark ? "1" : "0");
    const box = this.q<HTMLInputElement>("[data-dark]");
    if (box) box.checked = dark;
  }
  // The prototype loaded per-palette stylesheets; Deck carries none,
  // so the palettes ship as attribute-scoped accent blocks in bench.css and
  // the pick is an attribute swap with the same persisted key.
  private setPalette(name: string): void {
    this.set("palette", name);
    const sel = this.q<HTMLSelectElement>("[data-palette]");
    if (sel && sel.value !== name) sel.value = name;
    document.documentElement.setAttribute("data-bench-palette", name);
  }
  private setScale(v: string): void {
    this.scale = String(v);
    this.set("scale", this.scale);
    const s = this.q<HTMLInputElement>("[data-scale]");
    if (s && s.value !== this.scale && document.activeElement !== s) s.value = this.scale;
    const out = this.q("[data-scaleout]");
    if (out) out.textContent = Math.round(parseFloat(this.scale) * 100) + "%";
    this.applyAll();
  }
  private setFocus(on: boolean): void {
    this.focused = on;
    const bar = this.q("[data-topbar]");
    const rail = this.q("[data-rail]");
    const frame = this.q("[data-frame]");
    const pill = this.q("[data-focuspill]");
    if (bar) { bar.style.transform = on ? "translateY(-100%)" : ""; bar.style.opacity = on ? "0" : ""; bar.style.pointerEvents = on ? "none" : ""; }
    if (rail && this.mode !== "compare") { rail.style.width = on ? "0px" : "304px"; rail.style.paddingLeft = on ? "0px" : ""; rail.style.paddingRight = on ? "0px" : ""; rail.style.opacity = on ? "0" : ""; rail.style.overflow = on ? "hidden" : "auto"; }
    if (frame) frame.style.paddingTop = on ? "28px" : "";
    if (pill) pill.style.display = on ? "inline-flex" : "none";
  }
  private syncTopbar(): void {
    const bar = this.q("[data-topbar]");
    const app = this.q("[data-app]");
    if (!bar || !app) return;
    const h = bar.offsetHeight;
    app.style.setProperty("--tc-topbar", h + "px");
    document.documentElement.style.scrollPaddingTop = h + 22 + "px";
  }

  /* ---------- lifecycle ---------- */
  mount(): void {
    this.makeProbe();
    document.documentElement.style.scrollBehavior = "smooth";
    const app = this.q("[data-app]");
    const cs = getComputedStyle(document.documentElement);
    // The tool's own chrome is PINNED to the licensed UI face via
    // --tc-chrome, so substitution never re-sets the controls.
    app?.style.setProperty("--tc-chrome", (cs.getPropertyValue("--font-ui") || "").trim());
    this.readBase();
    this.setPalette(this.get("palette", "cobalt"));
    this.setTheme(this.get("dark", "0") === "1");
    this.setScale(this.get("scale", "1"));
    ROLES.forEach((r) => this.setRoleFont(r, this.fonts[r]));
    let seen: string[] = [];
    try { seen = (JSON.parse(this.get("installed", "[]")) as string[]) || []; } catch { seen = []; }
    this.rebuildFamilies(seen);
    this.syncRoleInputs();
    // A scan survives reload, but re-probe anyway: fonts come and go.
    const done = () => { this.detectInstalled(); this.applyAll(); };
    if (document.fonts?.ready) void document.fonts.ready.then(done);
    else done();
    this.setMode(this.mode);
    this.syncPanes();
    this.syncTopbar();
    this.bindEvents();
  }

  private bindEvents(): void {
    const root = this.root;
    root.addEventListener("input", (e) => {
      const t = e.target as HTMLElement;
      if (t.matches("[data-trim]")) {
        const el = t as HTMLInputElement;
        this.activeSlider = el;
        this.paintOut(el);
        this.setTrimValue(this.fontForControl(el), el.dataset.trim ?? "", el.value, this.scopeForControl(el));
      } else if (t.matches("[data-scale]")) this.setScale((t as HTMLInputElement).value);
    });
    root.addEventListener("change", (e) => {
      const t = e.target as HTMLElement;
      if (t.matches("[data-font], [data-pane-font]")) {
        const sel = t as HTMLSelectElement;
        if (sel.value === CUSTOM) this.pickCustom(sel);
        else this.commitInput(sel);
      } else if (t.matches("[data-wsel]")) {
        // Only a real absolute weight is written: a blank or out-of-range
        // value would persist as a pick and make the face's own weight
        // unreachable without re-picking it.
        const sel = t as HTMLSelectElement;
        const abs = parseInt(sel.value, 10);
        if (abs >= 100) this.setTrimValue(this.fontForControl(sel), wKey(this.roleForControl(sel)), String(abs), this.scopeForControl(sel));
      } else if (t.matches("[data-target]")) this.setTarget((t as HTMLSelectElement).value);
      else if (t.matches("[data-palette]")) this.setPalette((t as HTMLSelectElement).value);
      else if (t.matches("[data-dark]")) this.setTheme((t as HTMLInputElement).checked);
    });
    root.addEventListener("dblclick", (e) => {
      const t = e.target as HTMLElement;
      if (t.matches("[data-trim]")) {
        const el = t as HTMLInputElement;
        const d = TRIMS.find((x) => x.key === el.dataset.trim)?.def ?? "";
        this.setTrimValue(this.fontForControl(el), el.dataset.trim ?? "", d, this.scopeForControl(el));
        el.value = d;
        this.paintOut(el);
      } else if (t.matches("[data-scale]")) this.setScale("1");
    });
    // A slider keeps ownership of its value until the pointer is released,
    // so a re-sync mid-drag cannot snap the thumb back under the finger.
    root.addEventListener("pointerdown", (e) => {
      const t = e.target as HTMLElement;
      if (t.matches?.("[data-trim]")) this.activeSlider = t as HTMLInputElement;
    });
    const releaseSlider = () => { this.activeSlider = null; };
    document.addEventListener("pointerup", releaseSlider);
    document.addEventListener("pointercancel", releaseSlider);
    root.addEventListener("click", (e) => {
      const target = e.target as HTMLElement;
      const mode = target.closest<HTMLElement>("[data-mode]");
      if (mode) return this.setMode(mode.dataset.mode ?? "showcase");
      const ctx = target.closest<HTMLElement>("[data-ctx]");
      if (ctx) {
        this.ctx = ctx.dataset.ctx ?? "answer";
        this.set("ctx", this.ctx);
        this.syncCtxSeg();
        return this.syncPanes();
      }
      const mtch = target.closest<HTMLElement>("[data-match]");
      if (mtch) { void this.matchMetric(mtch.dataset.match ?? "x"); return; }
      if (target.closest("[data-scan]")) { void this.scanFonts(); return; }
      if (target.closest("[data-focus]")) return this.setFocus(!this.focused);
      if (target.closest("[data-focuspill]")) return this.setFocus(false);
      if (target.closest("[data-reset]")) {
        ROLES.forEach((r) => this.setRoleFont(r, REFERENCE[r]));
        this.syncRoleInputs();
        return;
      }
      const sec = target.closest<HTMLElement>("[data-sec]");
      if (sec) this.qa("[data-sec]").forEach((a) => a.classList.toggle("bp-menuitem--selected", a === sec));
    });
    document.addEventListener("keydown", (e) => {
      const t = e.target as HTMLElement;
      if (t.tagName === "INPUT" || t.tagName === "SELECT" || t.isContentEditable) {
        if (e.key === "Escape") t.blur();
        return;
      }
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      if (e.key === "f" || e.key === "F") { e.preventDefault(); this.setFocus(!this.focused); }
      else if (e.key === "Escape") this.setFocus(false);
      else if (e.key === "d" || e.key === "D") this.setTheme(document.documentElement.getAttribute("data-theme") !== "dark");
      else if (e.key >= "1" && e.key <= "5") this.setPalette(PALETTES[Number(e.key) - 1] ?? "cobalt");
    });
    this.bindDrag();
    window.addEventListener("resize", () => { this.readBase(); this.applyAll(); this.syncTopbar(); });
    const bar = this.q("[data-topbar]");
    if (window.ResizeObserver && bar) new ResizeObserver(() => this.syncTopbar()).observe(bar);
    const spy = new IntersectionObserver((entries) => {
      entries.forEach((en) => {
        if (!en.isIntersecting) return;
        this.qa("[data-sec]").forEach((a) => a.classList.toggle("bp-menuitem--selected", a.dataset.sec === en.target.id));
      });
    }, { rootMargin: "-20% 0px -70% 0px" });
    this.qa("[data-showcase] section").forEach((s) => spy.observe(s));
  }

  private dragFrom: number | null = null;
  private ghost: HTMLElement | null = null;
  private bindDrag(): void {
    const root = this.root;
    root.addEventListener("dragstart", (e) => {
      const h = (e.target as HTMLElement).closest("[data-drag]");
      if (!h) return;
      const pane = h.closest("[data-pane]") as HTMLElement;
      this.dragFrom = this.qa("[data-pane]").indexOf(pane);
      e.dataTransfer!.effectAllowed = "move";
      e.dataTransfer!.setData("text/plain", String(this.dragFrom));
      // What you are moving is the card, so the card follows the cursor —
      // not the little number chip that happens to be draggable.
      const r = pane.getBoundingClientRect();
      const ghost = pane.cloneNode(true) as HTMLElement;
      ghost.style.cssText = "position:fixed;top:0;left:-10000px;width:" + Math.round(r.width) +
        "px;max-height:280px;overflow:hidden;background:var(--surface-overlay);box-shadow:var(--ring-overlay);pointer-events:none";
      document.body.appendChild(ghost);
      this.ghost = ghost;
      e.dataTransfer!.setDragImage(ghost, Math.min(r.width / 2, 150), 24);
      setTimeout(() => { pane.style.opacity = ".45"; }, 0);
    });
    const endDrag = () => {
      this.ghost?.remove();
      this.ghost = null;
      this.qa("[data-pane]").forEach((p) => { p.style.opacity = ""; p.style.outline = ""; });
    };
    root.addEventListener("dragend", endDrag);
    root.addEventListener("dragover", (e) => {
      const pane = (e.target as HTMLElement).closest?.("[data-pane]") as HTMLElement | null;
      if (!pane || this.dragFrom == null) return;
      e.preventDefault();
      e.dataTransfer!.dropEffect = "move";
      this.qa("[data-pane]").forEach((p) => { p.style.outline = p === pane && p.style.opacity !== ".45" ? "2px dashed var(--accent)" : ""; });
    });
    root.addEventListener("drop", (e) => {
      const pane = (e.target as HTMLElement).closest?.("[data-pane]") as HTMLElement | null;
      if (!pane || this.dragFrom == null) return;
      e.preventDefault();
      const to = this.qa("[data-pane]").indexOf(pane);
      const from = this.dragFrom;
      this.dragFrom = null;
      endDrag();
      if (to === from || to < 0) return;
      const panes = this.panes.slice();
      const [moved] = panes.splice(from, 1);
      if (moved === undefined) return;
      panes.splice(to, 0, moved);
      this.panes = panes;
      this.set("panes", JSON.stringify(panes));
      this.syncPanes();
    });
  }
}

const CATALOGUE_SORTED = [...CATALOGUE]
  .sort((a, b) => a.n.localeCompare(b.n))
  .map((c) => ({ name: c.n, lic: c.lic }));

const mountPoint = document.getElementById("bench");
if (mountPoint) new Bench(mountPoint).mount();
