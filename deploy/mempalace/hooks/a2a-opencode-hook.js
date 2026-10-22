// a2a chat for OpenCode sessions.
//
// Two jobs, the same two as the Pi extension. Lifecycle events become
// presence (`a2a hook ...` subprocess), the same as the shell hooks on
// Claude Code. And because an OpenCode plugin can hand the session a prompt
// through its own server API, this file is also the courier's rung for
// OpenCode: while the session is idle it peeks the inbox and delivers
// anything addressed to this session as a new prompt. A message is marked
// read only when the admitted prompt itself exists, correlated by the id
// this plugin chose for it; a send with no receipt stays unread and is not
// submitted again. Attempts are persisted before submission and reconciled
// after a reload or a restart, so an uncertain or unacknowledged box is
// never delivered twice however the process died.

import { spawn } from "node:child_process";
import { mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const A2A = process.env.A2A_BIN ?? "/usr/local/bin/a2a";
const PYTHON = process.env.NOISEDECK_MEMPALACE_PYTHON ?? "/usr/bin/python3";
const HOOK_TIMEOUT_MS = 5000;
// A mailbox read folds the relay's rooms, and those grow with the fleet:
// a peek that would have returned in a second when the palace was young can
// take tens of seconds a few weeks later. Killing it at the hook budget
// made the plugin see an empty inbox forever — mail arrived and no prompt
// was submitted — so mailbox reads get their own, generous budget.
const INBOX_TIMEOUT_MS = 30000;
const INBOX_POLL_MS = Number(process.env.A2A_OPENCODE_POLL_MS ?? 4000);
const STATE_DIR = process.env.A2A_STATE_DIR || join(homedir(), ".a2a");

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

function wakeLine(e) {
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

function wakeText(events) {
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

const sessionOf = (event) =>
  event?.properties?.sessionID ?? event?.properties?.info?.id ?? event?.properties?.session?.id ?? "";

export const A2AChat = async ({ client, directory }) => {
  let sessionID = "";
  let idle = false;
  let polling = false;
  // scope key -> {messageID, ids, receipted, at}, persisted before every
  // submission and reconciled at every poll and after every restart:
  // an uncertain attempt is never resubmitted, a receipted one is
  // acknowledged until the Logstream takes it, and both die only there.
  const attempts = new Map();
  const keyOf = (e) => (typeof e.scope === "string" && e.scope ? e.scope : e.id);
  // Boxes this session has already submitted, receipted or not. A gap or
  // catch-up peek can return a snapshot from before a follower line
  // submitted the same box and the receipt settled its journal entry: the
  // journal is empty again by then, so only this history stops the stale
  // snapshot from submitting the box twice. The journal on disk still owns
  // restart safety; this owns wake-interleaving safety in this process.
  const delivered = new Set();
  const rememberDelivered = (events) => {
    for (const e of events) delivered.add(keyOf(e));
    while (delivered.size > 4096) delivered.delete(delivered.values().next().value);
  };

  const attemptsPath = () => join(STATE_DIR, `opencode-attempts-${sessionID}.json`);
  // The journal is the no-repeat guarantee: a submission is attempted only
  // once its attempt is durable, so persist answers whether the write
  // landed and every caller holds the submission when it did not.
  const persist = () => {
    const temporary = attemptsPath() + ".tmp";
    try {
      mkdirSync(STATE_DIR, { recursive: true, mode: 0o700 });
      writeFileSync(temporary, JSON.stringify(Object.fromEntries(attempts)));
      renameSync(temporary, attemptsPath());
      return true;
    } catch {
      return false;
    }
  };
  // restore answers whether the journal's state is KNOWN. Absent is a fresh
  // start; unreadable, corrupt, or a single malformed entry is unknown, and
  // unknown never delivers — an attempt it cannot see is an attempt it
  // must not repeat.
  const restore = () => {
    attempts.clear();
    let raw;
    try {
      raw = readFileSync(attemptsPath(), "utf8");
    } catch (exc) {
      return exc?.code === "ENOENT";
    }
    let doc;
    try {
      doc = JSON.parse(raw);
    } catch {
      return false;
    }
    if (!doc || typeof doc !== "object" || Array.isArray(doc)) return false;
    for (const [key, attempt] of Object.entries(doc)) {
      if (
        !attempt ||
        typeof attempt !== "object" ||
        Array.isArray(attempt) ||
        typeof attempt.messageID !== "string" ||
        !Array.isArray(attempt.ids) ||
        !attempt.ids.every((id) => typeof id === "string") ||
        typeof attempt.receipted !== "boolean"
      ) {
        return false;
      }
      attempts.set(key, attempt);
    }
    return true;
  };

  const hook = (event) => run(["hook", event, "--harness", "opencode"], JSON.stringify({ session_id: sessionID, cwd: directory ?? "" }), HOOK_TIMEOUT_MS);

  // The plugin serves one top-level session for the directory's lifetime.
  // A resumed conversation never fires session.created, so the first
  // lifecycle event naming a session adopts it — after the API confirms it
  // has no parent, so a subagent's child session can never become the
  // delivery target or replace it. Adoption never guesses the session is
  // idle: only a fresh session.created or an explicit idle signal allows
  // delivery. Every lifecycle signal from the moment adoption starts until
  // it settles — probe window and announcement window alike — is merged in
  // order, never overwritten; the signal that initiated adoption is one of
  // them, so its branch never replays over a newer signal.
  let adopting = false;
  let adoptingId = "";
  let pendingSignal = null;
  async function adopt(id, fresh) {
    if (!id || sessionID || adopting) return false;
    shuttingDown = false;  // a new session in this directory follows again
    adopting = true;
    adoptingId = id;
    try {
      let info;
      try {
        const res = await client.session.get({ path: { id } });
        info = res?.data ?? null;
      } catch {
        info = null;
      }
      if (!info || info.parentID) return false;
      sessionID = id;
      if (!restore()) {
        sessionID = "";
        return false;  // the journal's state is unknown: the rung stays closed
      }
      await hook("session-start");
      const signal = pendingSignal;
      if (signal === "busy" || signal === "retry" || signal === "prompted") idle = false;
      else if (signal === "idle") idle = true;
      else idle = fresh === true;
      startFollower();
      return true;
    } finally {
      adopting = false;
      adoptingId = "";
      pendingSignal = null;
    }
  }

  let messageSeq = 0;

  async function receiptOf(messageID, tries = 10) {
    // The prompt route answers a bare 204 whether or not the prompt is
    // later admitted. The only native receipt is the admitted message
    // itself, correlated by the id this plugin chose for it.
    for (let attempt = 0; attempt < tries; attempt++) {
      try {
        const res = await client.session.message({ path: { id: sessionID, messageID } });
        if (res?.data && !res?.error) return true;
      } catch {
        // not there yet, or the session is gone; the next attempt asks again
      }
      if (attempt + 1 < tries) await new Promise((resolve) => setTimeout(resolve, 500));
    }
    return false;
  }

  // Every poll first settles what earlier attempts left behind, persisted
  // or fresh: an unreceipted attempt asks for its receipt again, a
  // receipted one asks for its acknowledgement again — with the row the
  // fold keeps now, never the row that carried the box first. Neither is
  // ever submitted to the session a second time. A box settled in this
  // poll is in the settled set: the peek that already listed it does not
  // become a new delivery.
  async function reconcile(events, settled) {
    for (const [key, attempt] of attempts) {
      const rows = events.filter((e) => keyOf(e) === key).map((e) => e.id);
      if (rows.length) attempt.ids = rows;
      if (!attempt.receipted && !(await receiptOf(attempt.messageID, 2))) continue;
      attempt.receipted = true;
      const ack = await run(["mark-read", ...attempt.ids], "", INBOX_TIMEOUT_MS);
      if (ack.ok) {
        attempts.delete(key);
        settled.add(key);
        // This session handed the box off in an earlier life; a slow gap
        // peek that captured it before this acknowledgement must not
        // submit it again now that the journal entry is gone.
        delivered.add(key);
      }
      persist();
    }
  }

  // One wake's mail, from a follower line or a peek alike. The journal is
  // the no-repeat guarantee across both: a box already staged, submitted or
  // receipted is never submitted again, whichever path woke the plugin.
  async function deliverEvents(events) {
    if (!idle && sessionID && Array.isArray(events) && events.some((e) => e && typeof e.id === "string")) {
      // Wakes are dropped by the idle guards below, and the next line only
      // comes with the next event; the idle signal replays the newest one,
      // exactly as the poll interval's first tick after idle used to.
      whileBusy = events;
    }
    if (polling || !idle || !sessionID) return false;
    if (!Array.isArray(events)) return false;
    polling = true;
    try {
      const settled = new Set();
      await reconcile(events, settled);
      // Only verified events are delivered or acknowledged; a diagnostic
      // record, should one ever appear here, is neither.
      const seenNow = new Set();
      const fresh = events.filter((e) => e && typeof e.id === "string" && !e.untrusted && !e.unreadable && !delivered.has(keyOf(e)) && !attempts.has(keyOf(e)) && !settled.has(keyOf(e)) && !seenNow.has(keyOf(e)) && seenNow.add(keyOf(e)));
      if (!fresh.length || !idle) return false;
      const text = wakeText(fresh);
      const messageID = `msg_a2a${Date.now().toString(36)}${(messageSeq += 1).toString(36)}`;
      const submitted = fresh.map((e) => keyOf(e));
      const staged = submitted.map((key) => [key, { messageID, ids: fresh.filter((e) => keyOf(e) === key).map((e) => e.id), receipted: false, at: Date.now() }]);
      for (const [key, attempt] of staged) attempts.set(key, attempt);
      if (!persist()) {
        // The attempt is not durable: submitting now could deliver twice
        // across a restart. Roll the staging back; the next poll tries the
        // write again.
        for (const [key] of staged) attempts.delete(key);
        return false;
      }
      let accepted = false;
      let refused = false;
      try {
        const result = await client.session.promptAsync({
          path: { id: sessionID },
          body: { messageID, parts: [{ type: "text", text }] },
        });
        refused = result != null && Boolean(result.error);
        accepted = result != null && !result.error;
      } catch {
        // The call's own outcome is unknown; the attempts stay uncertain.
      }
      if (accepted && (await receiptOf(messageID))) {
        for (const key of submitted) attempts.get(key).receipted = true;
        persist();
        rememberDelivered(fresh);
        const ack = await run(["mark-read", ...fresh.map((e) => e.id)], "", INBOX_TIMEOUT_MS);
        if (ack.ok) for (const key of submitted) attempts.delete(key);
        persist();
        return true;
      }
      if (refused) {
        // A definitive refusal admitted nothing: the boxes may be retried,
        // so they are not remembered as delivered.
        for (const key of submitted) attempts.delete(key);
        persist();
        return false;
      }
      // Accepted or uncertain: this session handed the box off, and a stale
      // peek snapshot must never hand it off a second time.
      rememberDelivered(fresh);
      return false;
    } finally {
      polling = false;
    }
  }

  async function poll() {
    if (polling || !idle || !sessionID) return false;
    const peek = await run(["inbox", "--peek", "--json"], "", INBOX_TIMEOUT_MS);
    let events = [];
    try {
      events = peek.ok ? JSON.parse(peek.out || "[]") : [];
    } catch {
      events = [];
    }
    return deliverEvents(events);
  }

  // The follower is one long-lived `a2a inbox --follow --json` child: the
  // relay's own wait wakes it the moment mail lands, so delivery no longer
  // waits out a poll interval. Its stdin is a pipe this process holds open
  // and never writes: if the host process dies, the child sees EOF and
  // exits rather than lingering. While it is down one peek covers the gap;
  // the interval stays as the fallback when following is switched off
  // (`A2A_OPENCODE_FOLLOW=0`).
  let follower = null;
  let followerRestart = null;
  let followerBackoffMs = 1000;
  let timer = null;
  let shuttingDown = false;
  let whileBusy = null;

  const pollEvery = () => {
    if (timer === null) {
      timer = setInterval(() => {
        void poll();
      }, INBOX_POLL_MS);
      timer.unref?.();
    }
  };

  function retireFollower(proc) {
    if (follower !== proc) return;
    follower = null;
    if (shuttingDown) return;
    void poll();  // cover the gap while no one is following
    const wait = followerBackoffMs;
    followerBackoffMs = Math.min(followerBackoffMs * 2, 30000);
    followerRestart = setTimeout(startFollower, wait);
    followerRestart.unref?.();
  }

  function startFollower() {
    if (shuttingDown) return;
    if (process.env.A2A_OPENCODE_FOLLOW === "0") {
      pollEvery();
      return;
    }
    let proc;
    try {
      proc = spawn(PYTHON, [A2A, "inbox", "--follow", "--json"], {
        stdio: ["pipe", "pipe", "ignore"],
        env: { ...process.env, A2A_FOLLOW_PARENT_STDIN: "1" },
      });
    } catch {
      pollEvery();
      return;
    }
    follower = proc;
    // Unref'd so a host that forgets the delete event can still exit: the
    // pipe closing at exit is itself the child's death signal.
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
        void deliverEvents(events);
      }
    });
    proc.once("error", () => retireFollower(proc));
    proc.once("close", () => retireFollower(proc));
  }

  function stopFollowing() {
    shuttingDown = true;
    clearTimeout(followerRestart);
    if (follower !== null) follower.kill();
    clearInterval(timer);
  }

  const signalOf = (type, event) => {
    if (type === "session.next.prompted") return "prompted";
    if (type === "session.idle") return "idle";
    if (type === "session.status") {
      const status = event?.properties?.status?.type ?? event?.properties?.status;
      if (status === "busy" || status === "retry" || status === "idle") return status;
    }
    return null;
  };

  return {
    event: async ({ event }) => {
      const type = event?.type ?? "";
      if (type === "session.created") {
        const info = event?.properties?.info ?? {};
        if (info.parentID) return;  // a subagent's child is never the target
        await adopt(info.id ?? sessionOf(event), true);
        return;
      }
      const from = sessionOf(event);
      if (adopting) {
        // Any signal for the adoption in flight is merged, whatever window
        // it lands in; nothing else runs until adoption settles.
        if (!from || from === adoptingId) pendingSignal = signalOf(type, event) ?? pendingSignal;
        return;
      }
      if (!sessionID) {
        if (type === "session.updated" || type === "session.idle" || type === "session.next.prompted" || type === "session.status") {
          pendingSignal = signalOf(type, event);
          await adopt(from, false);
          // The initiating event joined the merge as one signal; its branch
          // never replays over a newer one.
          return;
        }
        return;
      }
      if (from && from !== sessionID) return;  // another session's lifecycle is not ours
      if (type === "session.next.prompted") {
        idle = false;
        await hook("prompt");
        return;
      }
      if (type === "session.status") {
        const status = event?.properties?.status?.type ?? event?.properties?.status;
        if (status === "busy" || status === "retry") idle = false;
        else if (status === "idle") idle = true;
        return;
      }
      if (type === "session.idle") {
        idle = true;
        await hook("stop");
        if (whileBusy !== null && sessionID) {
          const buffered = whileBusy;
          whileBusy = null;
          await deliverEvents(buffered);
        }
        return;
      }
      if (type === "session.deleted" || type === "session.error") {
        idle = false;
        if (type === "session.deleted") {
          stopFollowing();
        } else if (follower !== null) {
          // An errored session may recover into idle: stop the child but
          // leave the door open, so the restart backoff or the next
          // adoption re-arms delivery instead of silencing it forever.
          follower.kill();
        }
        await hook("session-end");
      }
    },
    poll,
    deliverEvents,
  };
};
