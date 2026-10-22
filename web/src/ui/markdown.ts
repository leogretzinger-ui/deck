// A deliberately small markdown renderer for task detail text.
//
// SAFETY MODEL: the input is untrusted. Task details come from
// tasks.json, which is written by the task engine from a PRD — user
// supplied, and for generated backlogs, model-written. So this escapes
// EVERY character of the source first and only then applies markdown to
// the already-escaped text. No path exists by which source markup can
// reach the DOM as markup: there is no "allowed tags" list to get wrong,
// because raw tags stopped being tags before any rule ran.
//
// Escaping first is necessary but NOT sufficient, and the two places it
// does not cover are the whole trust boundary:
//
//  1. A link's href is the one value that becomes an attribute, so
//     schemes are allowlisted rather than filtered — javascript:, data:
//     and anything unrecognised render as plain text.
//  2. The inline-code placeholder is expanded back into markup AFTER
//     the link rule has written its attribute, so a target that
//     swallows a placeholder would put raw < and " into attribute
//     position. Such links render as text, and NUL is stripped from the
//     source so the delimiter cannot be forged at all.
//
// The supported subset is what task text actually uses: fenced and
// inline code (the reason this exists — snippets were unreadable as one
// pre-wrapped blob), headings, bold, italic, lists, links, and
// paragraphs. Anything else passes through as text rather than being
// silently dropped.
import { escapeHtml } from "../lib/escape.ts";
import { findTaskRefs } from "./task-ref.ts";
import * as hints from "../lib/hints.ts";
import { INLINE_LINK_BUTTON } from "./controls.ts";
import { taskRefLinkHtml } from "./task-card.ts";

// SAFE_SCHEME matches the only link schemes that may become an anchor.
// Relative links are excluded too: there is nowhere meaningful for them
// to point from a task card.
const SAFE_SCHEME = /^https?:\/\//i;

// Tutorial lessons are bundled, reviewed files, never task text, so the
// lesson: and action: link schemes are opt-in. A caller passes the action
// names it will dispatch; those links become buttons carrying
// data-tutorial-action and never an href. Without the option, both schemes
// stay plain text like any other unrecognised target.
const LESSON_LINK = /^lesson:([a-z0-9-]{1,40})$/;
const ACTION_LINK = /^action:([a-z][a-z-]{0,30})$/;

// knownTaskIDs is deliberately caller-supplied: rendering never fetches or
// invents task identity, and absent ids remain plain text.
export function renderMarkdown(source: string, knownTaskIDs?: ReadonlySet<string>, tutorialActions?: ReadonlySet<string>): string {
  // NUL is stripped at the door, not assumed absent: the inline-code
  // placeholder below is delimited with it, and "\u0000" is legal JSON,
  // so task text really can carry one.
  const src = source.replace(/\u0000/g, "");
  if (src.trim() === "") return "";
  const blocks: string[] = [];
  // Fenced code is taken first and verbatim: inline rules must not run
  // inside a snippet, or an underscore in a variable name becomes
  // emphasis and the code stops being the code.
  for (const chunk of splitFences(src)) {
    if (chunk.fenced) {
      blocks.push(codeBlock(chunk.text, chunk.lang));
      continue;
    }
    for (const block of chunk.text.split(/\n{2,}/)) {
      const rendered = renderBlock(block, knownTaskIDs, tutorialActions);
      if (rendered !== "") blocks.push(rendered);
    }
  }
  return blocks.join("");
}

interface Chunk {
  text: string;
  fenced: boolean;
  lang: string;
}

// splitFences separates ``` blocks from prose. An unterminated fence
// runs to the end of the input and is still treated as code: the author
// clearly meant it as code, and rendering the remainder as prose would
// mangle it.
function splitFences(src: string): Chunk[] {
  const out: Chunk[] = [];
  const lines = src.split("\n");
  let buf: string[] = [];
  let code: string[] | null = null;
  let lang = "";
  const flushProse = (): void => {
    if (buf.length > 0) out.push({ text: buf.join("\n"), fenced: false, lang: "" });
    buf = [];
  };
  for (const line of lines) {
    const fence = /^\s*```(.*)$/.exec(line);
    if (fence !== null) {
      if (code === null) {
        flushProse();
        code = [];
        lang = fence[1]!.trim();
      } else {
        out.push({ text: code.join("\n"), fenced: true, lang });
        code = null;
        lang = "";
      }
      continue;
    }
    if (code !== null) code.push(line);
    else buf.push(line);
  }
  if (code !== null) out.push({ text: code.join("\n"), fenced: true, lang });
  flushProse();
  return out;
}

function codeBlock(text: string, lang: string): string {
  // The language is a class hint only, and is filtered to word
  // characters so it cannot break out of the attribute.
  const safeLang = /^[\w+-]{1,24}$/.test(lang) ? ` data-lang="${lang}"` : "";
  return `<pre${safeLang} class="my-2 overflow-x-auto rounded-control border border-border bg-surface-sunken p-2.5"><code class="font-mono text-micro leading-[1.5] text-text-secondary">${escapeHtml(text)}</code></pre>`;
}

function renderBlock(block: string, knownTaskIDs?: ReadonlySet<string>, tutorialActions?: ReadonlySet<string>): string {
  const trimmed = block.trim();
  if (trimmed === "") return "";

  const heading = /^(#{1,4})\s+(.*)$/.exec(trimmed);
  if (heading !== null) {
    return `<p class="pt-1 text-body-sm font-medium text-text-primary">${inline(heading[2]!, knownTaskIDs, tutorialActions)}</p>`;
  }

  const lines = trimmed.split("\n");
  if (lines.every((l) => /^\s*[-*+]\s+/.test(l))) {
    return list(lines.map((l) => l.replace(/^\s*[-*+]\s+/, "")), "ul", 1, knownTaskIDs, tutorialActions);
  }
  if (lines.every((l) => /^\s*\d+[.)]\s+/.test(l))) {
    return list(lines.map((l) => l.replace(/^\s*\d+[.)]\s+/, "")), "ol", firstOrdinal(lines[0]!), knownTaskIDs, tutorialActions);
  }

  // A single newline inside a paragraph is a wrap in the source, not a
  // break the author asked for.
  return `<p class="text-body-sm leading-relaxed text-text-secondary">${inline(lines.join(" "), knownTaskIDs, tutorialActions)}</p>`;
}

// firstOrdinal reads the number an ordered list actually starts at.
// Anything a browser would not honour as a `start` (0, or a run of digits
// long enough to be a typo rather than a step) falls back to 1.
function firstOrdinal(line: string): number {
  const m = /^\s*(\d{1,4})[.)]\s+/.exec(line);
  const n = m === null ? 1 : Number(m[1]);
  return n >= 1 ? n : 1;
}

function list(items: string[], tag: "ul" | "ol", start = 1, knownTaskIDs?: ReadonlySet<string>, tutorialActions?: ReadonlySet<string>): string {
  const marker = tag === "ul" ? "list-disc" : "list-decimal";
  // A fenced snippet between two steps ends the block, so the steps after
  // it are a SECOND <ol>, which numbers from 1 unless told otherwise. The
  // source's own numbering is what it is told.
  const from = tag === "ol" && start !== 1 ? ` start="${start}"` : "";
  const rendered = items.map((i) => `<li class="pl-0.5">${inline(i, knownTaskIDs, tutorialActions)}</li>`).join("");
  return `<${tag}${from} class="my-1 ${marker} space-y-0.5 pl-5 text-body-sm leading-relaxed text-text-secondary">${rendered}</${tag}>`;
}

// inline escapes first, then applies span-level rules to escaped text.
// Every branch below operates on text that can no longer contain markup.
function inline(raw: string, knownTaskIDs?: ReadonlySet<string>, tutorialActions?: ReadonlySet<string>): string {
  let out = escapeHtml(raw);

  // Inline code before emphasis, for the same reason fences come first.
  const codes: string[] = [];
  out = out.replace(/`([^`]+)`/g, (_m, body: string) => {
    codes.push(`<code class="rounded-mark bg-surface-muted px-1 py-px font-mono text-micro text-text-primary">${body}</code>`);
    // NUL-delimited: a bare index collides with ordinary prose —
    // "step 3 of 5" would lose its " 3 ". NUL cannot occur in the text.
    return `\u0000${codes.length - 1}\u0000`;
  });

  // Linkification runs only over escaped prose. Inline code is hidden behind
  // placeholders and markdown links are excluded below so generated markup
  // can never be scanned and rewritten as source text.
  if (knownTaskIDs !== undefined) {
    out = linkifyTaskRefs(out, knownTaskIDs);
  }

  out = out.replace(/\*\*([^*]+)\*\*/g, '<strong class="font-medium text-text-primary">$1</strong>');
  out = out.replace(/(^|[^*])\*([^*]+)\*/g, '$1<em>$2</em>');

  // Links last. The href is validated, not sanitised: an unrecognised
  // scheme renders as plain text so a bad URL can never become an
  // attribute value.
  out = out.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, (whole, label: string, href: string) => {
    // A code-span placeholder inside the target would be expanded back
    // into markup AFTER this attribute is written — raw < and " landing
    // in attribute position, which is the one thing escaping first does
    // not prevent. Such a link renders as text instead.
    if (href.includes("\u0000")) return whole;
    const url = href.replace(/&amp;/g, "&");
    if (tutorialActions !== undefined) {
      const lesson = LESSON_LINK.exec(url)?.[1];
      if (lesson !== undefined) return `<button type="button" data-tutorial-action="lesson" data-lesson="${lesson}" class="${INLINE_LINK_BUTTON}" data-hint="${hints.OPEN_THIS_LESSON_HINT}">${label}</button>`;
      const action = ACTION_LINK.exec(url)?.[1];
      if (action !== undefined) {
        return tutorialActions.has(action)
          ? `<button type="button" data-tutorial-action="${action}" class="${INLINE_LINK_BUTTON}" data-hint="${escapeHtml(hints.tutorialActionHint(action, null, null))}">${label}</button>`
          : whole;
      }
    }
    if (!SAFE_SCHEME.test(url)) return whole;
    return `<a href="${escapeHtml(url)}" target="_blank" rel="noopener noreferrer" class="text-accent underline underline-offset-2">${label}</a>`;
  });

  return out.replace(new RegExp("\u0000(\\d+)\u0000", "g"), (_m, i: string) => codes[Number(i)] ?? "");
}

function linkifyTaskRefs(escaped: string, known: ReadonlySet<string>): string {
  const refs = findTaskRefs(escaped);
  if (refs.length === 0) return escaped;
  const links = [...escaped.matchAll(/\[[^\]]+\]\([^)\s]+\)/g)].map((match) => ({
    start: match.index,
    end: match.index + match[0].length,
  }));
  let result = "";
  let cursor = 0;
  for (const ref of refs) {
    if (!known.has(ref.id)) continue;
    if (links.some((link) => ref.start < link.end && ref.end > link.start)) continue;
    result += escaped.slice(cursor, ref.start);
    result += taskRefLinkHtml(escaped.slice(ref.start, ref.end), ref.id);
    cursor = ref.end;
  }
  result += escaped.slice(cursor);
  return result;
}
