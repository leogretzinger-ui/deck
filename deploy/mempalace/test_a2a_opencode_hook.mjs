import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));

// A stand-in for the a2a CLI: records every invocation, answers `inbox` from
// a fixture file, and succeeds on everything else.
async function fakeA2A(root, inbox) {
  const capture = path.join(root, "calls.jsonl");
  const helper = path.join(root, "a2a.py");
  await writeFile(path.join(root, "inbox.json"), JSON.stringify(inbox));
  await writeFile(
    helper,
    [
      "import json, os, sys, time",
      "args = sys.argv[1:]",
      "with open(os.environ['A2A_TEST_CAPTURE'], 'a', encoding='utf-8') as stream:",
      // A follower child holds stdin open as its death signal and must not block on it.
      "    _stdin = '' if '--follow' in args else sys.stdin.read()",
      "    stream.write(json.dumps({'args': args, 'stdin': _stdin}) + '\\n')",
      "if args[:1] == ['hook'] and os.path.exists(os.environ.get('A2A_TEST_SLOW_HOOK', '/nonexistent')):",
      "    time.sleep(1.0)",
      "if args[:1] == ['inbox'] and '--follow' in args:",
      "    import os as _os, time as _t",
      "    _root = _os.path.dirname(_os.environ['A2A_TEST_INBOX'])",
      "    open(_os.path.join(_root, 'follow-pid'), 'w').write(str(_os.getpid()))",
      "    _last = None",
      "    while True:",
      "        try: _doc = open(_os.environ['A2A_TEST_INBOX']).read().strip()",
      "        except OSError: _doc = '[]'",
      "        if _doc != _last:",
      "            _last = _doc",
      "            sys.stdout.write(_doc + '\\n')",
      "            sys.stdout.flush()",
      "            with open(_os.path.join(_root, 'follow-lines'), 'a') as _f:",
      "                _f.write(_doc + '\\n')",
      "        _t.sleep(0.02)",
      "if args[:1] == ['inbox']:",
      "    print(open(os.environ['A2A_TEST_INBOX']).read())",
      "if args[:1] == ['mark-read']:",
      "    if os.path.exists(os.environ['A2A_TEST_ACK_FAILS']):",
      "        sys.exit(1)",
      "    kept = [e for e in json.load(open(os.environ['A2A_TEST_INBOX'])) if e['id'] not in args[1:]]",
      "    json.dump(kept, open(os.environ['A2A_TEST_INBOX'], 'w'))",
    ].join("\n"),
  );
  return { helper, capture };
}

// A stub SDK client. get answers the adoption probe; promptAsync records
// every request in a SHARED log (so a plugin rebuilt against the same client
// sees one history); message answers the receipt correlation only when the
// sender admits to the message existing.
function stubClient(sender, requests) {
  return {
    session: {
      get: async ({ path: p }) => {
        if (sender.getGate) await sender.getGate;
        const child = sender.children.get(p.id);
        if (child) return { data: { id: p.id, parentID: child.parentID }, error: undefined };
        return { data: { id: p.id, ...(sender.statusOf(p.id) ? { status: { type: sender.statusOf(p.id) } } : {}) }, error: undefined };
      },
      promptAsync: async (request) => {
        requests.push(request);
        if (sender.mode === "throw") throw new Error("session switching");
        if (sender.mode === "error") return { data: null, error: { message: "not admitted" } };
        return { data: {}, error: undefined };  // the route's bare 204, whatever happens later
      },
      message: async ({ path: p }) => {
        const exists = sender.receipts && requests.some((r) => r.body.messageID === p.messageID);
        if (!exists) throw Object.assign(new Error("not found"), { status: 404 });
        return { data: { id: p.messageID }, error: undefined };
      },
    },
  };
}

// Awaits an observable with a deadline so follower-driven tests never
// depend on timer racing.
async function until(check, label = "condition", ms = 5000) {
  const deadline = Date.now() + ms;
  while (Date.now() < deadline) {
    if (await check()) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  assert.fail(`timed out waiting for ${label}`);
}

// Follower children write marker files into the root; teardown waits out
// any restart-backoff child and retries the removal so a late write cannot
// fail the suite with ENOTEMPTY.
async function rmRoot(root, follow = false) {
  if (follow) {
    const deadline = Date.now() + 5000;
    while (Date.now() < deadline) {
      const pidText = (await readFile(path.join(root, "follow-pid"), "utf8").catch(() => "")).trim();
      let dead = true;
      if (pidText && !Number.isNaN(Number(pidText))) {
        try { process.kill(Number(pidText), 0); dead = false; } catch { dead = true; }
      }
      if (dead) break;
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
  }
  await rm(root, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 });
}

async function load(root, inbox, shared = null, { follow = false } = {}) {
  const { helper, capture } = await fakeA2A(root, inbox);
  process.env.A2A_BIN = helper;
  process.env.NOISEDECK_MEMPALACE_PYTHON = process.env.PYTHON ?? "python3";
  process.env.A2A_TEST_CAPTURE = capture;
  process.env.A2A_TEST_INBOX = path.join(root, "inbox.json");
  process.env.A2A_TEST_ACK_FAILS = path.join(root, "ack-fails");
  process.env.A2A_TEST_SLOW_HOOK = path.join(root, "slow-hook");
  process.env.A2A_OPENCODE_POLL_MS = "3600000";
  // Interval-mode tests drive delivery through poll; follower tests opt in
  // and await the follower's own lines instead.
  if (follow) delete process.env.A2A_OPENCODE_FOLLOW;
  else process.env.A2A_OPENCODE_FOLLOW = "0";
  process.env.A2A_STATE_DIR = root;
  const module = await import(
    `${pathToFileURL(path.join(here, "hooks", "a2a-opencode-hook.js"))}?test=${Date.now()}-${Math.random()}`
  );
  const requests = shared?.requests ?? [];
  const sender = shared?.sender ?? { mode: "admit", receipts: true, children: new Map(), statuses: new Map(), statusOf(id) { return this.statuses.get(id) ?? null; } };
  const client = stubClient(sender, requests);
  const plugin = await module.A2AChat({ client, directory: "/workspace/deck" });
  const emit = plugin.event;
  const calls = async () =>
    (await readFile(capture, "utf8").catch(() => ""))
      .trim()
      .split("\n")
      .filter(Boolean)
      .map((line) => JSON.parse(line));
  return { emit, poll: plugin.poll, calls, requests, sender };
}

test("lifecycle events announce presence with the bounded payload", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const { emit, calls } = await load(root, []);
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  await emit({ event: { type: "session.next.prompted", properties: { sessionID: "ses_1" } } });
  await emit({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } });
  await emit({ event: { type: "session.deleted", properties: { sessionID: "ses_1" } } });
  const hooks = (await calls()).filter((c) => c.args[0] === "hook");
  assert.deepEqual(
    hooks.map((c) => c.args.slice(1)),
    [
      ["session-start", "--harness", "opencode"],
      ["prompt", "--harness", "opencode"],
      ["stop", "--harness", "opencode"],
      ["session-end", "--harness", "opencode"],
    ],
  );
  for (const call of hooks) {
    assert.deepEqual(JSON.parse(call.stdin), { session_id: "ses_1", cwd: "/workspace/deck" });
  }
});

test("an idle session receives unread mail through the prompt API and only then marks it read", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_1", from_agent: "alice", correlation_id: "chat-1", type: "chat.invite", body: "hello?" }];
  const { emit, poll, calls, requests } = await load(root, inbox);
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  assert.equal(await poll(), true);
  assert.equal(requests.length, 1);
  assert.equal(requests[0].path.id, "ses_1");
  assert.match(requests[0].body.parts[0].text, /\[a2a\] alice \(chat-1\): hello\?/);
  const order = (await calls()).map((c) => c.args[0]);
  assert.deepEqual(order, ["hook", "inbox", "mark-read"]);
  assert.deepEqual((await calls()).at(-1).args, ["mark-read", "evt_1"]);
});

test("a stale gap peek never re-submits a box the follower already handed off", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  try {
    const box = { id: "evt_9", from_agent: "alice", correlation_id: "chat-9", type: "chat.invite", body: "once only", scope: "chat-9|i" };
    const { emit, poll, calls, requests } = await load(root, [box], null, { follow: true });
    await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
    await until(() => requests.length > 0, "follower submission");
    await until(async () => JSON.parse(await readFile(process.env.A2A_TEST_INBOX, "utf8")).length === 0, "mark-read");
    assert.deepEqual((await calls()).filter((c) => c.args[0] === "mark-read").at(-1).args, ["mark-read", "evt_9"]);
    // A gap peek's snapshot may predate the acknowledgement and list the box
    // again; the delivery history must refuse a second hand-off.
    await writeFile(process.env.A2A_TEST_INBOX, JSON.stringify([box]));
    assert.equal(await poll(), false, "the stale snapshot submits nothing");
    assert.equal(requests.length, 1);
    await emit({ event: { type: "session.deleted", properties: { sessionID: "ses_1" } } });
  } finally {
    await rmRoot(root, true);
  }
});

test("a session error interrupts following but later idle still delivers", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  try {
    const { emit, calls, requests } = await load(root, [], null, { follow: true });
    await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
    await until(async () => (await readFile(path.join(root, "follow-pid"), "utf8").catch(() => "")) !== "", "follower running");
    const followerPid = Number((await readFile(path.join(root, "follow-pid"))).toString().trim());
    const alive = () => { try { process.kill(followerPid, 0); return true; } catch { return false; } };
    await emit({ event: { type: "session.error", properties: { sessionID: "ses_1" } } });
    await until(async () => !alive(), "the errored session's follower child exited");
    await writeFile(process.env.A2A_TEST_INBOX, JSON.stringify([{ id: "evt_8", from_agent: "bob", correlation_id: "chat-8", type: "chat.message", body: "after the error", scope: "chat-8|m" }]));
    await emit({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } });
    await until(() => requests.length > 0, "delivery after recovery");
    assert.match(requests[0].body.parts[0].text, /bob \(chat-8\): after the error/);
    await emit({ event: { type: "session.deleted", properties: { sessionID: "ses_1" } } });
  } finally {
    await rmRoot(root, true);
  }
});

test("nothing is delivered before the session exists or while a turn runs", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_1", from_agent: "alice", correlation_id: "chat-1", type: "chat.message", body: "early" }];
  const { emit, poll, requests } = await load(root, inbox);
  assert.equal(await poll(), false, "no session yet");
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  await emit({ event: { type: "session.next.prompted", properties: { sessionID: "ses_1" } } });
  assert.equal(await poll(), false, "mid-turn");
  assert.equal(requests.length, 0);
  await emit({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } });
  assert.equal(await poll(), true);
  assert.equal(requests.length, 1);
});

test("the bound session's status events gate delivery, busy and retry alike", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_8", from_agent: "alice", correlation_id: "chat-8", type: "chat.message", body: "status" }];
  const { emit, poll, requests } = await load(root, inbox);
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  await emit({ event: { type: "session.status", properties: { sessionID: "ses_1", status: { type: "busy" } } } });
  assert.equal(await poll(), false, "busy status holds the rung");
  await emit({ event: { type: "session.status", properties: { sessionID: "ses_1", status: { type: "retry" } } } });
  assert.equal(await poll(), false, "retry status holds the rung");
  assert.equal(requests.length, 0);
  await emit({ event: { type: "session.status", properties: { sessionID: "ses_1", status: { type: "idle" } } } });
  assert.equal(await poll(), true);
  assert.equal(requests.length, 1);
});

test("an unverified or unreadable record is never delivered nor acknowledged", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [
    { id: "evt_bad", chat: "chat-9", from: "alice", type: "chat.closed", at: "2026-09-06T00:00:00Z", reason: "plaintext body that is not this host's pre-sealing history; refused", untrusted: true },
    { id: "evt_dmg", from_agent: "alice", correlation_id: "chat-9", type: "chat.message", body: "", unreadable: "the box does not open" },
    { id: "evt_ok", from_agent: "bob", correlation_id: "chat-8", type: "chat.message", body: "real" },
  ];
  const { emit, poll, calls, requests } = await load(root, inbox);
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  assert.equal(await poll(), true);
  assert.equal(requests.length, 1);
  assert.match(requests[0].body.parts[0].text, /real/);
  assert.doesNotMatch(requests[0].body.parts[0].text, /closed|refused|does not open/);
  assert.deepEqual((await calls()).filter((c) => c.args[0] === "mark-read").map((c) => c.args), [["mark-read", "evt_ok"]]);
  // Only diagnostics left: no hand-off and no acknowledgement, poll after poll.
  await writeFile(path.join(root, "inbox.json"), JSON.stringify(inbox.slice(0, 2)));
  const before = (await calls()).length;
  assert.equal(await poll(), false);
  assert.equal(await poll(), false);
  assert.equal(requests.length, 1);
  assert.ok(!(await calls()).slice(before).some((c) => c.args[0] === "mark-read"), "nothing acknowledged");
});

test("the same box under another relay row is one hand-off, and its acknowledgement follows the new row", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const scope = "chat-7|abcd|0123456789abcdef0123456789abcdef";
  const inbox = [{ id: "evt_first", from_agent: "bob", correlation_id: "chat-7", type: "chat.message", body: "once", scope }];
  const { emit, poll, calls, requests } = await load(root, inbox);
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  await writeFile(process.env.A2A_TEST_ACK_FAILS, "");
  assert.equal(await poll(), true);
  assert.equal(requests.length, 1);
  // The fold now keeps an identical copy of the box under a new row; the
  // acknowledgement is still outstanding.
  await writeFile(path.join(root, "inbox.json"), JSON.stringify([{ ...inbox[0], id: "evt_copy" }]));
  assert.equal(await poll(), false, "no second hand-off for the same box");
  assert.equal(requests.length, 1);
  await rm(process.env.A2A_TEST_ACK_FAILS, { force: true });
  assert.equal(await poll(), false);
  const acks = (await calls()).filter((c) => c.args[0] === "mark-read");
  assert.deepEqual(acks.at(-1).args, ["mark-read", "evt_copy"], "retried with the row the fold keeps now");
  assert.deepEqual(JSON.parse(await readFile(path.join(root, "inbox.json"), "utf8")), []);
  assert.equal(await poll(), false);
  assert.equal(requests.length, 1);
});

test("an uncertain call leaves the box unread and is never submitted again", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_2", from_agent: "bob", correlation_id: "chat-2", type: "chat.message", body: "still there?" }];
  const { emit, poll, calls, requests, sender } = await load(root, inbox);
  sender.mode = "throw";  // the call may or may not have landed; no receipt ever appears
  sender.receipts = false;
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  assert.equal(await poll(), false);
  assert.equal(await poll(), false, "uncertain: no second submission");
  assert.equal(requests.length, 1, "one request, however it ended");
  assert.ok(!(await calls()).some((c) => c.args[0] === "mark-read"), "nothing acknowledged");
  sender.mode = "admit";
  assert.equal(await poll(), false, "the attempts record keeps excluding the box");
  assert.equal(requests.length, 1);
});

test("a definitive refusal stays unread and may be retried", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_3", from_agent: "bob", correlation_id: "chat-3", type: "chat.message", body: "ping" }];
  const { emit, poll, calls, requests, sender } = await load(root, inbox);
  sender.mode = "error";
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  assert.equal(await poll(), false);
  assert.equal(await poll(), false, "a clean refusal is not an uncertainty; the next poll tries again");
  assert.equal(requests.length, 2, "one attempt per poll");
  assert.ok(!(await calls()).some((c) => c.args[0] === "mark-read"), "nothing acknowledged");
});

test("an accepted request with no receipt following is uncertain and never submitted again", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_5", from_agent: "bob", correlation_id: "chat-5", type: "chat.message", body: "still there?" }];
  const { emit, poll, calls, requests, sender } = await load(root, inbox);
  sender.receipts = false;  // the route's 204, then the message never exists
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  assert.equal(await poll(), false);
  assert.equal(await poll(), false, "uncertain: no second submission");
  assert.equal(requests.length, 1, "one request, however it ended");
  assert.ok(!(await calls()).some((c) => c.args[0] === "mark-read"), "nothing acknowledged");
});

test("a rebuilt plugin reconciles the persisted attempts instead of submitting again", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_9", from_agent: "bob", correlation_id: "chat-9", type: "chat.message", body: "durable" }];
  const shared = { requests: [], sender: { mode: "admit", receipts: false, children: new Map(), statuses: new Map(), statusOf(id) { return this.statuses.get(id) ?? null; } } };
  const first = await load(root, inbox, shared);
  await first.emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  assert.equal(await first.poll(), false, "uncertain: accepted, no receipt before the restart");
  assert.equal(shared.requests.length, 1);
  const second = await load(root, inbox, shared);
  await second.emit({ event: { type: "session.updated", properties: { sessionID: "ses_1" } } });
  await second.emit({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } });
  assert.equal(await second.poll(), false, "the rebuilt plugin asks for the old receipt, not a new submission");
  assert.equal(shared.requests.length, 1, "no second submission after the restart");
  shared.sender.receipts = true;  // the admission finally shows in the session
  assert.equal(await second.poll(), false, "settling is not delivery");
  assert.equal(shared.requests.length, 1);
  const acks = (await second.calls()).filter((c) => c.args[0] === "mark-read");
  assert.deepEqual(acks.at(-1).args, ["mark-read", "evt_9"], "the persisted attempt is acknowledged");
  assert.deepEqual(JSON.parse(await readFile(path.join(root, "inbox.json"), "utf8")), []);
  // A gap peek whose snapshot was captured before that acknowledgement
  // still lists the box, and the journal entry is gone: the reconciled
  // scope must be refused, not submitted as if it were new mail.
  await writeFile(path.join(root, "inbox.json"), JSON.stringify(inbox));
  assert.equal(await second.poll(), false, "a stale snapshot never re-submits a reconciled box");
  assert.equal(shared.requests.length, 1, "no submission for the reconciled box");
});

test("a child session never becomes the target, and its lifecycle is ignored", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_6", from_agent: "alice", correlation_id: "chat-6", type: "chat.message", body: "for the parent" }];
  const { emit, poll, requests, sender } = await load(root, inbox);
  sender.children.set("ses_child", { parentID: "ses_1" });
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_child", parentID: "ses_1" } } } });
  await emit({ event: { type: "session.next.prompted", properties: { sessionID: "ses_child" } } });
  await emit({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } });
  assert.equal(await poll(), true, "the parent's idle is what delivers, never the child's busy");
  assert.equal(requests.length, 1);
  assert.equal(requests[0].path.id, "ses_1", "delivery targets the top-level session");
});

test("a resumed conversation is adopted from its lifecycle events without session.created", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_7", from_agent: "alice", correlation_id: "chat-7", type: "chat.message", body: "welcome back" }];
  const { emit, poll, calls, requests } = await load(root, inbox);
  await emit({ event: { type: "session.next.prompted", properties: { sessionID: "ses_9" } } });
  const starts = (await calls()).filter((c) => c.args[0] === "hook" && c.args[1] === "session-start");
  assert.equal(starts.length, 1, "the resumed session is announced once adopted");
  assert.deepEqual(JSON.parse(starts[0].stdin).session_id, "ses_9");
  await emit({ event: { type: "session.idle", properties: { sessionID: "ses_9" } } });
  assert.equal(await poll(), true);
  assert.equal(requests[0].path.id, "ses_9");
});

test("adoption never guesses idle: a quiet resumed session delivers only after an idle signal", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_10", from_agent: "alice", correlation_id: "chat-10", type: "chat.message", body: "patience" }];
  const { emit, poll, requests } = await load(root, inbox);
  await emit({ event: { type: "session.updated", properties: { sessionID: "ses_4" } } });
  assert.equal(await poll(), false, "adopted but never signaled idle");
  assert.equal(requests.length, 0);
  await emit({ event: { type: "session.idle", properties: { sessionID: "ses_4" } } });
  assert.equal(await poll(), true);
  assert.equal(requests.length, 1);
});

test("a submission never happens without a durable attempt journal", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_11", from_agent: "bob", correlation_id: "chat-11", type: "chat.message", body: "durable first" }];
  const { emit, poll, requests } = await load(root, inbox);
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  await mkdir(path.join(root, "opencode-attempts-ses_1.json.tmp"));
  assert.equal(await poll(), false, "no durable journal, no submission");
  assert.equal(requests.length, 0);
  await rm(path.join(root, "opencode-attempts-ses_1.json.tmp"), { recursive: true });
  assert.equal(await poll(), true, "the next poll writes the journal and delivers");
  assert.equal(requests.length, 1);
});

test("an unreadable attempt journal keeps the rung closed", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_12", from_agent: "bob", correlation_id: "chat-12", type: "chat.message", body: "unknown state" }];
  const { emit, poll, calls, requests } = await load(root, inbox);
  await writeFile(path.join(root, "opencode-attempts-ses_1.json"), "not a journal");
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  assert.equal(await poll(), false, "unknown journal state never delivers");
  assert.equal(requests.length, 0);
  assert.equal((await calls()).filter((c) => c.args[0] === "hook" && c.args[1] === "session-start").length, 0, "unknown state is not even announced as adopted");
});

test("a lifecycle signal during a pending adoption is merged, never overwritten", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_13", from_agent: "alice", correlation_id: "chat-13", type: "chat.message", body: "race" }];
  const { emit, poll, requests, sender } = await load(root, inbox);
  sender.getGate = new Promise((resolve) => {
    sender.releaseGet = resolve;
  });
  const created = emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  const busy = emit({ event: { type: "session.status", properties: { sessionID: "ses_1", status: { type: "busy" } } } });
  sender.releaseGet();
  await Promise.all([created, busy]);
  assert.equal(await poll(), false, "the busy signal that raced adoption still holds");
  assert.equal(requests.length, 0);
  await emit({ event: { type: "session.status", properties: { sessionID: "ses_1", status: { type: "idle" } } } });
  assert.equal(await poll(), true);
  assert.equal(requests.length, 1);
});

test("a busy signal inside the announcement window still holds delivery", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_14", from_agent: "alice", correlation_id: "chat-14", type: "chat.message", body: "race two" }];
  const { emit, poll, requests } = await load(root, inbox);
  await writeFile(process.env.A2A_TEST_SLOW_HOOK, "");
  const created = emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  await new Promise((resolve) => setTimeout(resolve, 150));
  const busy = emit({ event: { type: "session.status", properties: { sessionID: "ses_1", status: { type: "busy" } } } });
  await Promise.all([created, busy]);
  await rm(process.env.A2A_TEST_SLOW_HOOK, { force: true });
  assert.equal(await poll(), false, "busy that landed while the announcement ran still holds");
  assert.equal(requests.length, 0);
});

test("a busy signal that races a resume-adoption is not replayed over", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_15", from_agent: "alice", correlation_id: "chat-15", type: "chat.message", body: "race three" }];
  const { emit, poll, requests, sender } = await load(root, inbox);
  sender.getGate = new Promise((resolve) => {
    sender.releaseGet = resolve;
  });
  const idleEvent = emit({ event: { type: "session.idle", properties: { sessionID: "ses_6" } } });
  await new Promise((resolve) => setTimeout(resolve, 50));
  const busyEvent = emit({ event: { type: "session.status", properties: { sessionID: "ses_6", status: { type: "busy" } } } });
  sender.releaseGet();
  await Promise.all([idleEvent, busyEvent]);
  assert.equal(await poll(), false, "busy merged during the probe is not replayed over by the initiating idle");
  assert.equal(requests.length, 0);
});

test("a schema-corrupt attempt journal keeps the rung closed", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_race", from_agent: "bob", correlation_id: "chat-16", type: "chat.message", body: "malformed entry" }];
  const { emit, poll, calls, requests } = await load(root, inbox);
  await writeFile(
    path.join(root, "opencode-attempts-ses_1.json"),
    JSON.stringify({ evt_race: { ids: ["evt_race"], receipted: false } }),
  );
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  assert.equal(await poll(), false, "a malformed attempt entry is unknown state, and unknown state never delivers");
  assert.equal(requests.length, 0);
  assert.equal((await calls()).filter((c) => c.args[0] === "hook" && c.args[1] === "session-start").length, 0, "unknown state is not even announced as adopted");
});

test("a failed acknowledgement is retried and never redelivers", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-oc-"));
  const inbox = [{ id: "evt_4", from_agent: "bob", correlation_id: "chat-4", type: "chat.message", body: "once only" }];
  const { emit, poll, calls, requests } = await load(root, inbox);
  await writeFile(process.env.A2A_TEST_ACK_FAILS, "");
  await emit({ event: { type: "session.created", properties: { info: { id: "ses_1" } } } });
  assert.equal(await poll(), true);
  assert.equal(requests.length, 1);
  assert.equal(await poll(), false, "no redelivery while the acknowledgement is outstanding");
  await rm(process.env.A2A_TEST_ACK_FAILS, { force: true });
  assert.equal(await poll(), false);
  const acks = (await calls()).filter((c) => c.args[0] === "mark-read");
  assert.deepEqual(acks.at(-1).args, ["mark-read", "evt_4"], "the outstanding acknowledgement settles");
  assert.deepEqual(JSON.parse(await readFile(path.join(root, "inbox.json"), "utf8")), []);
});
