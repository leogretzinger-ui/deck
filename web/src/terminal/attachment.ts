// AgentAttachment is one live terminal attachment's protocol state:
// fencing token, heartbeat, and frame routing. It is deliberately free of
// DOM and xterm — the browser wiring (xterm instance, ResizeObserver,
// WebSocket) lives in attach.ts, so this logic runs under plain node
// tests.
//
// The authority invariant holds here too: this class transports bytes
// and never interprets them. It does not parse terminal output, does not
// answer terminal queries, and does not know what a shell is.
import {
  FRAME,
  ERR,
  WRITE,
  bytesToBase64,
  base64ToBytes,
  encodeFrame,
  decodeFrame,
  type ClientFrame,
} from "../api/frames.ts";
import type { TerminalOutbound } from "./outbound.ts";
import type { ScrollCommand } from "./scroll.ts";

// Transport is the smallest socket shape the attachment needs. The real
// implementation wraps WebSocket; tests inject a fake.
export interface Transport {
  send(raw: string): void;
  close(): void;
  onMessage(fn: (raw: string) => void): void;
  onClose(fn: () => void): void;
}

// TerminalLike is what the attachment needs of the terminal itself:
// bytes in, source-classified outbound events, and a size to report.
export interface TerminalLike {
  write(data: Uint8Array): void;
  onOutbound(fn: (event: TerminalOutbound) => void): { dispose(): void };
  readonly cols: number;
  readonly rows: number;
}

export interface AttachmentCallbacks {
  // The controller lease is held elsewhere: the UI shows the takeover
  // screen.
  onTakeoverRequired?(message: string): void;
  // This controller was superseded or revoked: stop everything.
  onFenced?(message: string): void;
  // onLeaseExpired is this controller losing its OWN lease to the TTL,
  // with nobody having taken the session. Distinct from onFenced because
  // reclaiming an idle session of your own is safe.
  onLeaseExpired?(message: string): void;
  // We were dropped for not draining: the UI may reconnect.
  onDropped?(message: string): void;
  // Any other server error.
  onError?(message: string): void;
  // The attachment is live. The fencing token stays inside: the
  // attachment stamps its own frames with it and no caller reads it.
  onAttached?(): void;
  // One input/reply write's outcome: sent, failed, uncertain, or
  // interrupted. Never a delivery claim.
  onWriteStatus?(code: string, message?: string): void;
  // tmux's observed post-command copy-mode state. A requested scroll does
  // not change browser chrome until this acknowledgment arrives.
  onScrollStatus?(active: boolean): void;
  // The transport closed for any reason.
  onClose?(): void;
}

// heartbeatMs is well under the server's 15s lease TTL (internal/lease).
const heartbeatMs = 5000;

export class AgentAttachment {
  private token = 0;
  private sessionGeneration = 0;
  private attached = false;
  private lastOutcome: { code: string; message: string } | null = null;
  // The protocol carries no write id: the server answers every input or
  // reply write with one status, in order. Counting both sides lets a
  // caller know which status is its own.
  private writesForwarded = 0;
  private statusesReceived = 0;
  private tracked: { target: number; done: (code: string, message: string) => void }[] = [];

  // writeOutcome is the last terminal write outcome (failed, uncertain,
  // or interrupted), preserved so the close path can render the specific
  // reason the server gave instead of a generic disconnect.
  writeOutcome(): { code: string; message: string } | null {
    return this.lastOutcome;
  }
  private timer: ReturnType<typeof setInterval> | null = null;
  private closed = false;
  private transport: Transport;
  private term: TerminalLike;
  private cb: AttachmentCallbacks;
  private outboundSubscription: { dispose(): void } | null = null;
  private initialSize: { cols: number; rows: number };

  // NOTE: no TS parameter properties — node's type-stripping test
  // runner accepts erasable syntax only.
  constructor(
    transport: Transport,
    term: TerminalLike,
    cb: AttachmentCallbacks = {},
    initialSize = { cols: term.cols, rows: term.rows },
  ) {
    this.transport = transport;
    this.term = term;
    this.cb = cb;
    this.initialSize = initialSize;
    transport.onMessage((raw) => this.handle(raw));
    transport.onClose(() => {
      this.closed = true;
      this.settleTracked();
      this.stopHeartbeat();
      this.disposeOutboundSubscription();
      cb.onClose?.();
    });
    this.outboundSubscription = term.onOutbound((event) => this.forward(event));
  }

  // User bytes and emulator-generated replies have separate protocol
  // paths: replies cross a strict allowlist and cannot impersonate typing.
  // Before attach completes there is no token, so bytes are dropped rather
  // than guessed or queued.
  private forward(event: TerminalOutbound): void {
    if (!this.attached || this.closed) {
      return;
    }
    this.writesForwarded++;
    this.send({
      type: event.kind === "input" ? FRAME.input : FRAME.reply,
      token: this.token,
      generation: this.sessionGeneration,
      data: bytesToBase64(event.data),
    });
  }

  // trackWrite runs send, which must write to the terminal synchronously,
  // and reports that write's own outcome to done, once. Statuses arrive in
  // write order, so the write's status is the one numbered by how many
  // writes this attachment had forwarded when it left; earlier writes'
  // statuses cannot satisfy it. When the attachment ends first, done gets
  // the server's last non-sent outcome, or "interrupted". Returns false,
  // and never calls done, when the write did not leave (not attached).
  trackWrite(send: () => void, done: (code: string, message: string) => void): boolean {
    if (!this.attached || this.closed) return false;
    const before = this.writesForwarded;
    send();
    if (this.writesForwarded !== before + 1) return false;
    this.tracked.push({ target: this.writesForwarded, done });
    return true;
  }

  private settleTracked(): void {
    const pending = this.tracked;
    this.tracked = [];
    for (const entry of pending) {
      entry.done(this.lastOutcome?.code ?? "interrupted", this.lastOutcome?.message ?? "");
    }
  }

  // scroll asks tmux to move the pane's copy-mode view. It is a
  // view command, not terminal bytes: tmux owns the pane and its
  // scrollback, so this transports a direction and an optional bounded count
  // and interprets nothing. It carries the generation like a write does, so
  // the server fences stale browser and terminal lifetimes before moving the
  // pane's view.
  // Before attach there is no token, so commands are dropped rather than
  // queued — a scroll has no meaning for a view that was never shown.
  scroll(command: ScrollCommand): void {
    if (!this.attached || this.closed) {
      return;
    }
    this.send({
      type: FRAME.scroll,
      token: this.token,
      generation: this.sessionGeneration,
      direction: command.direction,
      lines: "lines" in command ? command.lines : undefined,
    });
  }

  // resize reports the new dimensions. tmux renegotiates with the
  // provider, which is why resize works without this product
  // understanding the TUI.
  resize(): void {
    if (!this.attached || this.closed) {
      return;
    }
    this.send({ type: FRAME.resize, token: this.token, cols: this.term.cols, rows: this.term.rows });
  }

  dispose(): void {
    if (this.closed) {
      this.stopHeartbeat();
      this.disposeOutboundSubscription();
      return;
    }
    this.closed = true;
    this.settleTracked();
    this.stopHeartbeat();
    this.disposeOutboundSubscription();
    this.transport.close();
  }

  private send(frame: ClientFrame): void {
    this.transport.send(encodeFrame(frame));
  }

  // fatal reports and ends the attachment, mirroring the server closing
  // on bad frames: a protocol violation is not something to keep
  // transporting bytes around.
  private fatal(message: string): void {
    this.cb.onError?.(message);
    this.dispose();
  }

  private handle(raw: string): void {
    // WebSocket events already queued when dispose/reconnect happened must not
    // write into the shared terminal or resurrect a heartbeat.
    if (this.closed) return;

    let frame;
    try {
      frame = decodeFrame(raw);
    } catch {
      // The server says bad frames are fatal to the attachment; mirror it.
      this.cb.onError?.("unreadable frame from server");
      this.dispose();
      return;
    }

    switch (frame.type) {
      case FRAME.attached: {
        // The fencing token must be a positive integer: accepting 0, a
        // fractional, or a non-number would stamp every later frame
        // with garbage the server is right to fence.
        if (
          typeof frame.token !== "number" ||
          !Number.isInteger(frame.token) ||
          frame.token <= 0
        ) {
          this.fatal("attach completed without a valid fencing token");
          return;
        }
        if (
          typeof frame.generation !== "number" ||
          !Number.isInteger(frame.generation) ||
          frame.generation <= 0
        ) {
          this.fatal("attach completed without a valid session generation");
          return;
        }
        this.token = frame.token;
        this.sessionGeneration = frame.generation;
        this.attached = true;
        this.startHeartbeat();
        this.cb.onAttached?.();
        // The handshake created the tmux client at initialSize. A layout
        // change that raced the handshake still needs one correction, but an
        // unchanged viewport must not trigger a redundant provider redraw.
        if (this.term.cols !== this.initialSize.cols || this.term.rows !== this.initialSize.rows) {
          this.resize();
        }
        return;
      }
      case FRAME.status: {
        // Per-write outcome, forwarded so browser-owned UI can
        // surface it with the correct class. A terminal outcome (failed,
        // uncertain, interrupted) is preserved: the server closes the
        // attachment after one of those, and the close must render the
        // specific reason, not a generic disconnect.
        if (typeof frame.code !== "string" || !Object.values(WRITE).includes(frame.code as never)) {
          this.fatal("write-status frame with an unknown outcome");
          return;
        }
        // A missing or non-string reason degrades to empty; the outcome
        // class is the fact, the message only explains it.
        const reason = typeof frame.message === "string" ? frame.message : "";
        if (frame.code !== WRITE.sent && frame.code !== WRITE.dropped) {
          this.lastOutcome = { code: frame.code, message: reason };
        }
        this.statusesReceived++;
        const due = this.tracked.filter((entry) => entry.target === this.statusesReceived);
        this.tracked = this.tracked.filter((entry) => entry.target > this.statusesReceived);
        for (const entry of due) entry.done(frame.code, reason);
        this.cb.onWriteStatus?.(frame.code, reason);
        return;
      }
      case FRAME.scrollStatus: {
        if (typeof frame.active !== "boolean") {
          this.fatal("scroll-status frame without a boolean pane state");
          return;
        }
        this.cb.onScrollStatus?.(frame.active);
        return;
      }
      case FRAME.output: {
        // Malformed KNOWN frames are as fatal as undecodable ones: an
        // invalid base64 output or a non-string field is a protocol
        // violation, and quietly swallowing it would hide stream
        // corruption behind a live-looking attachment.
        if (typeof frame.data !== "string") {
          this.fatal("output frame with non-string data");
          return;
        }
        try {
          this.term.write(base64ToBytes(frame.data));
        } catch {
          this.fatal("output frame with invalid base64 data");
        }
        return;
      }
      case FRAME.error: {
        switch (frame.code) {
          case ERR.leaseHeld:
            this.cb.onTakeoverRequired?.(frame.message ?? "another controller holds the session");
            return;
          case ERR.fenced:
            this.cb.onFenced?.(frame.message ?? "controller superseded");
            this.dispose();
            return;
          case ERR.leaseExpired:
            this.cb.onLeaseExpired?.(frame.message ?? "session lease timed out");
            this.dispose();
            return;
          case ERR.slowConsumer:
            this.cb.onDropped?.(frame.message ?? "dropped for not draining");
            return;
          default:
            this.cb.onError?.(frame.message ?? "server error");
            return;
        }
      }
    }
  }

  private startHeartbeat(): void {
    this.timer = setInterval(() => {
      if (!this.closed) {
        this.send({ type: FRAME.heartbeat, token: this.token });
      }
    }, heartbeatMs);
  }

  private stopHeartbeat(): void {
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  private disposeOutboundSubscription(): void {
    this.outboundSubscription?.dispose();
    this.outboundSubscription = null;
  }
}
