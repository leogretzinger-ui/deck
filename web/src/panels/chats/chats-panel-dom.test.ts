// The Chats DOM shell against a string-only container: what it draws for
// the rows the fixture cannot reach — an add and a leave, quiet unless
// uncertain, and one handshake row per member with the leaver out of the
// quorum. The shell only assigns innerHTML and binds two clicks, so a
// container that records the markup is the whole browser it needs.
import assert from "node:assert/strict";
import test from "node:test";

import type { ApiClient } from "../../api/contracts.ts";
import type { Chat, ChatLedger, ChatRow } from "../../api/chats.ts";
import { ChatsPanelModel } from "./chats-panel.ts";
import { ChatsPanel } from "./chats-panel-dom.ts";

function container(): { el: HTMLElement; html: () => string; click: (selector: string) => void } {
  const clicks = new Map<string, () => void>();
  const box = {
    innerHTML: "",
    querySelector: (selector: string) => ({
      addEventListener: (_event: string, callback: () => void) => clicks.set(selector, callback),
      focus: () => {},
    }),
    querySelectorAll: () => [],
  };
  return { el: box as unknown as HTMLElement, html: () => box.innerHTML, click: (selector) => clicks.get(selector)?.() };
}

function ledger(chat: Partial<Chat>): ChatLedger {
  return {
    agent: "claude-61c3",
    chats: [{
      id: "chat-1", state: "open", waiting_s: null, forced: false, opened_by: "claude-61c3", opened_at: "2026-09-03T14:22:00Z",
      last: "2026-09-03T14:25:10Z", members: ["claude-61c3", "codex-9f2a", "kimi-2b77"], left: [], remaining: ["claude-61c3", "codex-9f2a", "kimi-2b77"],
      close_requested_by: null, closed_by: null, messages: 1, rows: [], handshake: [], ...chat,
    }],
  };
}

async function render(chat: Partial<Chat>, earlier = false): Promise<string> {
  const box = container();
  const model = new ChatsPanelModel({ getSessionChats: async () => ledger(chat) } as unknown as ApiClient, "s1");
  const panel = new ChatsPanel(box.el, model);
  await panel.refresh();
  if (earlier) box.click("[data-chats-earlier-toggle]");
  return box.html();
}

test("an install without agent chat says so and links the setup steps", async () => {
  const box = container();
  const model = new ChatsPanelModel({
    getSessionChats: async () => { throw new Error("boom"); },
    getChatsStatus: async () => ({ available: false }),
  } as unknown as ApiClient, "s1", { onError: () => {} });
  await new ChatsPanel(box.el, model).refresh();
  const html = box.html();
  assert.match(html, /data-chats-empty/);
  assert.match(html, /Agent chat is off/);
  assert.match(html, /Docker installs add the agent chat overlay to turn it on\. <a href="[^"]+#agent-chat-and-shared-memory"[^>]*data-hint="[^"]+"[^>]*>Agent chat setup<\/a>/);
  assert.doesNotMatch(html, /could not be read/);
});

test("an add and a leave are quiet rows unless a delivery of them is uncertain", async () => {
  const html = await render({
    rows: [
      { id: "e1", tag: "a2a#3ff6b9", kind: "added", from: "codex-9f2a", member: "kimi-2b77", text: "", at: "2026-09-03T14:23:30Z", to: { "claude-61c3": "read", "kimi-2b77": "delivered" } },
      { id: "e2", tag: "a2a#3ff6e2", kind: "left", from: "kimi-2b77", member: null, text: "gotta go", at: "2026-09-03T14:25:10Z", to: { "claude-61c3": "uncertain", "codex-9f2a": "read" } },
    ],
  });
  assert.match(html, /codex-9f2a added kimi-2b77/);
  assert.match(html, /kimi-2b77 left: gotta go/);
  const added = html.slice(html.indexOf('data-chat-row="e1"'), html.indexOf('data-chat-row="e2"'));
  assert.match(added, /data-chat-row-state="delivered"/);
  assert.match(added, /<span data-chat-row-word[^>]*>\s*<\/span>/, "a quiet row carries no word");
  assert.match(added, /text-text-tertiary/, "the fact reads in tertiary");
  const left = html.slice(html.indexOf('data-chat-row="e2"'));
  assert.match(left, /data-chat-row-state="uncertain"/);
  assert.match(left, /text-warning[^>]*>\s*uncertain\s*</, "an uncertain delivery of a membership event takes the word and the hue");
  assert.match(left, /data-hint="a2a#3ff6e2 · \d\d:\d\d:\d\d · claude-61c3 uncertain · codex-9f2a read"/, "the hover record leads with the id and the time");
});

test("historical closing draws one handshake row per member, the leaver out of the quorum, the proposer checked", async () => {
  const html = await render({
    state: "closing",
    close_requested_by: "claude-61c3",
    left: ["kimi-2b77"],
    remaining: ["claude-61c3", "codex-9f2a"],
    handshake: [
      { member: "claude-61c3", agreed: true, at: "2026-09-03T14:24:12Z", left: false, left_at: null },
      { member: "codex-9f2a", agreed: false, at: null, left: false, left_at: null },
      { member: "kimi-2b77", agreed: false, at: null, left: true, left_at: "2026-09-03T14:25:10Z" },
    ],
  });
  assert.match(html, /data-chat-word[^>]*>closing</);
  assert.equal((html.match(/data-chat-handshake="/g) ?? []).length, 3);
  const proposer = html.slice(html.indexOf('data-chat-handshake="claude-61c3"'), html.indexOf('data-chat-handshake="codex-9f2a"'));
  assert.match(proposer, /<svg/, "the agreed row carries the check");
  assert.match(proposer, /claude-61c3 agreed/);
  assert.match(proposer, /data-hint="claude-61c3 agreed to end the chat"/);
  const waiting = html.slice(html.indexOf('data-chat-handshake="codex-9f2a"'), html.indexOf('data-chat-handshake="kimi-2b77"'));
  assert.doesNotMatch(waiting, /<svg/, "a waiting row carries nothing");
  assert.match(waiting, /codex-9f2a — waiting/);
  assert.match(waiting, /data-hint="Nothing has happened yet; codex-9f2a has not agreed"/);
  const leaver = html.slice(html.indexOf('data-chat-handshake="kimi-2b77"'));
  assert.match(leaver, /kimi-2b77 left/);
  assert.match(leaver, /data-hint="Out of the closing quorum"/);
  const memberLine = html.slice(html.indexOf("data-chats-members"), html.indexOf("data-chat-word"));
  assert.match(memberLine, /codex-9f2a/);
  assert.doesNotMatch(memberLine, /kimi-2b77/, "the member line shows those still in");
});

function rowAt(id: string, tag: string, at: string, to: Record<string, "sent" | "delivered" | "read" | "uncertain">): ChatRow {
  return { id, tag, kind: "message", from: "claude-61c3", member: null, text: `text ${id}`, at, to };
}

test("read rows dim without a word, the newest owed row alone carries the courier's word, time is a stamp", async () => {
  const html = await render({
    rows: [
      rowAt("m1", "a2a#000001", "2026-09-03T14:22:07Z", { "codex-9f2a": "read", "kimi-2b77": "read" }),
      rowAt("m2", "a2a#000002", "2026-09-03T14:22:31Z", { "codex-9f2a": "read", "kimi-2b77": "delivered" }),
      rowAt("m3", "a2a#000003", "2026-09-03T14:59:02Z", { "codex-9f2a": "read", "kimi-2b77": "sent" }),
    ],
  });
  const m1 = html.slice(html.indexOf('data-chat-row="m1"'), html.indexOf('data-chat-row="m2"'));
  const m2 = html.slice(html.indexOf('data-chat-row="m2"'), html.indexOf('data-chat-row="m3"'));
  const m3 = html.slice(html.indexOf('data-chat-row="m3"'));
  assert.match(m1, /data-chat-row-settled="true"/);
  assert.match(m1, /<span data-chat-row-word[^>]*>\s*<\/span>/, "read is not a word");
  assert.match(m2, /data-chat-row-settled="false"/);
  assert.match(m2, /<span data-chat-row-word[^>]*>\s*<\/span>/, "an owed row that is not the newest carries no word");
  assert.match(m3, /data-chat-row-word[^>]*>\s*sent\s*</, "the newest row says where the courier got to");
  assert.equal((html.match(/data-chat-stamp/g) ?? []).length, 2, "one stamp at the top and one at the 36-minute gap");
  assert.doesNotMatch(html, /data-chat-row-time/, "no per-row time without Details");
  assert.doesNotMatch(html, />[^<]*a2a#00000[123][^<]*</, "message ids never sit in the pane as text; they lead the hover record");
  assert.match(m1, /data-hint="a2a#000001 · \d\d:\d\d:\d\d · codex-9f2a read · kimi-2b77 read"/);
  assert.doesNotMatch(html, /font-mono/, "no mono in the pane");
  assert.doesNotMatch(html, /line-clamp/, "text in full");
});

test("Details puts the time on every row and the chat id under the header, and is remembered", async () => {
  const g = globalThis as { localStorage?: unknown };
  const store = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => store.set(k, v) };
  try {
    store.set("noisedeck.chats.details", "on");
    const html = await render({ rows: [rowAt("m1", "a2a#000001", "2026-09-03T14:22:07Z", { "codex-9f2a": "read" })] });
    assert.match(html, /data-chats-details[^>]*aria-pressed="true"/);
    assert.match(html, /data-chat-detail[^>]*>[\s\S]*chat-1[\s\S]*opened \d\d:\d\d by claude-61c3/);
    assert.match(html, /data-chat-row-time[^>]*>\d\d:\d\d:\d\d</);
    assert.doesNotMatch(html, /data-chat-stamp/, "stamps go when every row carries its time");
    assert.match(html, /data-hint="a2a#000001 · codex-9f2a read"/, "the record drops the time the row already shows");
  } finally {
    delete g.localStorage;
  }
});

test("the member line names three and folds from four to the opener, tiles and a count", async () => {
  const three = await render({ rows: [] });
  const line3 = three.slice(three.indexOf("data-chats-members"), three.indexOf("data-chat-word"));
  assert.match(line3, /data-foldable="false"/);
  assert.doesNotMatch(line3, /↔|·/, "no glyph between members");
  for (const h of ["claude-61c3", "codex-9f2a", "kimi-2b77"]) assert.match(line3, new RegExp(h));
  const four = await render({ rows: [], members: ["claude-61c3", "codex-9f2a", "kimi-2b77", "opencode-e0a1"], remaining: ["claude-61c3", "codex-9f2a", "kimi-2b77", "opencode-e0a1"] });
  const line4 = four.slice(four.indexOf("data-chats-members"), four.indexOf("data-chat-word"));
  assert.match(four, /<button type="button" data-chats-members data-foldable="true" aria-expanded="false"/, "a foldable line is a disclosure button");
  assert.match(three, /<span data-chats-members data-foldable="false"/, "a line that names everyone is not a control");
  assert.doesNotMatch(three, /<button[^>]*data-chats-members/);
  assert.match(line4, /claude-61c3/, "the opener keeps its handle");
  assert.doesNotMatch(line4, />(codex-9f2a|kimi-2b77|opencode-e0a1)</, "the rest fold to tiles; their handles live only in the hover record");
  assert.match(line4, /data-chats-members-folded[\s\S]*\+3/);
  assert.match(four, /data-hint="chat-1 · opened \d\d:\d\d by claude-61c3 · claude-61c3 · codex-9f2a · kimi-2b77 · opencode-e0a1"/, "the header's hover record names them all");
});

test("a tile colours by the harness presence names, and an agent without a hue gets a visible neutral", async () => {
  const html = await render({
    members: ["claude-61c3", "scout", "reviewer-933"], remaining: ["claude-61c3", "scout", "reviewer-933"],
    harnesses: { "claude-61c3": "claude", scout: "kimi", "reviewer-933": "unknown" },
    rows: [{ id: "m1", tag: "a2a#000001", kind: "message", from: "scout", member: null, text: "hi", at: "2026-09-03T14:22:07Z", to: { "claude-61c3": "read" } }],
  });
  assert.match(html, /data-chat-tile="kimi" class="[^"]*bg-provider-kimi/, "a chosen name colours by its presence's harness, not its shape");
  assert.match(html, /data-chat-tile="unknown" class="[^"]*bg-text-tertiary/, "no hue means the neutral");
  assert.doesNotMatch(html, /bg-surface-sunken/, "never a surface: black in dark mode");
  assert.equal((html.match(/data-chat-tile="kimi"/g) ?? []).length, 2, "the member line and the row agree");
  // Without a harness map, the handle's shape stands in.
  const bare = await render({ members: ["codex-9f2a", "claude-61c3"], remaining: ["codex-9f2a", "claude-61c3"], rows: [] });
  assert.match(bare, /data-chat-tile="codex" class="[^"]*bg-provider-codex/);
});

test("earlier rows describe each close without inventing agreement", async () => {
  const cases: { chat: Partial<Chat>; hint: RegExp }[] = [
    { chat: { state: "closed", close_cause: "opener", closed_by: "claude-61c3" }, hint: /claude-61c3 opened it and ended it for everyone at \d\d:\d\d/ },
    { chat: { state: "closed", close_cause: "quorum", closed_by: "codex-9f2a" }, hint: /Everyone agreed to end it at \d\d:\d\d/ },
    { chat: { state: "closed", close_cause: "quorum", closed_by: "claude-61c3" }, hint: /Everyone agreed to end it at \d\d:\d\d/ },
    { chat: { state: "closed", close_cause: undefined, closed_by: "claude-61c3" }, hint: /It ended at \d\d:\d\d/ },
    { chat: { state: "left", participation_at: "2026-09-03T14:24:00Z", close_cause: "opener", closed_by: "codex-9f2a" }, hint: /claude-61c3 left the chat at \d\d:\d\d/ },
    { chat: { state: "forced", forced: true, closed_by: "codex-9f2a", close_requested_by: "codex-9f2a" }, hint: /codex-9f2a forced the end at \d\d:\d\d/ },
    { chat: { state: "declined", left: ["codex-9f2a"] }, hint: /codex-9f2a declined the invitation at \d\d:\d\d/ },
    { chat: { state: "closed", close_cause: "departure", closed_by: null, members: ["claude-61c3", "codex-9f2a"], left: ["claude-61c3"], remaining: ["codex-9f2a"], handshake: [] }, hint: /It ended at \d\d:\d\d after a member left/ },
    { chat: { state: "closed", close_cause: "departure", closed_by: null, members: ["claude-61c3", "codex-9f2a", "kimi-2b77", "opencode-e0a1"], left: ["claude-61c3"], remaining: ["codex-9f2a", "kimi-2b77", "opencode-e0a1"] }, hint: /It ended at \d\d:\d\d after a member left/ },
  ];
  for (const { chat, hint } of cases) {
    const html = await render(chat, true);
    assert.match(html, /data-chat-earlier="chat-1"/);
    assert.match(html, new RegExp(`data-hint="${hint.source}"`));
  }
});

test("nothing in the shell sends, joins or ends a chat", async () => {
  const html = await render({ rows: [] });
  assert.doesNotMatch(html, /<textarea|<input|type="submit"/);
  assert.doesNotMatch(html, /a2a (chat|reply|add|end|leave)/, "no command is offered to the user");
  assert.match(html, /data-chats-close/);
  assert.match(html, /Hide the chats panel; participation is unchanged/);
});

test("a pre-adoption legacy leaver renders in Earlier without its historical handshake", async () => {
  const html = await render({
    state: "left",
    opened_by: "codex-9f2a",
    left: ["claude-61c3"],
    remaining: ["codex-9f2a", "kimi-2b77"],
    participation_at: "2026-09-03T14:24:00Z",
    close_requested_by: "codex-9f2a",
    handshake: [
      { member: "claude-61c3", agreed: false, at: null, left: true, left_at: "2026-09-03T14:24:00Z" },
      { member: "codex-9f2a", agreed: true, at: "2026-09-03T14:23:00Z", left: false, left_at: null },
      { member: "kimi-2b77", agreed: false, at: null, left: false, left_at: null },
    ],
  }, true);
  assert.match(html, /data-chat-earlier="chat-1"/);
  assert.match(html, /data-hint="claude-61c3 left the chat at \d\d:\d\d"/);
  assert.doesNotMatch(html, /data-chat="chat-1"|data-chat-handshake|data-chat-word/, "legacy closing facts cannot render a live chat for a leaver");
  assert.doesNotMatch(html, /Everyone agreed|waiting|agreed to end/, "no agreement is implied by departure");
});

test("dismissing the panel invokes only the local close callback", async () => {
  const box = container();
  let dismissals = 0;
  const data = ledger({ rows: [] });
  const model = new ChatsPanelModel({ getSessionChats: async () => data } as unknown as ApiClient, "s1");
  const panel = new ChatsPanel(box.el, model, { onClose: () => { dismissals += 1; } });
  await panel.refresh();
  box.click("[data-chats-close]");
  assert.equal(dismissals, 1);
  assert.equal(model.live.length, 1);
  assert.equal(model.live[0]?.state, "open");
  assert.deepEqual(model.live[0]?.left, []);
});

// A list that scrolls: the shell reads its place before a repaint and puts
// it back after. The fake is found only once the markup holds a list, as
// in a browser, remembers what the shell assigned, and takes the height the
// new markup would have as the markup is assigned, as a new row would.
function scrollingContainer(list: { scrollTop: number; clientHeight: number; scrollHeight: number }, height: () => number): HTMLElement {
  let markup = "";
  const box = {
    get innerHTML() {
      return markup;
    },
    set innerHTML(next: string) {
      markup = next;
      list.scrollHeight = height();
    },
    querySelector: (selector: string) => (selector === "[data-chats-list]" && markup.includes("data-chats-list") ? list : null),
    querySelectorAll: () => [],
  };
  return box as unknown as HTMLElement;
}

test("a repaint shows the newest row when you had it, and keeps the place when you had scrolled up", async () => {
  const list = { scrollTop: 0, clientHeight: 300, scrollHeight: 0 };
  let height = 1000;
  const row = (id: string): ChatRow => ({ id, tag: `a2a#${id}`, kind: "message", from: "codex-9f2a", member: null, text: id, at: "2026-09-03T14:23:30Z", to: { "claude-61c3": "read" } });
  let rows = [row("e1")];
  const model = new ChatsPanelModel({ getSessionChats: async () => ledger({ rows }) } as unknown as ApiClient, "s1");
  const panel = new ChatsPanel(scrollingContainer(list, () => height), model);
  // The first draw of the ledger lands on the newest row: nothing was in view before it.
  await panel.refresh();
  assert.equal(list.scrollTop, 1000, "a list never scrolled shows its newest row");
  // A row arrives while the newest row is in view: the list follows it.
  height = 1200;
  rows = [...rows, row("e2")];
  await panel.refresh();
  assert.equal(list.scrollTop, 1200, "the list follows a new row when the newest was in view");
  // Scrolled up to read, a row arrives: the place is kept.
  list.scrollTop = 250;
  height = 1400;
  rows = [...rows, row("e3")];
  await panel.refresh();
  assert.equal(list.scrollTop, 250, "a list scrolled up to read stays where it is");
  // Within the slack of the end still counts as showing the newest row.
  list.scrollTop = 1400 - 300 - 3;
  height = 1600;
  await panel.refresh();
  assert.equal(list.scrollTop, 1600);
});

// A container whose Earlier row can be opened: querySelectorAll hands the
// shell one fake button for the row's selector and records its click.
function earlierContainer(): { el: HTMLElement; html: () => string; click: (selector: string) => void; openRow: () => void } {
  const clicks = new Map<string, () => void>();
  let openRow: () => void = () => {};
  const box = {
    innerHTML: "",
    querySelector: (selector: string) => ({
      addEventListener: (_event: string, callback: () => void) => clicks.set(selector, callback),
      focus: () => {},
    }),
    querySelectorAll: (selector: string) => (selector === "button[data-chat-earlier-open]"
      ? [{ addEventListener: (_event: string, callback: () => void) => { openRow = callback; }, closest: () => ({ getAttribute: () => "chat-1" }) }]
      : []),
  };
  return { el: box as unknown as HTMLElement, html: () => box.innerHTML, click: (selector) => clicks.get(selector)?.(), openRow: () => openRow() };
}

test("an ended chat is a row that opens in place to its rows, its member line folded like a header", async () => {
  const members = Array.from({ length: 17 }, (_, i) => (i === 0 ? "claude-61c3" : `codex-${String(i).padStart(4, "0")}`));
  const rows: ChatRow[] = [
    { id: "m1", tag: "a2a#0001", kind: "message", from: "claude-61c3", member: null, text: "STRESS1 start", at: "2026-09-03T14:22:07Z", to: Object.fromEntries(members.slice(1).map((m) => [m, "read"])) },
    { id: "m2", tag: "a2a#0002", kind: "message", from: "codex-0001", member: null, text: "STRESS1 codex-0001 1/3", at: "2026-09-03T14:22:37Z", to: { "claude-61c3": "read" } },
  ];
  // The shell escapes the chat id for the selector it refocuses; node has no CSS.
  (globalThis as { CSS?: unknown }).CSS ??= { escape: (value: string) => value };
  const box = earlierContainer();
  const model = new ChatsPanelModel({ getSessionChats: async () => ledger({ state: "closed", closed_by: "claude-61c3", close_cause: "opener", members, remaining: members, messages: 2, rows }) } as unknown as ApiClient, "s1");
  const panel = new ChatsPanel(box.el, model);
  await panel.refresh();
  box.click("[data-chats-earlier-toggle]");
  let html = box.html();
  const row = html.slice(html.indexOf('data-chat-earlier="chat-1"'));
  assert.match(row, /button[^>]*data-chat-earlier-open[^>]*aria-expanded="false"/);
  assert.equal((row.match(/data-chat-tile=/g) ?? []).length, 17, "the opener's tile and sixteen folded tiles");
  assert.match(row, />\+16</, "the fold counts the members it hides");
  assert.match(row, /2 messages/);
  assert.doesNotMatch(row, /data-chat-row=/, "the rows wait to be opened");
  assert.doesNotMatch(row, /data-chat-word/, "the state is the row's word, never a live chat's");
  box.openRow();
  html = box.html();
  const opened = html.slice(html.indexOf('data-chat-earlier="chat-1"'));
  assert.match(opened, /aria-expanded="true"/);
  assert.match(opened, /data-chat-row="m1"[\s\S]*data-chat-row="m2"/, "open, the rows read oldest to newest");
  assert.match(opened, /STRESS1 codex-0001 1\/3/);
  box.openRow();
  assert.doesNotMatch(box.html().slice(box.html().indexOf('data-chat-earlier="chat-1"')), /data-chat-row=/, "a second press folds it");
});
