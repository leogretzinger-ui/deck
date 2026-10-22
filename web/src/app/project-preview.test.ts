// The Open preview flow's gate: the entry URL is the one value
// the flow navigates a tab to, so every shape it must refuse is pinned
// here — the browser path (project-preview.ts) rejects the same set
// before any form exists, and a ticket in a query string is the exact
// hazard the POST body exists to avoid.
import test from "node:test";
import assert from "node:assert/strict";
import { PREVIEW_NOT_CONFIGURED, previewEntryProblem } from "./project-preview.ts";

const DECK = "https://deck.example";
const LOOPBACK_DECK = "http://127.0.0.1:8080";
const LOCALHOST_DECK = "http://localhost:8080";

test("an exact HTTPS entry on another origin passes the gate", () => {
  assert.equal(previewEntryProblem("https://app.preview.example/entry", DECK), null);
});

test("a loopback Deck accepts a plain-HTTP entry on another loopback literal", () => {
  assert.equal(previewEntryProblem("http://127.0.0.2:8840/entry", LOOPBACK_DECK), null);
  assert.equal(previewEntryProblem("http://127.0.0.2:8840/entry", LOCALHOST_DECK), null);
  assert.equal(previewEntryProblem("http://127.255.255.255:8840/entry", LOOPBACK_DECK), null,
    "the whole 127.0.0.0/8 block except 127.0.0.1 is accepted");
});

test("a loopback entry never uses Deck's own address or name", () => {
  assert.ok(previewEntryProblem("http://127.0.0.1:8840/entry", LOOPBACK_DECK) !== null,
    "cookies ignore the port, so a preview on 127.0.0.1 is the same site as Deck");
  assert.ok(previewEntryProblem("http://127.1:8840/entry", LOOPBACK_DECK) !== null,
    "URL parsing normalizes 127.1 to 127.0.0.1");
  assert.ok(previewEntryProblem("http://localhost:8840/entry", LOOPBACK_DECK) !== null);
});

test("the loopback rule judges the parser's normalized IPv4 form", () => {
  const deck = "http://127.0.0.1:18082";
  // form.action resolves through the same WHATWG parser, so the host the
  // gate accepts is the host the browser POSTs to. Pin the normalized
  // spelling beside each verdict so a parser change fails loudly.
  assert.equal(new URL("http://0x7f.0.0.2:8840/").hostname, "127.0.0.2");
  assert.equal(previewEntryProblem("http://0x7f.0.0.2:8840/entry", deck), null);
  assert.equal(new URL("http://2130706434:8840/").hostname, "127.0.0.2");
  assert.equal(previewEntryProblem("http://2130706434:8840/entry", deck), null);
  assert.equal(new URL("http://127.0.0.2.:8840/").hostname, "127.0.0.2");
  assert.equal(previewEntryProblem("http://127.0.0.2.:8840/entry", deck), null,
    "the parser drops the trailing dot, so the dotted-decimal check sees a plain 127.0.0.2");
  assert.ok(previewEntryProblem("http://user@127.0.0.2:8840/entry", deck) !== null,
    "userinfo would travel with every request");
  assert.ok(previewEntryProblem("http://[::ffff:127.0.0.2]:8840/entry", deck) !== null,
    "an IPv6-mapped spelling stays bracketed, never the dotted decimal the rule matches");
  assert.equal(previewEntryProblem("http://127.0.0.0:8840/entry", deck), null,
    "127.0.0.0 is the block's network address, still a loopback literal");
});

test("a plain-HTTP entry is refused when Deck is not itself loopback HTTP", () => {
  assert.ok(previewEntryProblem("http://127.0.0.2:8840/entry", DECK) !== null,
    "an HTTPS page never POSTs the ticket to a plain-HTTP address");
  assert.ok(previewEntryProblem("http://127.0.0.2:8840/entry", "https://127.0.0.1:8080") !== null);
});

test("a plain-HTTP Deck on a non-loopback host still refuses a loopback entry", () => {
  assert.ok(previewEntryProblem("http://127.0.0.2:8840/entry", "http://deck.example") !== null,
    "an http page reached by name would POST the ticket to an address the viewer's machine resolves locally");
  assert.ok(previewEntryProblem("http://127.0.0.2:8840/entry", "http://192.168.1.5:8080") !== null);
  assert.ok(previewEntryProblem("http://127.0.0.2:8840/entry", "http://[::1]:8080") !== null,
    "IPv6 loopback is not in the accepted set");
  assert.ok(previewEntryProblem("http://127.0.0.2:8840/entry", "not a url") !== null,
    "an unparseable Deck origin fails closed");
});

test("loopback allowance covers no other host", () => {
  assert.ok(previewEntryProblem("http://app.preview.example/entry", LOOPBACK_DECK) !== null);
  assert.ok(previewEntryProblem("http://[::1]:8840/entry", LOOPBACK_DECK) !== null);
  assert.ok(previewEntryProblem("http://127.evil.example.com/entry", LOOPBACK_DECK) !== null,
    "a DNS name that merely begins with 127. is not a loopback literal");
  assert.ok(previewEntryProblem("http://127.0.0.2.evil.example/entry", LOOPBACK_DECK) !== null,
    "a name longer than four labels is a DNS name, not a dotted quad");
  assert.ok(previewEntryProblem("http://192.0.2.9:8840/entry", LOOPBACK_DECK) !== null,
    "a dotted quad outside 127.0.0.0/8 is not loopback");
});

test("the loopback block's edges and malformed octets are refused", () => {
  for (const host of ["126.255.255.255", "128.0.0.0", "12.0.0.2", "107.0.0.2", "227.0.0.2", "127.0.0.2x", "127.0.0.x2", "127.a.b.c", "127.0.0.1e1", "127.0.0.2e0", "127x0x0x2", "127-0-0-2", "127.0.0-2"]) {
    assert.equal(new URL(`http://${host}/`).hostname, host, `${host} reaches the rule verbatim`);
    assert.ok(previewEntryProblem(`http://${host}:8840/entry`, LOOPBACK_DECK) !== null, host);
  }
  // A leading zero is parser-normalized away before the check, so it is the
  // same literal, not a malformed one.
  assert.equal(new URL("http://127.0.0.02:8840/").hostname, "127.0.0.2");
  assert.equal(previewEntryProblem("http://127.0.0.02:8840/entry", LOOPBACK_DECK), null);
  assert.equal(new URL("http://127.0.0.0x2:8840/").hostname, "127.0.0.2");
  assert.equal(previewEntryProblem("http://127.0.0.0x2:8840/entry", LOOPBACK_DECK), null);
  // Every digit class accepts its full range, including inner zeros.
  assert.equal(previewEntryProblem("http://127.9.99.199:8840/entry", LOOPBACK_DECK), null);
  assert.equal(previewEntryProblem("http://127.10.100.200:8840/entry", LOOPBACK_DECK), null);
});

test("a Deck host merely resembling loopback does not unlock plain-HTTP entries", () => {
  for (const deck of ["http://localhost.evil.example:8080", "http://127.0.0.7:8080", "http://localhost2:8080", "http://notlocalhost:8080"]) {
    assert.ok(previewEntryProblem("http://127.0.0.2:8840/entry", deck) !== null, deck);
  }
});

test("the entry never shares Deck's own origin", () => {
  assert.ok(previewEntryProblem("https://deck.example/entry", DECK) !== null,
    "a same-origin entry would put the ticket POST on Deck itself");
});

test("a non-HTTPS entry is refused", () => {
  assert.ok(previewEntryProblem("http://app.preview.example/entry", DECK) !== null);
});

test("the loopback allowance covers plain HTTP and no other scheme", () => {
  assert.ok(previewEntryProblem("ftp://127.0.0.2:8840/entry", LOOPBACK_DECK) !== null,
    "the allowance exists so a loopback install can POST the ticket over plain HTTP");
  assert.ok(previewEntryProblem("ws://127.0.0.2:8840/entry", LOOPBACK_DECK) !== null);
});

test("userinfo, query, and fragment never reach the submission", () => {
  assert.ok(previewEntryProblem("https://user:pass@app.preview.example/entry", DECK) !== null,
    "userinfo would travel with every request");
  assert.ok(previewEntryProblem("https://:pass@app.preview.example/entry", DECK) !== null,
    "a password with an empty username is still userinfo");
  assert.ok(previewEntryProblem("https://app.preview.example/entry?ticket=one-use", DECK) !== null,
    "a query would put the ticket in referrers, history and access logs");
  assert.ok(previewEntryProblem("https://app.preview.example/entry#after", DECK) !== null);
  assert.ok(previewEntryProblem("http://127.0.0.2:8840/entry?ticket=one-use", LOOPBACK_DECK) !== null,
    "the loopback allowance skips none of the gate");
});

test("an unparseable entry is refused, not submitted", () => {
  assert.ok(previewEntryProblem("not a url", DECK) !== null);
  assert.ok(previewEntryProblem("", DECK) !== null);
});

test("the unconfigured sentence matches the backend's own words", () => {
  assert.equal(PREVIEW_NOT_CONFIGURED, "Preview is not configured for this project.");
});
