import { test, mock } from "node:test";
import type { TestContext } from "node:test";
import assert from "node:assert/strict";
import {
  AgentAttachment,
  type Transport,
  type TerminalLike,
} from "./attachment.ts";
import { bytesToBase64, base64ToBytes, FRAME } from "../api/frames.ts";
import type { TerminalOutbound } from "./outbound.ts";

class FakeTransport implements Transport {
  sent: string[] = [];
  closed = false;
  private msgFn: ((raw: string) => void) | null = null;
  private closeFn: (() => void) | null = null;

  send(raw: string): void {
    this.sent.push(raw);
  }
  close(): void {
    this.closed = true;
  }
  onMessage(fn: (raw: string) => void): void {
    this.msgFn = fn;
  }
  onClose(fn: () => void): void {
    this.closeFn = fn;
  }
  deliver(raw: string): void {
    this.msgFn?.(raw);
  }
  drop(): void {
    this.closeFn?.();
  }

  frames(): Array<Record<string, unknown>> {
    return this.sent.map((s) => JSON.parse(s));
  }
}

class FakeTerm implements TerminalLike {
  written: Uint8Array[] = [];
  cols = 80;
  rows = 24;
  private outboundFn: ((event: TerminalOutbound) => void) | null = null;
  inputDisposed = false;

  write(data: Uint8Array): void {
    this.written.push(data);
  }
  onOutbound(fn: (event: TerminalOutbound) => void): { dispose(): void } {
    this.outboundFn = fn;
    return {
      dispose: () => {
        this.inputDisposed = true;
        this.outboundFn = null;
      },
    };
  }
  type(data: string): void {
    this.outboundFn?.({ kind: "input", data: new TextEncoder().encode(data) });
  }
  reply(data: string): void {
    this.outboundFn?.({ kind: "reply", data: new TextEncoder().encode(data) });
  }
  writtenText(): string {
    return new TextDecoder().decode(
      Uint8Array.from(this.written.flatMap((d) => Array.from(d))),
    );
  }
}

function attachedPair(t: TestContext) {
  const transport = new FakeTransport();
  const term = new FakeTerm();
  const calls: Record<string, unknown[]> = {};
  const record = (name: string) => (msg: unknown) => {
    (calls[name] ??= []).push(msg);
  };
  const att = new AgentAttachment(transport, term, {
    onAttached: () => record("attached")("attached"),
    onTakeoverRequired: record("takeover"),
    onFenced: record("fenced"),
    onDropped: record("dropped"),
    onScrollStatus: record("scrollStatus"),
    onError: record("error"),
  });
  // Attachments start a real heartbeat interval on attach; without this
  // cleanup the leaked timers keep the test process alive.
  t.after(() => att.dispose());
  return { transport, term, att, calls };
}

test("attach flow: token, then input and output flow", (t) => {
  const { transport, term, calls } = attachedPair(t);

  // Before attach completes, keystrokes are dropped (no token yet).
  term.type("x");
  assert.equal(transport.sent.length, 0);

  transport.deliver(`{"type":"attached","token":42,"generation":1}`);
  // The callback announces the attachment and carries nothing: the token
  // stays inside, and the frames below are where it has to show up.
  assert.deepEqual(calls.attached, ["attached"]);

  term.type("ls");
  const inputFrames = transport.frames().filter((f) => f.type === "input");
  assert.equal(inputFrames.length, 1);
  assert.equal(inputFrames[0]!.token, 42);
  assert.equal(inputFrames[0]!.generation, 1, "input frames stamp the attached generation");
  assert.deepEqual(
    base64ToBytes(inputFrames[0]!.data as string),
    new TextEncoder().encode("ls"),
  );

  transport.deliver(
    JSON.stringify({ type: FRAME.output, data: bytesToBase64(new TextEncoder().encode("hello")) }),
  );
  assert.equal(term.writtenText(), "hello");
});

test("an attached frame without a generation is fatal", (t) => {
  const { transport, calls } = attachedPair(t);
  transport.deliver(`{"type":"attached","token":42}`);
  assert.equal(calls.attached, undefined, "attach without a session generation must not complete");
  assert.equal(calls.error?.length, 1);
});

test("emulator replies use the allowlisted reply frame", (t) => {
  const { transport, term } = attachedPair(t);
  transport.deliver(`{"type":"attached","token":42,"generation":1}`);

  term.reply("\x1b[0n");

  const replies = transport.frames().filter((frame) => frame.type === FRAME.reply);
  assert.equal(replies.length, 1);
  assert.equal(replies[0]!.token, 42);
  assert.deepEqual(
    base64ToBytes(replies[0]!.data as string),
    new TextEncoder().encode("\x1b[0n"),
  );
  assert.equal(transport.frames().filter((frame) => frame.type === FRAME.input).length, 0);
});

test("Escape remains ordinary provider-owned terminal input", (t) => {
  const { transport, term } = attachedPair(t);
  transport.deliver(`{"type":"attached","token":42,"generation":1}`);

  term.type("\x1b");

  const inputs = transport.frames().filter((frame) => frame.type === FRAME.input);
  assert.equal(inputs.length, 1);
  assert.deepEqual(base64ToBytes(inputs[0]!.data as string), new Uint8Array([0x1b]));
  assert.equal(transport.frames().filter((frame) => frame.type === FRAME.scroll).length, 0);
});

test("attached frame without a token is fatal", (t) => {
  const { transport, calls } = attachedPair(t);
  transport.deliver(`{"type":"attached"}`);
  assert.equal(calls.error!.length, 1);
  assert.equal(transport.closed, true);
});

test("lease_held triggers the takeover path without closing", (t) => {
  const { transport, calls } = attachedPair(t);
  transport.deliver(`{"type":"attached","token":42,"generation":1}`);
  transport.deliver(`{"type":"error","code":"lease_held","message":"held"}`);
  assert.deepEqual(calls.takeover, ["held"]);
  assert.equal(transport.closed, false);
});

test("fenced closes the attachment", (t) => {
  const { transport, calls } = attachedPair(t);
  transport.deliver(`{"type":"attached","token":42,"generation":1}`);
  transport.deliver(`{"type":"error","code":"fenced","message":"superseded"}`);
  assert.deepEqual(calls.fenced, ["superseded"]);
  assert.equal(transport.closed, true);
});

test("slow_consumer drops without closing (reconnectable)", (t) => {
  const { transport, calls } = attachedPair(t);
  transport.deliver(`{"type":"attached","token":42,"generation":1}`);
  transport.deliver(`{"type":"error","code":"slow_consumer","message":"not draining"}`);
  assert.deepEqual(calls.dropped, ["not draining"]);
  assert.equal(transport.closed, false);
});

test("malformed server frame is fatal", (t) => {
  const { transport, calls } = attachedPair(t);
  transport.deliver("definitely not json");
  assert.equal(calls.error!.length, 1);
  assert.equal(transport.closed, true);
});

test("resize reports the terminal's dimensions with the token", (t) => {
  const { transport, att } = attachedPair(t);
  transport.deliver(`{"type":"attached","token":7,"generation":1}`);
  let frames = transport.frames().filter((f) => f.type === "resize");
  assert.equal(frames.length, 0, "the handshake already applied the fitted initial size");

  att.resize();
  frames = transport.frames().filter((f) => f.type === "resize");
  assert.equal(frames.length, 1);
  assert.deepEqual(frames[0], { type: "resize", token: 7, cols: 80, rows: 24 });
});

test("a geometry change during the handshake is corrected once after attach", (t) => {
  const { transport, term } = attachedPair(t);
  term.cols = 100;
  term.rows = 40;
  transport.deliver(`{"type":"attached","token":7,"generation":1}`);
  assert.deepEqual(
    transport.frames().filter((f) => f.type === "resize"),
    [{ type: "resize", token: 7, cols: 100, rows: 40 }],
  );
});

test("heartbeat keeps the lease alive on the server's cadence", (t) => {
  const timers = mock.timers;
  timers.enable({ apis: ["setInterval"] });
  try {
    const { transport } = attachedPair(t);
    transport.deliver(`{"type":"attached","token":7,"generation":1}`);
    timers.tick(4999);
    assert.equal(transport.frames().filter((f) => f.type === "heartbeat").length, 0);
    timers.tick(1);
    const beats = transport.frames().filter((f) => f.type === "heartbeat");
    assert.equal(beats.length, 1);
    assert.equal(beats[0]!.token, 7);
  } finally {
    timers.reset();
  }
});

test("dispose closes transport and stops heartbeats", (t) => {
  const timers = mock.timers;
  timers.enable({ apis: ["setInterval"] });
  try {
    const { transport, term, att } = attachedPair(t);
    transport.deliver(`{"type":"attached","token":7,"generation":1}`);
    att.dispose();
    assert.equal(transport.closed, true);
    assert.equal(term.inputDisposed, true);
    timers.tick(10000);
    assert.equal(transport.frames().filter((f) => f.type === "heartbeat").length, 0);
  } finally {
    timers.reset();
  }
});

test("queued server events after dispose cannot write or restart a heartbeat", (t) => {
  const timers = mock.timers;
  timers.enable({ apis: ["setInterval"] });
  try {
    const { transport, term, att, calls } = attachedPair(t);
    att.dispose();
    transport.deliver(JSON.stringify({
      type: FRAME.output,
      data: bytesToBase64(new TextEncoder().encode("stale")),
    }));
    transport.deliver(`{"type":"attached","token":99,"generation":1}`);
    timers.tick(10_000);

    assert.equal(term.writtenText(), "");
    assert.equal(calls.attached, undefined);
    assert.equal(transport.frames().length, 0);
  } finally {
    timers.reset();
  }
});

test("transport close releases the terminal input listener", (t) => {
  const { transport, term } = attachedPair(t);
  transport.deliver(`{"type":"attached","token":42,"generation":1}`);
  transport.drop();
  assert.equal(term.inputDisposed, true);
  term.type("ignored");
  assert.equal(
    transport.frames().filter((frame) => frame.type === FRAME.input).length,
    0,
  );
});

test("output frame with invalid base64 is fatal", (t) => {
  const { transport, calls } = attachedPair(t);
  transport.deliver(`{"type":"attached","token":7,"generation":1}`);
  transport.deliver(`{"type":"output","data":"%%%"}`);
  assert.equal(calls.error!.length, 1);
  assert.equal(transport.closed, true);
});

test("output frame with non-string data is fatal", (t) => {
  const { transport, calls } = attachedPair(t);
  transport.deliver(`{"type":"attached","token":7,"generation":1}`);
  transport.deliver(`{"type":"output","data":123}`);
  assert.equal(calls.error!.length, 1);
  assert.equal(transport.closed, true);
});

test("attached frame with a fractional or zero token is fatal", (t) => {
  for (const token of [0, 1.5, -3]) {
    const { transport, calls } = attachedPair(t);
    transport.deliver(`{"type":"attached","token":${token},"generation":1}`);
    assert.equal(calls.error!.length, 1, `token ${token}`);
    assert.equal(transport.closed, true, `token ${token}`);
  }
});

test("output frame without data is fatal", (t) => {
  const { transport, calls } = attachedPair(t);
  transport.deliver(`{"type":"attached","token":7,"generation":1}`);
  transport.deliver(`{"type":"output"}`);
  assert.equal(calls.error!.length, 1);
  assert.equal(transport.closed, true);
});

test("write-status frames forward each outcome class", (t) => {
  const transport = new FakeTransport();
  const term = new FakeTerm();
  const statuses: (string | undefined)[] = [];
  new AgentAttachment(transport, term, {
    onWriteStatus: (code) => statuses.push(code),
  });
  for (const code of ["sent", "failed", "uncertain", "interrupted"]) {
    transport.deliver(JSON.stringify({ type: FRAME.status, code }));
  }
  assert.deepEqual(statuses, ["sent", "failed", "uncertain", "interrupted"]);
});

test("scroll-status frames forward only an observed boolean pane state", (t) => {
  const { transport, calls } = attachedPair(t);
  transport.deliver(JSON.stringify({ type: FRAME.scrollStatus, active: true }));
  transport.deliver(JSON.stringify({ type: FRAME.scrollStatus, active: false }));
  assert.deepEqual(calls.scrollStatus, [true, false]);
});

test("a scroll-status frame without a boolean pane state is fatal", (t) => {
  const { transport, calls } = attachedPair(t);
  transport.deliver(JSON.stringify({ type: FRAME.scrollStatus, active: "yes" }));
  assert.equal(calls.error?.length, 1);
  assert.equal(transport.closed, true);
});

test("a terminal write outcome is preserved for the close path", (t) => {
  const transport = new FakeTransport();
  const term = new FakeTerm();
  const att = new AgentAttachment(transport, term, {});
  assert.equal(att.writeOutcome(), null);
  transport.deliver(JSON.stringify({ type: FRAME.status, code: "sent" }));
  assert.equal(att.writeOutcome(), null, "sent is not a terminal outcome");
  transport.deliver(JSON.stringify({ type: FRAME.status, code: "uncertain", message: "may not have reached the CLI" }));
  assert.deepEqual(att.writeOutcome(), { code: "uncertain", message: "may not have reached the CLI" });
});

test("a write-status frame with an unknown outcome is fatal", (t) => {
  const transport = new FakeTransport();
  const term = new FakeTerm();
  const calls: Record<string, unknown[]> = {};
  const record = (name: string) => (msg: unknown) => {
    (calls[name] ??= []).push(msg);
  };
  new AgentAttachment(transport, term, { onError: record("error") });
  transport.deliver(JSON.stringify({ type: FRAME.status, code: "delivered" }));
  assert.equal(calls.error?.length, 1, "unknown outcome code must fail closed");
});

test("zero, negative, and fractional generations are fatal on attach", (t) => {
  for (const generation of [0, -2, 1.5]) {
    const { transport, calls } = attachedPair(t);
    transport.deliver(JSON.stringify({ type: "attached", token: 42, generation }));
    assert.equal(calls.attached, undefined, `generation ${generation} must not complete attach`);
  }
});

test("a tracked write is settled by its own status, not an earlier write's", (t) => {
  const { transport, term, att } = attachedPair(t);
  transport.deliver(`{"type":"attached","token":42,"generation":1}`);
  const status = (code: string) => transport.deliver(JSON.stringify({ type: FRAME.status, code, message: "" }));
  const seen: string[] = [];
  term.type("a");
  assert.equal(att.trackWrite(() => term.type("tutorial"), (code) => seen.push(code)), true);
  status("sent");
  assert.deepEqual(seen, [], "the earlier write's status is not the tracked write's");
  status("uncertain");
  assert.deepEqual(seen, ["uncertain"]);
  status("sent");
  assert.deepEqual(seen, ["uncertain"], "settles once");
});

test("a tracked write that never gets a status settles when the attachment ends", (t) => {
  const { transport, term, att } = attachedPair(t);
  transport.deliver(`{"type":"attached","token":42,"generation":1}`);
  const seen: string[] = [];
  att.trackWrite(() => term.type("x"), (code) => seen.push(code));
  transport.drop();
  assert.deepEqual(seen, ["interrupted"]);
});

test("trackWrite reports false and never settles when the write did not leave", (t) => {
  const { term, att } = attachedPair(t);
  const seen: string[] = [];
  assert.equal(att.trackWrite(() => term.type("x"), (code) => seen.push(code)), false);
  assert.deepEqual(seen, []);
});

test("a dropped reply status keeps the tutorial write's attribution in step", (t) => {
  const { transport, term, att } = attachedPair(t);
  transport.deliver(`{"type":"attached","token":42,"generation":1}`);
  const status = (code: string) => transport.deliver(JSON.stringify({ type: FRAME.status, code, message: "" }));
  const seen: string[] = [];
  term.reply("\x1b[?25;1$y");
  att.trackWrite(() => term.type("tutorial"), (code) => seen.push(code));
  status("dropped");
  assert.deepEqual(seen, [], "the rejected reply's status is not the tutorial's");
  status("sent");
  assert.deepEqual(seen, ["sent"]);
  assert.equal(att.writeOutcome(), null, "a dropped reply is not a terminal outcome");
});
