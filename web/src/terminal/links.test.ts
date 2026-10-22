import assert from "node:assert/strict";
import test from "node:test";
import { continuesInto, findUrls, joinWrappedSelection, rowFromChars, rowFromText, trimUrlEnd } from "./links.ts";

const cols = 40;
const pad = (s: string) => s + " ".repeat(Math.max(0, cols - s.length));
const hard = (s: string) => rowFromText(pad(s), false, cols);
const soft = (s: string) => rowFromText(s, true, cols);
const plain = (s: string) => rowFromText(s, false, cols);

// The sign-in case: a long URL the application broke at the column edge and
// padded. The rows must read as one URL, mapped back to the cells it occupies.
test("a URL hard-wrapped over three rows is one link", () => {
  const url = "https://claude.ai/oauth/authorize?code=abc123&redirect_uri=https%3A%2F%2Fconsole.example%2Fcallback&scope=x";
  const rows = [plain("Open this URL to sign in:"), hard(url.slice(0, 40)), hard(url.slice(40, 80)), plain(url.slice(80))];
  const found = findUrls(rows);
  assert.equal(found.length, 1);
  assert.equal(found[0]!.text, url);
  assert.deepEqual([found[0]!.startRow, found[0]!.startCol, found[0]!.endRow, found[0]!.endCol], [1, 0, 3, url.length - 80]);
});

test("xterm's own soft wraps join verbatim", () => {
  const url = "https://example.com/a/very/long/path/that/keeps/going/on/and/on/x";
  const rows = [plain(url.slice(0, 40)), soft(url.slice(40))];
  assert.equal(findUrls(rows)[0]?.text, url);
});

// A row that ends short of the edge is a real line end, and a successor that
// begins with a space is prose, whatever the row before it looked like.
test("rows that do not fill the width are not joined", () => {
  assert.equal(continuesInto(plain("https://example.com/short"), plain("next line")), false);
  assert.equal(continuesInto(hard("x".repeat(40)), plain(" indented")), false);
  assert.equal(continuesInto(hard("x".repeat(39)), plain("y")), true, "ink within a cell of the edge counts");
});

// String offsets are not columns: a wide character takes two cells and a
// combining sequence takes one. The ranges must come from the cells.
test("cell ranges survive wide and combining characters before and inside a URL", () => {
  const wide = rowFromChars([{ ch: "中", width: 2 }, { ch: " ", width: 1 }, ...[..."https://example.com"].map((ch) => ({ ch, width: 1 }))], false, cols);
  const w = findUrls([wide])[0]!;
  assert.equal(w.text, "https://example.com");
  assert.deepEqual([w.startCol, w.endCol], [3, 22], "the URL starts after the two-cell character");
  const combining = rowFromChars([{ ch: "é", width: 1 }, { ch: " ", width: 1 }, ...[..."https://example.com"].map((ch) => ({ ch, width: 1 }))], false, cols);
  const c = findUrls([combining])[0]!;
  assert.deepEqual([c.startCol, c.endCol], [2, 21], "a combining sequence is one cell");
  // A wide character inside a hard-wrapped URL row: ink is measured in cells,
  // so the row still reads as filled and the continuation still joins.
  const head = [..."https://example.com/"].map((ch) => ({ ch, width: 1 }));
  const inside = rowFromChars([...head, ...Array.from({ length: 9 }, () => ({ ch: "中", width: 2 })), { ch: "a", width: 1 }, { ch: "b", width: 1 }], false, cols);
  assert.equal(inside.inkEnd, 40);
  const joined = findUrls([inside, plain("tail")])[0]!;
  assert.equal(joined.text, "https://example.com/中中中中中中中中中abtail");
  assert.deepEqual([joined.endRow, joined.endCol], [1, 4]);
});

test("trailing punctuation and an unbalanced bracket go back to the sentence", () => {
  assert.equal(trimUrlEnd("https://example.com/a)."), "https://example.com/a");
  assert.equal(trimUrlEnd("https://example.com/a_(b)"), "https://example.com/a_(b)");
  assert.equal(findUrls([plain("see <https://example.com/x>, then")])[0]?.text, "https://example.com/x");
});

test("several URLs on one logical line are each found", () => {
  const found = findUrls([plain("https://a.example/1 and https://b.example/2")]);
  assert.deepEqual(found.map((u) => u.text), ["https://a.example/1", "https://b.example/2"]);
});

// The known ambiguity, pinned: a URL that ends exactly at the edge followed
// by unindented prose gains the prose's first word and nothing more, because
// matching stops at the first space. It is the stock provider's failure for
// every wrapped URL, confined to this shape.
test("a URL ending at the edge before unindented prose gains at most one word", () => {
  const url = "https://example.com/" + "a".repeat(20);
  assert.equal(url.length, 40);
  const found = findUrls([hard(url), plain("Next instruction here")]);
  assert.equal(found[0]!.text, url + "Next");
  assert.equal(findUrls([hard(url), plain(" Next instruction here")])[0]!.text, url, "an indented successor does not join");
});

// The copy side: only a selection that is exactly one hard-wrapped URL joins.
// Anything the repair would have to drop — a suffix, a quote, trailing
// punctuation — keeps the native text, since the native copy is suppressed
// in the repair's favour and selected bytes must never be lost.
test("a copied selection that is one wrapped URL is joined, and nothing else is", () => {
  const url = "https://claude.ai/oauth/authorize?code=abc123&redirect_uri=https%3A%2F%2Fconsole.example%2Fcallback";
  const selection = [pad(url.slice(0, 40)), pad(url.slice(40, 80)), url.slice(80)].join("\n");
  assert.equal(joinWrappedSelection(selection, cols), url);
  assert.equal(joinWrappedSelection("just one line", cols), null);
  assert.equal(joinWrappedSelection(["Open https://example.com/x", "and more"].join("\n"), cols), null);
  assert.equal(joinWrappedSelection([pad("some prose that fills the row exactly!!"), "continues here"].join("\n"), cols), null, "not a URL");
  const edge = "https://example.com/" + "a".repeat(20);
  assert.equal(joinWrappedSelection([edge, 'end"suffix'].join("\n"), cols), null, "a suffix after the URL is never dropped");
  assert.equal(joinWrappedSelection([edge, "end."].join("\n"), cols), null, "trailing punctuation is never dropped");
  assert.equal(joinWrappedSelection([edge, "end)"].join("\n"), cols), null, "an unbalanced bracket is never dropped");
  assert.equal(joinWrappedSelection([edge, "end"].join("\n"), cols), edge + "end");
});

// Ordinary, unwrapped links keep the recognition the stock addon had: an
// uppercase scheme, a one-letter host, several on a line, and text around
// them; what is not a URL stays text.
test("ordinary links are recognised as the addon recognised them", () => {
  const texts = (row: string) => findUrls([plain(row)]).map((u) => u.text);
  assert.deepEqual(texts("see HTTPS://Example.com/Path now"), ["HTTPS://Example.com/Path"]);
  assert.deepEqual(texts("short host http://a and http://ab, then"), ["http://a", "http://ab"]);
  assert.deepEqual(texts("no scheme example.com/x or ftp://x.y"), []);
  assert.deepEqual(texts("bare https:// is not a link"), []);
  assert.deepEqual(joinWrappedSelection([pad("HTTPS://EXAMPLE.COM/" + "A".repeat(20)), "END"].join("\n"), cols), "HTTPS://EXAMPLE.COM/" + "A".repeat(20) + "END");
});
