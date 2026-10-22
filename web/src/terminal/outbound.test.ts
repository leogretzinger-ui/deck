import { test } from "node:test";
import assert from "node:assert/strict";
import {
  subscribeXtermOutbound,
  type TerminalOutbound,
} from "./outbound.ts";

class FakeXterm {
  private userFns = new Set<() => void>();
  private dataFns = new Set<(data: string) => void>();
  private binaryFns = new Set<(data: string) => void>();

  readonly _core = {
    coreService: {
      onUserInput: (fn: () => void) => subscription(this.userFns, fn),
    },
  };

  onData(fn: (data: string) => void) {
    return subscription(this.dataFns, fn);
  }

  onBinary(fn: (data: string) => void) {
    return subscription(this.binaryFns, fn);
  }

  userData(data: string): void {
    for (const fn of this.userFns) fn();
    for (const fn of this.dataFns) fn(data);
  }

  replyData(data: string): void {
    for (const fn of this.dataFns) fn(data);
  }

  binaryData(data: string): void {
    for (const fn of this.binaryFns) fn(data);
  }

  listenerCount(): number {
    return this.userFns.size + this.dataFns.size + this.binaryFns.size;
  }
}

function subscription<T>(listeners: Set<T>, fn: T) {
  listeners.add(fn);
  return { dispose: () => listeners.delete(fn) };
}

test("separates user data from every xterm-generated reply path", () => {
  const terminal = new FakeXterm();
  const events: TerminalOutbound[] = [];
  const sub = subscribeXtermOutbound(terminal, (event) => events.push(event));

  terminal.userData("λ");
  terminal.replyData("\x1b[0n");
  terminal.replyData("\x1b[?1;2c");
  terminal.replyData("\x1b[12;40R");
  terminal.replyData("unsupported machine response");
  terminal.userData("\x1b[<0;4;5M");
  terminal.userData("\x03");
  terminal.userData("\x1b[200~pasted\x1b[201~");
  terminal.binaryData(String.fromCharCode(0, 127, 128, 255));

  assert.deepEqual(events.map((event) => event.kind), [
    "input",
    "reply",
    "reply",
    "reply",
    "reply",
    "reply",
    "input",
    "input",
    "reply",
  ]);
  assert.deepEqual(events[0]!.data, new TextEncoder().encode("λ"));
  assert.deepEqual(events[1]!.data, new TextEncoder().encode("\x1b[0n"));
  assert.deepEqual(events[4]!.data, new TextEncoder().encode("unsupported machine response"));
  assert.deepEqual(events[5]!.data, new TextEncoder().encode("\x1b[<0;4;5M"));
  assert.deepEqual(events[8]!.data, Uint8Array.from([0, 127, 128, 255]));

  sub.dispose();
  assert.equal(terminal.listenerCount(), 0);
  terminal.userData("ignored");
  assert.equal(events.length, 9);
});

test("fails closed when xterm's input-source signal is unavailable", () => {
  const terminal = {
    onData: (_fn: (data: string) => void) => ({ dispose() {} }),
    onBinary: (_fn: (data: string) => void) => ({ dispose() {} }),
  };
  assert.throws(
    () => subscribeXtermOutbound(terminal, () => {}),
    /user-input source signal is unavailable/,
  );
});
