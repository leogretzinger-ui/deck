// The Chats ledger model: what the
// console knows about the chats the active session's agent is in. DOM-free
// so the node test runner can drive every fold. The server hands over one
// delivery state per recipient and the chat's own state; everything the
// panel shows beyond that — the row's word, whether it has settled, the
// time stamps, the hover record, the member line, the news sentence,
// unseen — is folded here, once, and the DOM shell only draws it.
import * as hints from "../../lib/hints.ts";
import { agentHandle } from "../../lib/agent-handle.ts";
import { ApiError } from "../../api/http.ts";
import type { ApiClient } from "../../api/contracts.ts";
import type { Chat, ChatDeliveryState, ChatIndex, ChatLedger, ChatRow } from "../../api/chats.ts";

// "off" is an install without a working agent chat service, as opposed to
// "unavailable", a read that failed on one that has it.
export type ChatsPanelState = "loading" | "ready" | "unavailable" | "off" | "no-agent";

// The courier's order. "Worst" is the earliest of these, and uncertain
// outranks everything: it is the one state you may need to act on.
const RANK: Record<ChatDeliveryState, number> = { uncertain: -1, sent: 0, delivered: 1, read: 2 };
const LIVE = new Set(["invited", "waiting", "open", "closing"]);

export interface ChatsPanelCallbacks {
  onError?: (message: string, cause: unknown) => void;
  onChange?: () => void;
}

export class ChatsPanelModel {
  state: ChatsPanelState = "loading";
  agent = "";
  chats: Chat[] = [];
  error: string | null = null;
  private client: ApiClient;
  private sessionID: string;
  private cb: ChatsPanelCallbacks;
  private refreshSeq = 0;
  statusTimeoutMS = 8000;
  // seen holds, per live chat, the id of the last row you have had
  // in front of you: the panel was open when it arrived, or was opened
  // afterwards. A row newer than that is unseen (shown as an
  // accent, no count). Browser-local, per mount; never sent anywhere.
  private seen: Map<string, string | null>;
  private panelOpen = false;

  // seen may be shared: the shell keeps one map per session across mounts,
  // so returning to a session does not make its rows unseen again, and the
  // sidebar mark reads the same map the trigger does.
  constructor(client: ApiClient, sessionID: string, cb: ChatsPanelCallbacks = {}, seen: Map<string, string | null> = new Map()) {
    this.client = client;
    this.sessionID = sessionID;
    this.cb = cb;
    this.seen = seen;
  }

  seenLastRow(chatID: string): string | null | undefined {
    return this.seen.get(chatID);
  }

  async refresh(): Promise<void> {
    const seq = ++this.refreshSeq;
    let ledger: ChatLedger;
    try {
      ledger = await this.client.getSessionChats(this.sessionID);
    } catch (cause) {
      if (seq !== this.refreshSeq) return;
      if (cause instanceof ApiError && cause.code === "no_agent_handle") {
        this.state = "no-agent";
        this.chats = [];
        this.error = null;
      } else if (this.state === "ready") {
        // A read that fails after the panel had data keeps the data: a
        // flapping read never blanks the ledger.
        this.error = cause instanceof Error ? cause.message : String(cause);
        this.cb.onError?.("The chat ledger could not be read", cause);
      } else {
        const off = await this.agentChatOff();
        if (seq !== this.refreshSeq) return;
        this.state = off ? "off" : "unavailable";
        this.error = cause instanceof Error ? cause.message : String(cause);
      }
      this.cb.onChange?.();
      return;
    }
    if (seq !== this.refreshSeq) return;
    this.state = "ready";
    this.error = null;
    this.agent = ledger.agent;
    this.chats = ledger.chats;
    if (this.panelOpen) this.markSeen();
    this.cb.onChange?.();
  }

  private async agentChatOff(): Promise<boolean> {
    try {
      return (await this.client.getChatsStatus?.(AbortSignal.timeout(this.statusTimeoutMS)))?.available === false;
    } catch {
      return false;
    }
  }

  // The panel opening or already being open is what makes a row seen.
  setPanelOpen(open: boolean): void {
    this.panelOpen = open;
    if (open) this.markSeen();
  }

  private markSeen(): void {
    for (const chat of this.chats) {
      this.seen.set(chat.id, chat.rows.at(-1)?.id ?? null);
    }
  }

  get live(): Chat[] {
    return this.chats.filter((c) => LIVE.has(c.state));
  }

  get earlier(): Chat[] {
    return this.chats.filter((c) => !LIVE.has(c.state));
  }

  // unseen: any live chat holds a row newer than the last one you
  // had in front of you. A chat never opened counts from its first row.
  get unseen(): boolean {
    return this.live.some((chat) => this.chatUnseen(chat));
  }

  chatUnseen(chat: Chat): boolean {
    const last = chat.rows.at(-1)?.id;
    if (last === undefined) return false;
    if (!this.seen.has(chat.id)) return true;
    return this.seen.get(chat.id) !== last;
  }

  // The news sentence: the active agent's chat, in the
  // courier's words. Null when the agent is in no live chat.
  get news(): string | null {
    const chat = this.live[0];
    if (chat === undefined || this.agent === "") return null;
    const others = chat.members.filter((m) => m !== this.agent && !chat.left.includes(m));
    let sentence = others.length === 0
      ? `${this.agent} is in a chat with no other participants`
      : `${this.agent} is in a chat with ${others.join(", ")}`;
    if (chat.state === "waiting") sentence += ` · waiting ${formatWait(chat.waiting_s)}`;
    else {
      const uncertain = chat.rows.filter((r) => worst(r) === "uncertain");
      const last = uncertain.at(-1);
      if (last !== undefined) sentence += ` · ${last.tag} written, no echo seen`;
    }
    return sentence;
  }
}

// worst folds a row's per-recipient states to the one word the row shows.
// A row with no recipients (an add or a leave seen by nobody yet) has no
// word.
export function worst(row: ChatRow): ChatDeliveryState | null {
  const states = Object.values(row.to);
  const first = states[0];
  if (first === undefined) return null;
  return states.reduce((w, s) => (RANK[s] < RANK[w] ? s : w), first);
}

// breakdown is the per-recipient truth in the courier's register, for the
// info bar: `codex-9f2a read · kimi-2b77 uncertain`.
export function breakdown(row: ChatRow): string {
  return Object.entries(row.to)
    .map(([handle, state]) => `${handle} ${state}`)
    .join(" · ");
}

// stateWord is the chat's word: `waiting · 3m` carries its age.
export function stateWord(chat: Chat): string {
  if (chat.state === "waiting") return `waiting · ${formatWait(chat.waiting_s)}`;
  return chat.state;
}

export function formatWait(seconds: number | null): string {
  if (seconds === null || seconds < 60) return "< 1m";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  return `${Math.floor(minutes / 60)}h`;
}

// Explain the observed close without implying agreement from a departure.
export function endedHint(chat: Chat, agent = "This agent"): string {
  if (chat.state === "left") return hints.chatLeftAt(agent, clock(chat.participation_at ?? null));
  const at = clock(chat.last);
  if (chat.state === "declined") return hints.chatDeclinedAt(chat.left[0] ?? "the invitee", at);
  if (chat.state === "forced") return hints.chatForcedAt(chat.close_requested_by ?? "the proposer", at);
  if (chat.close_cause === "departure") return hints.chatEndedByDeparturesAt(at);
  if (chat.close_cause === "opener") return hints.chatClosedByOpenerAt(chat.closed_by ?? chat.opened_by ?? "The opener", at);
  if (chat.close_cause === "quorum") return hints.chatEndedAt(at);
  return hints.chatClosedAt(at);
}

// clock renders a Logstream timestamp as HH:MM in your zone, or
// HH:MM:SS for a row. The server's second precision is the ledger's own.
export function clock(iso: string | null, seconds = false): string {
  if (iso === null) return "";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  const parts = [date.getHours(), date.getMinutes()].map((n) => String(n).padStart(2, "0"));
  if (seconds) parts.push(String(date.getSeconds()).padStart(2, "0"));
  return parts.join(":");
}

// chatHandles folds the index's session map into the per-session handle
// resolution: presence's word for the session's agent wins, the default
// derivation (harness and session-id tag) otherwise, so the sidebar marks
// the same identity the panel's ledger is read for.
export function chatHandles(index: ChatIndex, sessions: { id: string; provider: string }[]): Map<string, string> {
  const handles = new Map<string, string>();
  for (const session of sessions) {
    const handle = index.sessions?.[session.id] ?? agentHandle(session);
    if (handle !== null) handles.set(session.id, handle);
  }
  return handles;
}

// chatMarks folds the content-free index into one mark per session id,
// given each session's agent handle. Unseen is decided the same way the
// panel decides it for the active session, from the ids the shell has
// recorded as seen for that session's chats.
export function chatMarks(
  index: ChatIndex,
  handles: Map<string, string>,
  seenLastRow: (sessionID: string, chatID: string) => string | null | undefined,
): Map<string, "live" | "unseen" | null> {
  const marks = new Map<string, "live" | "unseen" | null>();
  for (const [sessionID, handle] of handles) {
    let mark: "live" | "unseen" | null = null;
    for (const chat of index.chats) {
      if (!LIVE.has(chat.state) || !chat.members.includes(handle) || chat.left.includes(handle)) continue;
      const seen = seenLastRow(sessionID, chat.id);
      const unseen = chat.rows > 0 && (seen === undefined || seen !== chat.last_row);
      mark = unseen ? "unseen" : (mark ?? "live");
      if (mark === "unseen") break;
    }
    marks.set(sessionID, mark);
  }
  return marks;
}

// rowSettled: nobody owes anything on this row. A read message dims; an
// add or a leave is a fact and reads quiet from the start; a row with no
// recipients has nobody to wait on.
export function rowSettled(row: ChatRow): boolean {
  const w = worst(row);
  return row.kind !== "message" || w === null || w === "read";
}

// rowWord is the messages-app rule: a word only while something is owed,
// and only on the newest row, where the courier's progress is read.
// Uncertain is the failure case and keeps its word on any row.
export function rowWord(row: ChatRow, newest: boolean): ChatDeliveryState | null {
  const w = worst(row);
  if (w === "uncertain") return w;
  if (w === null || rowSettled(row) || !newest) return null;
  return w;
}

// Time is a stamp, not a per-row caption: one before the first row and one
// wherever five minutes or more pass. A row whose time cannot be read gets
// no stamp of its own.
export const STAMP_GAP_MS = 5 * 60 * 1000;

export function stampBefore(previous: ChatRow | undefined, row: ChatRow): boolean {
  const b = Date.parse(row.at);
  if (Number.isNaN(b)) return false;
  if (previous === undefined) return true;
  const a = Date.parse(previous.at);
  if (Number.isNaN(a)) return false;
  return b - a >= STAMP_GAP_MS;
}

// rowRecord is the hover record: the id leads, the second-precise time
// follows unless every row already shows its own, then the per-recipient
// breakdown. Message ids never sit in the pane; this is where they live.
export function rowRecord(row: ChatRow, detail: boolean): string {
  const parts = [row.tag];
  if (!detail) parts.push(clock(row.at, true));
  parts.push(breakdown(row) || hints.CHAT_SEEN_BY_NOBODY);
  return parts.join(" · ");
}

// present lists the members still in the chat, opener first.
export function present(chat: Chat): string[] {
  return chat.members.filter((m) => !chat.left.includes(m));
}

// The member line names up to three. From four the opener keeps its handle
// and the rest fold to tiles and a count until the line is expanded. A
// live chat's line is its present members; an ended chat's is everyone who
// was in it, the leavers included, since nobody is in it now.
export interface MemberLine {
  named: string[];
  folded: string[];
  more: string;
  foldable: boolean;
}

export function memberLine(chat: Chat, expanded: boolean, members: string[] = present(chat)): MemberLine {
  const foldable = members.length > 3;
  const fold = foldable && !expanded;
  return {
    named: fold ? members.slice(0, 1) : members,
    folded: fold ? members.slice(1) : [],
    more: fold ? `+${members.length - 1}` : "",
    foldable,
  };
}

// memberHint is the header's hover record: the chat id, who opened it and
// when, then every member by handle.
export function memberHint(chat: Chat): string {
  const opened = `opened ${clock(chat.opened_at)}${chat.opened_by === null ? "" : ` by ${chat.opened_by}`}`;
  return [chat.id, opened, ...present(chat)].join(" · ");
}
