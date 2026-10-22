// xterm exposes keyboard, paste, mouse, and emulator-generated replies through
// overlapping public events. The two trust classes must stay separate, or
// provider output could synthesize user input: user bytes use
// `input`; replies generated while parsing provider output use the server's
// allowlisted `reply` path.
//
// xterm 6.0.0 does not expose the source bit on its public onData event. Its
// core does, however, synchronously fire onUserInput immediately before onData
// for keyboard/paste/user-originated text. Keep that version-specific seam in
// this one module, pin the dependency, and fail closed if the seam disappears.

export type TerminalOutboundKind = "input" | "reply";

export interface TerminalOutbound {
  kind: TerminalOutboundKind;
  data: Uint8Array;
}

interface Disposable {
  dispose(): void;
}

interface PublicXtermDataSource {
  onData(fn: (data: string) => void): Disposable;
  onBinary(fn: (data: string) => void): Disposable;
}

interface XtermCoreService {
  onUserInput?: (fn: () => void) => Disposable;
}

interface XtermRuntime extends PublicXtermDataSource {
  _core?: {
    coreService?: XtermCoreService;
  };
}

export function subscribeXtermOutbound(
  terminal: PublicXtermDataSource,
  fn: (event: TerminalOutbound) => void,
): Disposable {
  const runtime = terminal as XtermRuntime;
  const coreService = runtime._core?.coreService;
  if (typeof coreService?.onUserInput !== "function") {
    throw new Error("xterm 6 user-input source signal is unavailable");
  }

  const subscriptions: Disposable[] = [];
  let nextDataIsUserInput = false;
  try {
    subscriptions.push(assertDisposable(coreService.onUserInput(() => {
      nextDataIsUserInput = true;
    })));
    subscriptions.push(assertDisposable(terminal.onData((data) => {
      // xterm marks SGR mouse reports as user input even though the protocol
      // and server classify those emulator reports as allowlisted replies.
      // All other marked data is keyboard/paste/control input. Unmarked data
      // always takes the reply path, where the server drops unknown shapes.
      const kind = nextDataIsUserInput && !isSGRMouseReply(data)
        ? "input"
        : "reply";
      nextDataIsUserInput = false;
      fn({ kind, data: new TextEncoder().encode(data) });
    })));
    // onBinary is xterm's byte-preserving legacy mouse-report path. Mouse
    // reports are emulator replies, not user typing; the backend's fixed
    // allowlist decides which encodings may actually reach tmux.
    subscriptions.push(assertDisposable(terminal.onBinary((data) => {
      fn({ kind: "reply", data: binaryStringToBytes(data) });
    })));
  } catch (cause) {
    for (const subscription of subscriptions.reverse()) subscription.dispose();
    throw cause;
  }

  return {
    dispose: () => {
      nextDataIsUserInput = false;
      for (const subscription of subscriptions.splice(0).reverse()) {
        subscription.dispose();
      }
    },
  };
}

function isSGRMouseReply(data: string): boolean {
  return /^\x1b\[<\d+;\d+;\d+[Mm]$/.test(data);
}

function assertDisposable(value: unknown): Disposable {
  if (
    typeof value !== "object" || value === null ||
    typeof (value as { dispose?: unknown }).dispose !== "function"
  ) {
    throw new Error("xterm event subscription is unavailable");
  }
  return value as Disposable;
}

function binaryStringToBytes(data: string): Uint8Array {
  const bytes = new Uint8Array(data.length);
  for (let i = 0; i < data.length; i++) {
    bytes[i] = data.charCodeAt(i) & 0xff;
  }
  return bytes;
}
