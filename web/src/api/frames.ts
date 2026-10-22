// The terminal WebSocket protocol, mirrored from
// internal/terminal/protocol.go. Keep the two in sync: the server owns
// the contract, this file only speaks it.
export const FRAME = {
  // Client -> server.
  input: "input",
  resize: "resize",
  reply: "reply",
  heartbeat: "heartbeat",
  // scroll asks tmux to move the pane's copy-mode view through its own
  // scrollback. It carries a direction and a line count, never
  // terminal bytes.
  scroll: "scroll",
  // Server -> client.
  output: "output",
  attached: "attached",
  scrollStatus: "scroll_status",
  status: "status",
  error: "error",
} as const;

// Error codes the browser acts on, mirrored from protocol.go:
// `lease_held` triggers the takeover screen, `fenced` means this
// controller was superseded, `lease_expired` means it lost its
// own lease to the TTL with nobody displacing it, `slow_consumer` means
// the server dropped us to keep the CLI unblocked.
export const ERR = {
  leaseHeld: "lease_held",
  fenced: "fenced",
  leaseExpired: "lease_expired",
  slowConsumer: "slow_consumer",
  attachFailed: "attach_failed",
  badFrame: "bad_frame",
  notRunning: "not_running",
  internal: "internal",
} as const;

// Write outcome codes, mirrored from protocol.go: sent means tmux
// acknowledged the write, failed means a provable pre-write rejection,
// uncertain means the write could not be proven either way, interrupted
// means the connection or service was lost mid-flight, and dropped means an
// emulator reply outside the server's allowlist was refused: nothing was
// written and the attachment stays open. The server answers every input and
// reply frame with exactly one of these. None is a delivery claim.
export const WRITE = {
  sent: "sent",
  failed: "failed",
  uncertain: "uncertain",
  interrupted: "interrupted",
  dropped: "dropped",
} as const;

export interface ClientFrame {
  type: string;
  token: number;
  // generation is the session generation from the attached frame. Stamped
  // on every input and reply: the server rejects a stale generation
  // before writing.
  generation?: number;
  data?: string;
  cols?: number;
  rows?: number;
  // direction and lines describe a scroll frame: "up" or "down" moves a
  // clamped number of history lines; "live" exits the pane view.
  direction?: string;
  lines?: number;
}

export interface ServerFrame {
  type: string;
  data?: string;
  token?: number;
  // generation is the session generation the attachment was created for,
  // sent on attach.
  generation?: number;
  code?: string;
  message?: string;
  active?: boolean;
}

// bytesToBase64 encodes arbitrary bytes for JSON transport — control
// characters, escape sequences, and invalid UTF-8 alike. Chunked
// because String.fromCharCode(...bytes) blows the stack on large writes.
export function bytesToBase64(data: Uint8Array): string {
  // One binary string, one btoa: base64 is a 3-byte-grouped encoding, so
  // encoding chunks separately and concatenating corrupts every
  // boundary (8192 is not a multiple of 3).
  let s = "";
  const chunk = 8192;
  for (let i = 0; i < data.length; i += chunk) {
    s += String.fromCharCode(...data.subarray(i, i + chunk));
  }
  return btoa(s);
}

// base64ToBytes is the exact inverse of bytesToBase64.
export function base64ToBytes(b64: string): Uint8Array {
  const s = atob(b64);
  const out = new Uint8Array(s.length);
  for (let i = 0; i < s.length; i++) {
    out[i] = s.charCodeAt(i);
  }
  return out;
}

// encodeFrame serializes a client frame. Nothing here validates content —
// that is the server's job.
export function encodeFrame(frame: ClientFrame): string {
  return JSON.stringify(frame);
}

// decodeFrame parses one server frame, refusing anything that does not
// carry a known type. A frame we cannot name is a protocol bug, and
// swallowing it would hide a version mismatch from everyone.
export function decodeFrame(raw: string): ServerFrame {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    throw new Error("malformed frame: not JSON");
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    throw new Error("malformed frame: not an object");
  }
  const type = (parsed as { type?: unknown }).type;
  if (
    type !== FRAME.output &&
    type !== FRAME.attached &&
    type !== FRAME.scrollStatus &&
    type !== FRAME.status &&
    type !== FRAME.error
  ) {
    throw new Error(`malformed frame: unknown type ${String(type)}`);
  }
  return parsed as ServerFrame;
}
