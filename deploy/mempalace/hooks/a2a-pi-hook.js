// a2a chat for Pi-family harnesses (pi, omp).
//
// Two jobs. Lifecycle events become presence (`a2a hook ...`), the same as
// the shell hooks on Claude Code. And because a Pi extension can hand the
// agent a user message directly, this file is also the courier's rung for
// Pi: while the agent is idle it peeks at the inbox and delivers anything
// addressed to this session as a new user prompt. A message is marked
// read only when a native before_agent_start hook confirms the exact prompt.
// A send with no receipt stays unread and is not submitted again.

import { spawn } from "node:child_process";

const A2A = process.env.A2A_BIN ?? "/usr/local/bin/a2a";
const PYTHON = process.env.NOISEDECK_MEMPALACE_PYTHON ?? "/usr/bin/python3";
const HOOK_TIMEOUT_MS = 5000;
// A mailbox read folds the relay's rooms, and those grow with the fleet:
// a peek that would have returned in a second when the palace was young can
// take tens of seconds a few weeks later. Killing it at the hook budget
// made the extension see an empty inbox forever — mail arrived and no turn
// started — so mailbox reads get their own, generous budget.
const INBOX_TIMEOUT_MS = 30000;
const INBOX_POLL_MS = Number(process.env.A2A_PI_POLL_MS ?? 4000);

function harness(ctx) {
  const configured = process.env.NOISEDECK_MEMPALACE_HARNESS;
  if (configured === "pi" || configured === "omp") return configured;
  const transcript = ctx?.sessionManager?.getSessionFile?.() ?? "";
  return transcript.includes("/.omp/") ? "omp" : "pi";
}

function run(args, stdin, timeoutMs) {
  return new Promise((resolve) => {
    let settled = false;
    let out = "";
    let timer;
    const child = spawn(PYTHON, [A2A, ...args], { stdio: ["pipe", "pipe", "ignore"] });
    const finish = (code) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve({ out, ok: code === 0 });
    };
    timer = setTimeout(() => {
      child.kill("SIGKILL");
      finish(null);
    }, timeoutMs);
    child.stdout.on("data", (chunk) => {
      out += chunk;
    });
    child.once("error", () => finish(null));
    child.once("close", (code) => finish(code));
    child.stdin.once("error", () => {});
    child.stdin.end(stdin);
  });
}

function payload(ctx) {
  return JSON.stringify({
    session_id: ctx?.sessionManager?.getSessionId?.() ?? "",
    cwd: ctx?.sessionManager?.getCwd?.() ?? "",
  });
}

const WAKE_BODY_CHARS = 400;

// One bounded line per event, the same contract as the courier's wake line:
// whitespace collapsed, body truncated, so a Logstream body cannot push
// hundreds of lines into the agent's prompt.
// eslint-disable-next-line no-control-regex
const CONTROL = /[\x00-\x1f\x7f\x80-\x9f]/g;
const inert = (value) => String(value ?? "").replace(/\s+/g, " ").replace(CONTROL, "").trim();
const HANDLE_CHARS = 64;
const handle = (value) => {
  const text = inert(value);
  return text.length <= HANDLE_CHARS ? text : text.slice(0, HANDLE_CHARS) + "…";
};

export function wakeLine(e) {
  const kind = String(e.type ?? "").split(".")[1] ?? "";
  const who = handle(e.from_agent);
  const cid = handle(e.correlation_id);
  if (kind === "join") return `[a2a] ${who} added ${handle(e.metadata?.member)} to chat ${cid}`;
  if (kind === "leave") return `[a2a] ${who} left chat ${cid}`;
  if (kind === "closed") return `[a2a] ${who} closed chat ${cid}`;
  if (kind === "close-request") return `[a2a] ${who} proposed ending chat ${cid}`;
  if (kind === "close-ack") return `[a2a] ${who} agreed to end chat ${cid}`;
  let body = inert(e.body ?? e.type);
  if (body.length > WAKE_BODY_CHARS) body = body.slice(0, WAKE_BODY_CHARS) + "…";
  return `[a2a] ${who} (${cid}): ${body}`;
}

export function wakeText(events) {
  const closed = new Set(events.filter((e) => e.type === "chat.closed").map((e) => e.correlation_id));
  const live = events.filter((e) => !closed.has(e.correlation_id));
  const replyable = new Set(live
    .filter((e) => e.type === "chat.invite" || e.type === "chat.message" || e.type === "chat.join")
    .map((e) => e.correlation_id));
  const lines = events.map(wakeLine).join("\n");
  if (replyable.size === 0) return lines;
  const joined = new Set(live.filter((e) => e.type === "chat.join").map((e) => e.correlation_id));
  const history = joined.size === 0 ? "" : ` Read history for ${[...joined].map(handle).join(", ")} with \`a2a read <chat>\`.`;
  return lines + `\n\nIf the chat is still live and other members remain, reply to ${[...replyable].map(handle).join(", ")} with \`a2a reply <chat> "..."\`.` + history;
}

export default function (pi) {
  let idle = false;
  let polling = false;
  let pending = null;
  let shuttingDown = false;
  let whileBusy = null;
  // Delivered but not yet acknowledged on the Logstream. A message stays
  // here until mark-read succeeds, so a failed acknowledgement is retried
  // and never turns into a second delivery.
  // Attempted hand-offs are filed by the box's own identity (its scope),
  // never by the relay row that carried it: a replay of the same box under
  // another row is the same hand-off, delivered once. The map keeps the
  // newest row for each, which is what an acknowledgement is retried with.
  const unacked = new Map();
  const keyOf = (e) => (typeof e.scope === "string" && e.scope ? e.scope : e.id);
  // Boxes this session has already handed to the harness, acknowledged or
  // not. A gap or catch-up peek can return a snapshot taken before a
  // follower line delivered the same box and its receipt marked it read:
  // the in-flight trackers are empty again by then, so only this history
  // stops the stale snapshot from submitting the box twice. Box scopes are
  // immutable, so an entry is wrong only if the session itself was reborn.
  const delivered = new Set();
  const rememberDelivered = (events) => {
    for (const e of events) delivered.add(keyOf(e));
    while (delivered.size > 4096) delivered.delete(delivered.values().next().value);
  };

  const hook = (event, ctx) => run(["hook", event, "--harness", harness(ctx)], payload(ctx), HOOK_TIMEOUT_MS);

  // One wake's mail, from a follower line or a peek alike: settle what an
  // earlier delivery left unacknowledged, deliver what is fresh while the
  // agent is idle, and file the hand-off as pending until the harness's
  // native receipt acknowledges it.
  async function deliver(events) {
    if (!idle && Array.isArray(events) && events.some((e) => e && typeof e.id === "string")) {
      // Wakes are dropped by the idle guards below, and the next line only
      // comes with the next event; the turn's end replays the newest one,
      // exactly as the poll interval's first tick after idle used to.
      whileBusy = events;
    }
    if (polling || pending || !idle || typeof pi.sendUserMessage !== "function") return false;
    if (!Array.isArray(events)) return false;
    polling = true;
    try {
      // The wake may list messages delivered on an earlier one whose
      // acknowledgement failed; those are settled here, never delivered again.
      const settled = new Set();
      if (unacked.size) {
        for (const e of events) if (unacked.has(keyOf(e))) unacked.set(keyOf(e), e.id);
        const ack = await run(["mark-read", ...unacked.values()], "", INBOX_TIMEOUT_MS);
        if (ack.ok) {
          for (const key of unacked.keys()) settled.add(key);
          unacked.clear();
        }
      }
      // Only verified events are delivered or acknowledged; a diagnostic
      // record, should one ever appear here, is neither.
      const seenNow = new Set();
      const fresh = events.filter((e) => e && typeof e.id === "string" && !e.untrusted && !e.unreadable && !delivered.has(keyOf(e)) && !unacked.has(keyOf(e)) && !settled.has(keyOf(e)) && !seenNow.has(keyOf(e)) && seenNow.add(keyOf(e)));
      if (!fresh.length || !idle) return false;
      rememberDelivered(fresh);
      // Pi and OMP return void here: asynchronous send failures are reported
      // by the harness, not returned to this caller. Only the native receipt
      // below can acknowledge these rows. Keep an uncertain send pending so
      // the next inbox check cannot inject it twice.
      pending = { text: wakeText(fresh), events: fresh };
      try {
        pi.sendUserMessage(pending.text);
      } catch {
        return false;
      }
      return true;
    } finally {
      polling = false;
    }
  }

  async function poll(ctx) {
    if (polling || pending || !idle || typeof pi.sendUserMessage !== "function") return false;
    const peek = await run(["inbox", "--peek", "--json"], "", INBOX_TIMEOUT_MS);
    let events = [];
    try {
      events = peek.ok ? JSON.parse(peek.out || "[]") : [];
    } catch {
      events = [];
    }
    return deliver(events);
  }

  // The follower is one long-lived `a2a inbox --follow --json` child: the
  // relay's own wait wakes it the moment mail lands, so delivery no longer
  // waits out a poll interval. Its stdin is a pipe this process holds open
  // and never writes: if the harness dies without a shutdown event, the
  // child sees EOF and exits rather than lingering. While it is down —
  // restarting after a crash, backing off after relay downtime — one peek
  // covers the gap, and the interval remains as the fallback when
  // following is switched off (`A2A_PI_FOLLOW=0`).
  let follower = null;
  let followerRestart = null;
  let followerBackoffMs = 1000;
  let interval = null;

  function startInterval(ctx) {
    if (interval !== null) return;
    interval = setInterval(() => poll(ctx), INBOX_POLL_MS);
    interval.unref?.();
  }

  function retireFollower(ctx, proc) {
    if (follower !== proc) return;
    follower = null;
    if (shuttingDown) return;
    void poll(ctx);  // cover the gap while no one is following
    const wait = followerBackoffMs;
    followerBackoffMs = Math.min(followerBackoffMs * 2, 30000);
    followerRestart = setTimeout(() => startFollower(ctx), wait);
    followerRestart.unref?.();
  }

  function startFollower(ctx) {
    if (shuttingDown) return;
    if (process.env.A2A_PI_FOLLOW === "0" || typeof pi.sendUserMessage !== "function") {
      startInterval(ctx);
      return;
    }
    let proc;
    try {
      proc = spawn(PYTHON, [A2A, "inbox", "--follow", "--json"], {
        stdio: ["pipe", "pipe", "ignore"],
        env: { ...process.env, A2A_FOLLOW_PARENT_STDIN: "1" },
      });
    } catch {
      startInterval(ctx);
      return;
    }
    follower = proc;
    // Unref'd so a host that forgets the shutdown event can still exit:
    // the pipe closing at exit is itself the child's death signal.
    proc.unref?.();
    proc.stdout.unref?.();
    proc.stdin.unref?.();
    proc.stdin.on("error", () => {});  // the child may be gone already
    let buffered = "";
    proc.stdout.setEncoding("utf8");
    proc.stdout.on("data", (chunk) => {
      buffered += chunk;
      let newline;
      while ((newline = buffered.indexOf("\n")) >= 0) {
        const line = buffered.slice(0, newline).trim();
        buffered = buffered.slice(newline + 1);
        if (!line) continue;
        followerBackoffMs = 1000;  // a line proved the pipe is alive
        let events = [];
        try {
          events = JSON.parse(line);
        } catch {
          events = [];
        }
        void deliver(events);
      }
    });
    proc.once("error", () => retireFollower(ctx, proc));
    proc.once("close", () => retireFollower(ctx, proc));
  }

  pi.on("before_agent_start", async (event) => {
    if (!pending || event.prompt !== pending.text) return;
    const accepted = pending.events;
    pending = null;
    for (const e of accepted) unacked.set(keyOf(e), e.id);
    const ack = await run(["mark-read", ...accepted.map((e) => e.id)], "", INBOX_TIMEOUT_MS);
    if (ack.ok) for (const e of accepted) unacked.delete(keyOf(e));
  });

  pi.on("session_start", async (_event, ctx) => {
    await hook("session-start", ctx);
    idle = true;
    startFollower(ctx);
  });
  pi.on("agent_start", async (_event, ctx) => {
    idle = false;
    await hook("prompt", ctx);
  });
  pi.on("agent_end", async (_event, ctx) => {
    idle = true;
    await hook("stop", ctx);
    if (whileBusy !== null && !pending) {
      const buffered = whileBusy;
      whileBusy = null;
      await deliver(buffered);
    }
  });
  pi.on("session_shutdown", async (_event, ctx) => {
    shuttingDown = true;
    idle = false;
    clearTimeout(followerRestart);
    if (follower !== null) follower.kill();
    clearInterval(interval);
    await hook("session-end", ctx);
  });

  return { poll, deliver };
}
