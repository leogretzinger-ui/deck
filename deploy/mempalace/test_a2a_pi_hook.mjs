import assert from "node:assert/strict";
import { mkdtemp, readFile, rename, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { test } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));

// Publish whole inbox snapshots while the follower and test assertions read them.
async function writeInbox(file, inbox) {
  const temporary = file + "." + randomUUID() + ".tmp";
  try {
    await writeFile(temporary, JSON.stringify(inbox));
    await rename(temporary, file);
  } finally {
    await rm(temporary, { force: true });
  }
}

// A stand-in for the a2a CLI: records every invocation, answers `inbox` from
// a fixture file, and succeeds on everything else.
async function fakeA2A(root, inbox) {
  const capture = path.join(root, "calls.jsonl");
  const helper = path.join(root, "a2a.py");
  await writeInbox(path.join(root, "inbox.json"), inbox);
  await writeFile(
    helper,
    [
      "import json, os, sys, tempfile",
      "args = sys.argv[1:]",
      "with open(os.environ['A2A_TEST_CAPTURE'], 'a', encoding='utf-8') as stream:",
      // A follower child holds stdin open as its death signal and must not block on it.
      "    _stdin = '' if '--follow' in args else sys.stdin.read()",
      "    stream.write(json.dumps({'args': args, 'stdin': _stdin}) + '\\n')",
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
      "    with tempfile.NamedTemporaryFile(mode='w', encoding='utf-8', dir=os.path.dirname(os.environ['A2A_TEST_INBOX']), delete=False) as stream:",
      "        json.dump(kept, stream)",
      "        temporary = stream.name",
      "    os.replace(temporary, os.environ['A2A_TEST_INBOX'])",
    ].join("\n"),
  );
  return { helper, capture };
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

async function load(root, inbox, { follow = false } = {}) {
  const { helper, capture } = await fakeA2A(root, inbox);
  process.env.A2A_BIN = helper;
  process.env.NOISEDECK_MEMPALACE_PYTHON = process.env.PYTHON ?? "python3";
  process.env.A2A_TEST_CAPTURE = capture;
  process.env.A2A_TEST_INBOX = path.join(root, "inbox.json");
  process.env.A2A_TEST_ACK_FAILS = path.join(root, "ack-fails");
  process.env.A2A_PI_POLL_MS = "3600000";
  // Interval-mode tests drive delivery through api.poll; follower tests opt
  // in and await the follower's own lines instead.
  if (follow) delete process.env.A2A_PI_FOLLOW;
  else process.env.A2A_PI_FOLLOW = "0";
  delete process.env.NOISEDECK_MEMPALACE_HARNESS;
  const module = await import(
    `${pathToFileURL(path.join(here, "hooks", "a2a-pi-hook.js"))}?test=${Date.now()}-${Math.random()}`
  );
  const handlers = new Map();
  const sender = { accept: true };
  let receipt;
  const extension = module.default({
    on(event, handler) {
      handlers.set(event, handler);
    },
    get sendUserMessage() {
      if (typeof sender.sendUserMessage !== "function") return undefined;
      return (text, options) => {
        sender.sendUserMessage(text, options);
        if (sender.accept) receipt = handlers.get("before_agent_start")({ prompt: text });
      };
    },
  });
  const api = { poll: async (ctx) => {
    const attempted = await extension.poll(ctx);
    await receipt;
    return attempted;
  } };
  const calls = async () =>
    (await readFile(capture, "utf8").catch(() => ""))
      .trim()
      .split("\n")
      .filter(Boolean)
      .map((line) => JSON.parse(line));
  return { handlers, api, calls, sender };
}

const session = (extra = {}) => ({
  sessionManager: {
    getSessionId: () => "pi-session",
    getSessionFile: () => "/home/agent/.pi/agent/sessions/session.jsonl",
    getCwd: () => "/workspace/deck",
  },
  ...extra,
});

test("fake acknowledgement publishes a complete inbox snapshot atomically", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-pi-"));
  try {
    const inbox = [{ id: "evt_atomic", body: "synthetic" }];
    const { helper, capture } = await fakeA2A(root, inbox);
    const file = path.join(root, "inbox.json");
    // Read the live inbox from inside json.dump after a partial write. This
    // deterministically catches truncating the published path before writing.
    const instrument = [
      "import json, os, runpy, sys",
      "inbox = os.environ['A2A_TEST_INBOX']",
      "with open(inbox) as stream: original = json.load(stream)",
      "def partial_dump(value, stream):",
      "    encoded = json.dumps(value)",
      "    with open(inbox) as live: assert json.load(live) == original",
      "    split = max(1, len(encoded) // 2)",
      "    stream.write(encoded[:split])",
      "    stream.flush()",
      "    with open(inbox) as live: assert json.load(live) == original",
      "    stream.write(encoded[split:])",
      "json.dump = partial_dump",
      "helper = sys.argv[1]",
      "sys.argv = sys.argv[1:]",
      "runpy.run_path(helper, run_name='__main__')",
    ].join("\n");
    const result = spawnSync(process.env.PYTHON ?? "python3", ["-B", "-c", instrument, helper, "mark-read", "evt_atomic"], {
      input: "", encoding: "utf8", timeout: 5000,
      env: { ...process.env, A2A_TEST_INBOX: file, A2A_TEST_CAPTURE: capture, A2A_TEST_ACK_FAILS: path.join(root, "ack-fails") },
    });
    assert.equal(result.error, undefined);
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(JSON.parse(await readFile(file, "utf8")), []);
    assert.deepEqual(JSON.parse((await readFile(capture, "utf8")).trim()).args, ["mark-read", "evt_atomic"]);
  } finally {
    await rmRoot(root);
  }
});

test("lifecycle events announce presence with the bounded payload", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-pi-"));
  try {
    const { handlers, calls } = await load(root, []);
    assert.deepEqual([...handlers.keys()].sort(), ["agent_end", "agent_start", "before_agent_start", "session_shutdown", "session_start"]);
    const ctx = session();
    for (const event of ["session_start", "agent_start", "agent_end", "session_shutdown"]) {
      await handlers.get(event)({}, ctx);
    }
    const hooks = (await calls()).filter((c) => c.args[0] === "hook");
    assert.deepEqual(
      hooks.map((c) => c.args.slice(1)),
      [
        ["session-start", "--harness", "pi"],
        ["prompt", "--harness", "pi"],
        ["stop", "--harness", "pi"],
        ["session-end", "--harness", "pi"],
      ],
    );
    for (const call of hooks) {
      assert.deepEqual(JSON.parse(call.stdin), { session_id: "pi-session", cwd: "/workspace/deck" });
    }
  } finally {
    await rmRoot(root, true);
  }
});

test("an idle agent receives unread mail through the extension API and only then marks it read", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-pi-"));
  try {
    const inbox = [{ id: "evt_1", from_agent: "alice", correlation_id: "chat-1", type: "chat.invite", body: "hello?" }];
    const { handlers, api, calls, sender } = await load(root, inbox);
    const delivered = [];
    const ctx = session();
    sender.sendUserMessage = (text, options) => delivered.push({ text, options });
    await handlers.get("session_start")({}, ctx);
    assert.equal(await api.poll(ctx), true);
    assert.equal(delivered.length, 1);
    assert.match(delivered[0].text, /\[a2a\] alice \(chat-1\): hello\?/);
    assert.equal(delivered[0].options, undefined, "idle delivery starts a turn instead of only queuing a follow-up");
    const order = (await calls()).map((c) => c.args[0]);
    assert.deepEqual(order, ["hook", "inbox", "mark-read"]);
    assert.deepEqual((await calls()).at(-1).args, ["mark-read", "evt_1"]);
  } finally {
    await rmRoot(root, true);
  }
});

test("an unverified or unreadable record is never delivered nor acknowledged", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-pi-"));
  try {
    const inbox = [
      { id: "evt_bad", chat: "chat-9", from: "alice", type: "chat.closed", at: "2026-09-06T00:00:00Z", reason: "plaintext body that is not this host's pre-sealing history; refused", untrusted: true },
      { id: "evt_dmg", from_agent: "alice", correlation_id: "chat-9", type: "chat.message", body: "", unreadable: "the box does not open" },
      { id: "evt_ok", from_agent: "bob", correlation_id: "chat-8", type: "chat.message", body: "real" },
    ];
    const { handlers, api, calls, sender } = await load(root, inbox);
    const delivered = [];
    const ctx = session();
    sender.sendUserMessage = (text, options) => delivered.push({ text, options });
    await handlers.get("session_start")({}, ctx);
    assert.equal(await api.poll(ctx), true);
    assert.equal(delivered.length, 1);
    assert.match(delivered[0].text, /real/);
    assert.doesNotMatch(delivered[0].text, /closed|refused|does not open/);
    assert.deepEqual((await calls()).filter((c) => c.args[0] === "mark-read").map((c) => c.args), [["mark-read", "evt_ok"]]);
    // Only diagnostics left: no hand-off and no acknowledgement, poll after poll.
    await writeInbox(path.join(root, "inbox.json"), inbox.slice(0, 2));
    const before = (await calls()).length;
    assert.equal(await api.poll(ctx), false);
    assert.equal(await api.poll(ctx), false);
    assert.equal(delivered.length, 1);
    assert.ok(!(await calls()).slice(before).some((c) => c.args[0] === "mark-read"), "nothing acknowledged");
  } finally {
    await rmRoot(root, true);
  }
});

test("the same box under another relay row is one hand-off, and its acknowledgement follows the new row", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-pi-"));
  try {
    const scope = "chat-7|abcd|0123456789abcdef0123456789abcdef";
    const inbox = [{ id: "evt_first", from_agent: "bob", correlation_id: "chat-7", type: "chat.message", body: "once", scope }];
    const { handlers, api, calls, sender } = await load(root, inbox);
    const delivered = [];
    const ctx = session();
    sender.sendUserMessage = (text) => delivered.push(text);
    await handlers.get("session_start")({}, ctx);
    await writeFile(process.env.A2A_TEST_ACK_FAILS, "");
    assert.equal(await api.poll(ctx), true);
    assert.equal(delivered.length, 1);
    // The fold now keeps an identical copy of the box under a new row; the
    // acknowledgement is still outstanding.
    await writeInbox(path.join(root, "inbox.json"), [{ ...inbox[0], id: "evt_copy" }]);
    assert.equal(await api.poll(ctx), false, "no second hand-off for the same box");
    assert.equal(delivered.length, 1);
    await rm(process.env.A2A_TEST_ACK_FAILS, { force: true });
    assert.equal(await api.poll(ctx), false);
    const acks = (await calls()).filter((c) => c.args[0] === "mark-read");
    assert.deepEqual(acks.at(-1).args, ["mark-read", "evt_copy"], "retried with the row the fold keeps now");
    assert.deepEqual(JSON.parse(await readFile(path.join(root, "inbox.json"), "utf8")), []);
    assert.equal(await api.poll(ctx), false);
    assert.equal(delivered.length, 1);
  } finally {
    await rmRoot(root, true);
  }
});

test("a synchronous send failure leaves the message unread without retrying", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-pi-"));
  try {
    const inbox = [{ id: "evt_2", from_agent: "bob", correlation_id: "chat-2", type: "chat.message", body: "still there?" }];
    const { handlers, api, calls, sender } = await load(root, inbox);
    const ctx = session();
    sender.sendUserMessage = () => {
      throw new Error("session switching");
    };
    await handlers.get("session_start")({}, ctx);
    assert.equal(await api.poll(ctx), false);
    assert.equal(await api.poll(ctx), false, "uncertain send is not retried");
    assert.ok(!(await calls()).some((c) => c.args[0] === "mark-read"), "nothing acknowledged");
  } finally {
    await rmRoot(root, true);
  }
});

test("a failed acknowledgement is retried and never redelivers", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-pi-"));
  try {
    const inbox = [{ id: "evt_4", from_agent: "bob", correlation_id: "chat-4", type: "chat.message", body: "once only" }];
    const { handlers, api, calls, sender } = await load(root, inbox);
    await writeFile(process.env.A2A_TEST_ACK_FAILS, "");
    const delivered = [];
    const ctx = session();
    sender.sendUserMessage = (text) => delivered.push(text);
    await handlers.get("session_start")({}, ctx);
    assert.equal(await api.poll(ctx), true);
    assert.equal(delivered.length, 1);
    assert.equal(await api.poll(ctx), false, "the same unread event is not delivered again");
    assert.equal(delivered.length, 1);
    await rm(process.env.A2A_TEST_ACK_FAILS);
    assert.equal(await api.poll(ctx), false);
    assert.equal(delivered.length, 1);
    const acks = (await calls()).filter((c) => c.args[0] === "mark-read");
    assert.equal(acks.length, 3, "acknowledgement retried on every poll until it succeeds");
    assert.deepEqual(acks.at(-1).args, ["mark-read", "evt_4"]);
  } finally {
    await rmRoot(root, true);
  }
});

test("a wake line is one bounded line however large or multiline the body", async () => {
  const module = await import(`${pathToFileURL(path.join(here, "hooks", "a2a-pi-hook.js"))}?test=${Date.now()}-${Math.random()}`);
  const body = "x\ny ".repeat(400);
  const line = module.wakeLine({ from_agent: "alice", correlation_id: "chat-9", body });
  assert.ok(!line.includes("\n"), "no newline survives");
  assert.ok(line.length < 400 + 60, `bounded, got ${line.length}`);
  assert.match(line, /^\[a2a\] alice \(chat-9\): x y x y/);
  const text = module.wakeText([{ from_agent: "a", correlation_id: "c1", type: "chat.message", body }, { from_agent: "b", correlation_id: "c2", type: "chat.invite", body: "short" }]);
  assert.equal(text.split("\n").length, 4, "one line per event plus the two-line footer");
  const join = module.wakeLine({ from_agent: "alice", correlation_id: "c1", type: "chat.join", body: "", metadata: { member: "carol" } });
  assert.equal(join, "[a2a] alice added carol to chat c1", "a membership event is described, not left empty");
  const huge = module.wakeLine({ from_agent: "alice", correlation_id: "c1", type: "chat.join", metadata: { member: "y".repeat(10000) } });
  assert.ok(huge.length < 200, `a forged member name is bounded, got ${huge.length}`);
  const hostile = module.wakeLine({ from_agent: "al\x1bice", correlation_id: "c\r1", body: "ok\x1b[201~\x9bZ\x7f\x00 done" });
  assert.equal(hostile, "[a2a] alice (c 1): ok[201~Z done", "every C0 and C1 control is removed; line breaks become spaces");
});

test("closure and legacy agreement wakes report facts without proposing another command", async () => {
  const { wakeLine, wakeText } = await import("./hooks/a2a-pi-hook.js");
  const base = { from_agent: "alice", correlation_id: "chat-1", body: "untrusted body" };
  const closed = { ...base, type: "chat.closed" };
  assert.equal(wakeLine(closed), "[a2a] alice closed chat chat-1");
  assert.equal(wakeLine({ ...base, type: "chat.close-request" }), "[a2a] alice proposed ending chat chat-1");
  assert.equal(wakeLine({ ...base, type: "chat.close-ack" }), "[a2a] alice agreed to end chat chat-1");
  for (const type of ["chat.closed", "chat.close-request", "chat.close-ack", "chat.leave", "chat.decline"]) {
    assert.doesNotMatch(wakeText([{ ...base, type }]), /Reply|a2a (end|reply)/, type);
  }
  const hostile = wakeLine({ ...closed, from_agent: "al\x1bice", correlation_id: "c\r1" });
  assert.equal(hostile, "[a2a] alice closed chat c 1");
  assert.ok(wakeLine({ ...closed, from_agent: "a".repeat(10000), correlation_id: "c".repeat(10000) }).length < 160);
});

test("invitation and message reply guidance excludes chats closed in the batch", async () => {
  const { wakeText } = await import("./hooks/a2a-pi-hook.js");
  const base = { from_agent: "alice", correlation_id: "chat-1", body: "hello" };
  for (const type of ["chat.invite", "chat.message"]) {
    const payload = { ...base, type };
    const close = { ...base, type: "chat.closed" };
    for (const batch of [[payload, close], [close, payload]]) {
      assert.doesNotMatch(wakeText(batch), /Reply|a2a reply/, "same-chat closure suppresses reply guidance regardless of event order");
      const mixed = wakeText([...batch, { ...payload, correlation_id: "chat-2" }, { ...payload, correlation_id: "chat-2" }]);
      assert.equal(mixed.split("\n\n")[1], 'If the chat is still live and other members remain, reply to chat-2 with `a2a reply <chat> "..."`.');
    }
  }
  assert.equal(wakeText([]), "");
});

test("live joins offer reply and history guidance without directing action at a closed chat", async () => {
  const { wakeText } = await import("./hooks/a2a-pi-hook.js");
  const join = { from_agent: "alice", correlation_id: "chat-1", type: "chat.join", metadata: { member: "bob" } };
  assert.equal(wakeText([join]), '[a2a] alice added bob to chat chat-1\n\nIf the chat is still live and other members remain, reply to chat-1 with `a2a reply <chat> "..."`. Read history for chat-1 with `a2a read <chat>`.');
  const close = { ...join, type: "chat.closed" };
  for (const batch of [[join, close], [close, join]]) {
    assert.doesNotMatch(wakeText(batch), /a2a (reply|read)/, "a closed join offers no action regardless of event order");
    const liveJoin = { ...join, correlation_id: "chat-2" };
    const mixed = wakeText([...batch, liveJoin, liveJoin]);
    assert.equal(mixed.split("\n\n")[1], 'If the chat is still live and other members remain, reply to chat-2 with `a2a reply <chat> "..."`. Read history for chat-2 with `a2a read <chat>`.');
    const liveMessage = { ...join, type: "chat.message", correlation_id: "chat-3", body: "hello" };
    assert.equal(wakeText([...batch, liveMessage]).split("\n\n")[1], 'If the chat is still live and other members remain, reply to chat-3 with `a2a reply <chat> "..."`.', "a closed join never adds history guidance to an unrelated live message");
  }
});

test("a newly added Pi or omp member receives actionable guidance and is acknowledged once", async () => {
  for (const harness of ["pi", "omp"]) {
    const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-pi-"));
    try {
      const inbox = [{ id: "evt_joined", from_agent: "alice", correlation_id: "chat-joined", type: "chat.join", metadata: { member: "bob" }, body: "" }];
      const { handlers, api, calls, sender } = await load(root, inbox);
      process.env.NOISEDECK_MEMPALACE_HARNESS = harness;
      const delivered = [];
      const ctx = session();
      sender.sendUserMessage = (text, options) => delivered.push({ text, options });
      await handlers.get("session_start")({}, ctx);
      assert.equal(await api.poll(ctx), true);
      assert.equal(delivered.length, 1);
      assert.equal(delivered[0].text, '[a2a] alice added bob to chat chat-joined\n\nIf the chat is still live and other members remain, reply to chat-joined with `a2a reply <chat> "..."`. Read history for chat-joined with `a2a read <chat>`.');
      assert.equal(delivered[0].options, undefined, "idle delivery starts a turn instead of only queuing a follow-up");
      assert.equal(await api.poll(ctx), false);
      assert.equal(delivered.length, 1);
      const invocations = await calls();
      assert.deepEqual(invocations[0].args, ["hook", "session-start", "--harness", harness]);
      assert.deepEqual(invocations.filter((c) => c.args[0] === "mark-read").map((c) => c.args), [["mark-read", "evt_joined"]]);
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  }
});

test("a message followed by the sender leaving qualifies the reply without guessing the roster", async () => {
  const { wakeText } = await import("./hooks/a2a-pi-hook.js");
  const message = { from_agent: "alice", correlation_id: "chat-1", type: "chat.message", body: "my last thought" };
  const leave = { ...message, type: "chat.leave", body: "" };
  assert.equal(wakeText([message, leave]), '[a2a] alice (chat-1): my last thought\n[a2a] alice left chat chat-1\n\nIf the chat is still live and other members remain, reply to chat-1 with `a2a reply <chat> "..."`.');
  assert.equal(wakeText([leave]), "[a2a] alice left chat chat-1", "departure alone does not ask for a reply");
});

test("a close-only follow-up is acknowledged once and never asks the agent to reply", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-pi-"));
  try {
    const inbox = [{ id: "evt_closed", from_agent: "alice", correlation_id: "chat-closed", type: "chat.closed", body: "" }];
    const { handlers, api, calls, sender } = await load(root, inbox);
    const delivered = [];
    const ctx = session();
    sender.sendUserMessage = (text) => delivered.push(text);
    await handlers.get("session_start")({}, ctx);
    assert.equal(await api.poll(ctx), true);
    assert.deepEqual(delivered, ["[a2a] alice closed chat chat-closed"]);
    assert.equal(await api.poll(ctx), false);
    assert.equal(delivered.length, 1);
    assert.deepEqual((await calls()).filter((c) => c.args[0] === "mark-read").map((c) => c.args), [["mark-read", "evt_closed"]]);
  } finally {
    await rmRoot(root, true);
  }
});

test("mail that arrived while the agent was busy is delivered after agent_end, not swallowed by the stop hook", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-pi-"));
  try {
    const { handlers, api, calls, sender } = await load(root, []);
    const delivered = [];
    const ctx = session();
    sender.sendUserMessage = (text) => delivered.push(text);
    await handlers.get("session_start")({}, ctx);
    await handlers.get("agent_start")({}, ctx);
    await writeInbox(process.env.A2A_TEST_INBOX, [{ id: "evt_5", from_agent: "alice", correlation_id: "chat-5", type: "chat.message", body: "while busy" }]);
    assert.equal(await api.poll(ctx), false, "not delivered mid-turn");
    await handlers.get("agent_end")({}, ctx);
    const stop = (await calls()).find((c) => c.args[0] === "hook" && c.args[1] === "stop");
    assert.deepEqual(stop.args, ["hook", "stop", "--harness", "pi"], "the stop hook is told it is Pi, so it only announces presence");
    assert.equal(await api.poll(ctx), true);
    assert.match(delivered[0], /alice \(chat-5\): while busy/);
  } finally {
    await rmRoot(root, true);
  }
});

test("a stale gap peek never re-delivers a box the follower already handed off", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-pi-"));
  try {
    const box = { id: "evt_9", from_agent: "alice", correlation_id: "chat-9", type: "chat.invite", body: "once only", scope: "chat-9|i" };
    const { handlers, api, calls, sender } = await load(root, [box], { follow: true });
    const delivered = [];
    const ctx = session();
    sender.sendUserMessage = (text) => delivered.push(text);
    await handlers.get("session_start")({}, ctx);
    // The follower's own line delivers, and the native receipt marks it read.
    await until(() => delivered.length > 0, "follower delivery");
    await until(async () => JSON.parse(await readFile(process.env.A2A_TEST_INBOX, "utf8")).length === 0, "mark-read");
    assert.deepEqual((await calls()).filter((c) => c.args[0] === "mark-read").at(-1).args, ["mark-read", "evt_9"]);
    // A gap peek's snapshot may predate the receipt and list the box again;
    // the delivery history must refuse the second hand-off.
    await writeInbox(process.env.A2A_TEST_INBOX, [box]);
    assert.equal(await api.poll(ctx), false, "the stale snapshot delivers nothing");
    assert.equal(delivered.length, 1);
    await handlers.get("session_shutdown")({}, ctx);
  } finally {
    await rmRoot(root, true);
  }
});

test("mail that arrived while the agent was busy is replayed to it at agent end in follower mode", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-pi-"));
  try {
    const { handlers, api, calls, sender } = await load(root, [], { follow: true });
    const delivered = [];
    const ctx = session();
    sender.sendUserMessage = (text) => delivered.push(text);
    const lines = path.join(root, "follow-lines");
    await handlers.get("session_start")({}, ctx);
    await until(async () => (await readFile(path.join(root, "follow-pid"), "utf8").catch(() => "")) !== "", "follower running");
    await handlers.get("agent_start")({}, ctx);
    await writeInbox(process.env.A2A_TEST_INBOX, [{ id: "evt_7", from_agent: "bob", correlation_id: "chat-7", type: "chat.message", body: "while busy", scope: "chat-7|m" }]);
    // The follower must have emitted the busy wake, and the extension must
    // have consumed a busy snapshot without interrupting the turn: the poll
    // returns false, nothing is submitted and nothing is acknowledged while
    // the agent is busy.
    await until(async () => (await readFile(lines, "utf8").catch(() => "")).includes("evt_7"), "the busy wake was emitted");
    assert.equal(await api.poll(ctx), false, "a busy snapshot is never submitted");
    assert.equal(delivered.length, 0, "never interrupted mid-turn");
    assert.ok(!(await calls()).some((c) => c.args[0] === "mark-read"), "nothing acknowledged mid-turn");
    // The buffered wake is replayed inside agent_end itself: delivery is
    // complete by the time the handler returns, not on some later tick.
    await handlers.get("agent_end")({}, ctx);
    assert.equal(delivered.length, 1, "the replay happens within agent_end");
    assert.match(delivered[0], /bob \(chat-7\): while busy/);
    await handlers.get("session_shutdown")({}, ctx);
  } finally {
    await rmRoot(root, true);
  }
});

test("a busy agent is not interrupted and an API without sendUserMessage is skipped", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-pi-"));
  try {
    const inbox = [{ id: "evt_3", from_agent: "bob", correlation_id: "chat-3", type: "chat.message", body: "?" }];
    const { handlers, api, calls, sender } = await load(root, inbox);
    const ctx = session();
    sender.sendUserMessage = () => assert.fail("must not deliver mid-turn");
    await handlers.get("session_start")({}, ctx);
    await handlers.get("agent_start")({}, ctx);
    assert.equal(await api.poll(ctx), false);
    await handlers.get("agent_end")({}, ctx);
    delete sender.sendUserMessage;
    assert.equal(await api.poll(session()), false);
    assert.ok(!(await calls()).some((c) => c.args[0] === "inbox"), "the inbox is never even read");
  } finally {
    await rmRoot(root, true);
  }
});


test("void send without a native receipt leaves mail unread and cannot acknowledge another prompt", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-a2a-pi-"));
  try {
    const inbox = [{ id: "evt_failed", from_agent: "alice", correlation_id: "chat-failed", type: "chat.message", body: "hello" }];
    const { handlers, api, calls, sender } = await load(root, inbox);
    const ctx = session();
    sender.accept = false;
    let sent;
    let attempts = 0;
    sender.sendUserMessage = (text) => { sent = text; attempts++; };
    await handlers.get("session_start")({}, ctx);
    assert.equal(await api.poll(ctx), true);
    assert.ok(!(await calls()).some((c) => c.args[0] === "mark-read"));
    await handlers.get("before_agent_start")({ prompt: "unrelated user prompt" });
    assert.equal(await api.poll(ctx), false);
    assert.equal(attempts, 1);
    assert.ok(!(await calls()).some((c) => c.args[0] === "mark-read"));
    assert.deepEqual(JSON.parse(await readFile(path.join(root, "inbox.json"), "utf8")), inbox);
    await handlers.get("before_agent_start")({ prompt: sent });
    assert.deepEqual((await calls()).filter((c) => c.args[0] === "mark-read").map((c) => c.args), [["mark-read", "evt_failed"]]);
  } finally {
    await rmRoot(root, true);
  }
});
