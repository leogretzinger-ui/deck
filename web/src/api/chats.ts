// The chat ledger: what the console knows about
// the chats a session's agent is in, as `a2a ledger` exports it. One
// delivery state per recipient; the browser folds them, never the server.
export type ChatDeliveryState = "sent" | "delivered" | "uncertain" | "read";

export type ChatState = "invited" | "waiting" | "open" | "closing" | "closed" | "left" | "declined" | "forced";

export type ChatRowKind = "message" | "added" | "left" | "declined";

export interface ChatRow {
  id: string;
  // tag is the six-hex id the recipient sees in its terminal: a2a#3ff6ae.
  tag: string;
  kind: ChatRowKind;
  from: string;
  member: string | null;
  text: string;
  at: string;
  to: Record<string, ChatDeliveryState>;
}

export interface ChatHandshake {
  member: string;
  agreed: boolean;
  at: string | null;
  left: boolean;
  left_at: string | null;
}

export interface Chat {
  id: string;
  state: ChatState;
  waiting_s: number | null;
  forced: boolean;
  opened_by: string | null;
  opened_at: string | null;
  last: string | null;
  // The selected agent's departure time; last still describes the whole chat.
  participation_at?: string | null;
  members: string[];
  // The harness each member runs on, as its presence says; the tile
  // colours by this, never by the shape of the handle.
  harnesses?: Record<string, string>;
  left: string[];
  remaining: string[];
  close_requested_by: string | null;
  // Who ended the chat: the opener, the proposer who forced it, or the
  // member whose agreement completed the quorum. Null while the chat is
  // live, and null for a chat that ended on a departure: fewer than two
  // members remained, or everyone remaining had already agreed and the
  // departure was what completed the quorum. Nobody ended those.
  closed_by: string | null;
  // Optional for older servers; absence is not evidence of a close cause.
  close_cause?: "opener" | "quorum" | "departure" | "forced" | "declined" | null;
  messages: number;
  rows: ChatRow[];
  handshake: ChatHandshake[];
}

// Whether agent chat works on this install: the a2a client is present and
// its service answers.
export interface ChatsStatus {
  available: boolean;
}

export interface ChatLedger {
  agent: string;
  chats: Chat[];
}

// ChatIndexEntry is the content-free index of every chat on the palace:
// enough to mark a sidebar row, never a body.
export interface ChatIndexEntry {
  id: string;
  state: ChatState;
  members: string[];
  left: string[];
  last: string | null;
  last_row: string | null;
  rows: number;
}

export interface ChatIndex {
  chats: ChatIndexEntry[];
  // sessions maps a Deck session id to the effective handle presence
  // resolved for its agent — the same identity the per-session ledger is
  // read for. Absent from an older server; the caller derives the default.
  sessions?: Record<string, string>;
}
