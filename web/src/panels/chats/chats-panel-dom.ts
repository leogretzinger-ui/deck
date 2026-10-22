// The Chats ledger's DOM shell. Quiet by
// default: a row is a tile, the text in full, and a word only while
// someone still owes an acknowledgement. Read rows dim. Time is a stamp at
// the top and at any gap of five minutes; Details puts the time on every
// row and the chat id under the header. Message ids never sit in the
// pane; they lead the hover record. Nothing here is mono: the machine
// voice stays the terminal's. The shell draws what the model folded and
// owns only the Earlier disclosure, the Details setting and which member
// lines are expanded.
import { AGENT_CHAT_SETUP_SENTENCE, AGENT_CHAT_SETUP_URL } from "../../lib/agent-chat-setup.ts";
import { escapeHtml } from "../../lib/escape.ts";
import * as hints from "../../lib/hints.ts";
import { ICON_BUTTON } from "../../ui/controls.ts";
import { lucideIcon } from "../../ui/icons.ts";
import { providerTileClass } from "../../ui/session-identity.ts";
import type { Chat, ChatDeliveryState, ChatHandshake, ChatRow } from "../../api/chats.ts";
import {
  ChatsPanelModel,
  clock,
  endedHint,
  memberHint,
  memberLine,
  rowRecord,
  rowSettled,
  rowWord,
  stampBefore,
  stateWord,
  worst,
} from "./chats-panel.ts";

export interface ChatsPanelShellCallbacks {
  onClose?: () => void;
}

// Details is a pane setting, remembered per browser, off by default. A
// storage that throws leaves it off and the toggle still works for the
// mount.
export const DETAILS_STORAGE_KEY = "noisedeck.chats.details";

export function readDetails(): boolean {
  try {
    return localStorage.getItem(DETAILS_STORAGE_KEY) === "on";
  } catch {
    return false;
  }
}

export function writeDetails(on: boolean): void {
  try {
    localStorage.setItem(DETAILS_STORAGE_KEY, on ? "on" : "off");
  } catch {
    // The preference simply fails to persist.
  }
}

// A member's harness is what the ledger says its presence says. Only
// without that does the handle's shape stand in: claude-61c3 is a claude
// session, codex-9f2a a codex one. The tile borrows the sidebar's provider
// hue so the same agent reads the same in both places, and an agent the
// palette has no hue for gets the neutral, never a surface.
export function handleProvider(handle: string): string {
  const dash = handle.lastIndexOf("-");
  return dash > 0 ? handle.slice(0, dash) : handle;
}

export function memberProvider(chat: Pick<Chat, "harnesses">, handle: string): string {
  return chat.harnesses?.[handle] ?? handleProvider(handle);
}

function tile(chat: Pick<Chat, "harnesses">, handle: string, extra = ""): string {
  return `<span aria-hidden="true" data-chat-tile="${escapeHtml(memberProvider(chat, handle))}" class="inline-block h-2.5 w-2.5 flex-none rounded-[3px] ${providerTileClass(memberProvider(chat, handle))}${extra === "" ? "" : ` ${extra}`}"></span>`;
}

// A member unit: the tile and its handle, one thing, no glyph in front.
function memberUnit(chat: Pick<Chat, "harnesses">, handle: string): string {
  return `<span class="inline-flex min-w-0 items-center gap-2">${tile(chat, handle)}<span class="truncate text-body-sm font-medium text-text-primary">${escapeHtml(handle)}</span></span>`;
}

const STATE_CLASS: Record<ChatDeliveryState, string> = {
  sent: "text-text-tertiary",
  delivered: "text-text-secondary",
  read: "text-text-primary",
  uncertain: "text-warning",
};

// A list within this many pixels of its end counts as showing the newest
// row; a fractional scroll position never leaves it stranded one pixel up.
const NEWEST_SLACK_PX = 4;

interface ListPlace {
  top: number;
  newest: boolean;
}

const CHAT_STATE_CLASS: Record<string, string> = {
  invited: "text-text-tertiary",
  waiting: "text-text-secondary",
  open: "text-text-secondary",
  closing: "text-text-secondary",
  closed: "text-text-tertiary",
  left: "text-text-tertiary",
  declined: "text-text-tertiary",
  forced: "text-text-tertiary",
};

// The right column shares the message's first line: caption type on the
// body-sm line box.
const ROW_LINE = "leading-[calc(var(--text-body-sm)*1.4)]";

export class ChatsPanel {
  private container: HTMLElement;
  private model: ChatsPanelModel;
  private cb: ChatsPanelShellCallbacks;
  private earlierOpen = false;
  private details: boolean;
  // Which folded member lines you have expanded. Not remembered:
  // a fresh mount folds again.
  private expanded = new Set<string>();
  // Which ended chats you have opened to their rows. Not
  // remembered either: Earlier folds again on a fresh mount.
  private opened = new Set<string>();
  private disposed = false;
  // Whether the list shows its newest row, kept current by its scroll
  // events; the observer that holds the end in view while it does.
  private pinned = true;
  private following: ResizeObserver | null = null;

  constructor(container: HTMLElement, model: ChatsPanelModel, cb: ChatsPanelShellCallbacks = {}) {
    this.container = container;
    this.model = model;
    this.cb = cb;
    this.details = readDetails();
    this.render();
  }

  async open(): Promise<void> {
    if (this.disposed) return;
    this.model.setPanelOpen(true);
    await this.refresh();
  }

  close(): void {
    this.model.setPanelOpen(false);
  }

  async refresh(): Promise<void> {
    await this.model.refresh();
    if (this.disposed) return;
    this.render();
  }

  dispose(): void {
    this.disposed = true;
    this.following?.disconnect();
    this.following = null;
    this.container.innerHTML = "";
  }

  render(): void {
    if (this.disposed) return;
    const place = this.place();
    this.container.innerHTML = `
      <div class="relative flex min-h-0 flex-1 flex-col">
        ${this.headerHtml()}
        ${this.bodyHtml()}
      </div>
    `;
    this.restore(place);
    this.follow(place.newest);
    this.container.querySelector("[data-chats-close]")?.addEventListener("click", () => this.cb.onClose?.());
    this.container.querySelector("[data-chats-details]")?.addEventListener("click", () => {
      this.details = !this.details;
      writeDetails(this.details);
      this.rerender("[data-chats-details]");
    });
    this.container.querySelector("[data-chats-earlier-toggle]")?.addEventListener("click", () => {
      this.earlierOpen = !this.earlierOpen;
      this.rerender("[data-chats-earlier-toggle]");
    });
    for (const row of this.container.querySelectorAll<HTMLElement>("button[data-chat-earlier-open]")) {
      row.addEventListener("click", () => {
        const id = row.closest<HTMLElement>("[data-chat-earlier]")?.getAttribute("data-chat-earlier");
        if (id === null || id === undefined) return;
        if (this.opened.has(id)) this.opened.delete(id);
        else this.opened.add(id);
        this.rerender(`[data-chat-earlier="${CSS.escape(id)}"] button[data-chat-earlier-open]`);
      });
    }
    for (const line of this.container.querySelectorAll<HTMLElement>("button[data-chats-members]")) {
      line.addEventListener("click", () => {
        const id = line.closest<HTMLElement>("[data-chat]")?.getAttribute("data-chat");
        if (id === null || id === undefined) return;
        if (this.expanded.has(id)) this.expanded.delete(id);
        else this.expanded.add(id);
        this.rerender(`[data-chat="${CSS.escape(id)}"] button[data-chats-members]`);
      });
    }
  }

  // A repaint replaces the whole list, and a fresh list starts at its
  // first row. Rows run oldest to newest, so without a memory of where
  // you were, every change stream event would carry you back
  // to the chat's first message while the new one lands out of view at the
  // bottom. The list's place is read before the repaint and put back after:
  // a list that shows the newest row (including one you have never
  // scrolled, and a list not yet drawn) shows the newest row again, and a
  // list scrolled up to read stays where it is.
  private place(): ListPlace {
    const list = this.container.querySelector<HTMLElement>("[data-chats-list]");
    if (list === null) return { top: 0, newest: true };
    return { top: list.scrollTop, newest: list.scrollTop + list.clientHeight >= list.scrollHeight - NEWEST_SLACK_PX };
  }

  private restore(place: ListPlace): void {
    const list = this.container.querySelector<HTMLElement>("[data-chats-list]");
    if (list === null) return;
    list.scrollTop = place.newest ? list.scrollHeight : place.top;
  }

  // The rows keep moving after a repaint: the web font arrives and the
  // text rewraps, the panel slides open, the window resizes. Each of those
  // grows or shrinks the list under a scroll position that was its end a
  // moment ago and leaves the newest row a few lines out of view. While the
  // you have the newest row, every resize of the list or its contents
  // puts the end back in view; scrolling up releases the hold.
  private follow(newest: boolean): void {
    this.following?.disconnect();
    this.following = null;
    this.pinned = newest;
    const list = this.container.querySelector<HTMLElement>("[data-chats-list]");
    if (list === null || typeof ResizeObserver !== "function") return;
    list.addEventListener("scroll", () => {
      this.pinned = list.scrollTop + list.clientHeight >= list.scrollHeight - NEWEST_SLACK_PX;
    });
    const observer = new ResizeObserver(() => {
      if (this.pinned) list.scrollTop = list.scrollHeight;
    });
    observer.observe(list);
    for (const child of list.children) observer.observe(child);
    this.following = observer;
  }

  // A toggle replaces the markup it sits in. Focus returns to the
  // replacement control, so a keyboard user can press again and keeps
  // their place in the panel.
  private rerender(focusSelector: string): void {
    this.render();
    this.container.querySelector<HTMLElement>(focusSelector)?.focus();
  }

  private headerHtml(): string {
    return `
      <div data-chats-header class="chrome gap-2.5 pl-(--space-5) pr-(--space-4)">
        <h2 class="chrome-title flex-none">Chats</h2>
        <span class="ml-auto"></span>
        <button type="button" data-chats-details aria-pressed="${this.details}" aria-label="Details"
          data-hint="${this.details ? hints.HIDE_CHAT_DETAILS : hints.SHOW_CHAT_DETAILS}"
          class="${ICON_BUTTON} aria-pressed:bg-wash-pressed max-md:h-11 max-md:w-11">${lucideIcon("info", 14)}</button>
        <button type="button" data-chats-close aria-label="Close chats" data-hint="${hints.CLOSE_CHATS}"
          class="${ICON_BUTTON} max-md:h-11 max-md:w-11">${lucideIcon("x", 14)}</button>
      </div>
    `;
  }

  private bodyHtml(): string {
    const m = this.model;
    if (m.state === "loading") {
      return `<div class="p-5 text-body-sm text-text-tertiary">Reading the ledger…</div>`;
    }
    if (m.state === "unavailable") {
      return `<div class="p-5 text-body-sm text-text-tertiary">The chat ledger could not be read.</div>`;
    }
    if (m.state === "off") {
      const link = `<a href="${escapeHtml(AGENT_CHAT_SETUP_URL)}" target="_blank" rel="noopener noreferrer" data-hint="${hints.OPEN_AGENT_CHAT_SETUP}" class="text-accent underline underline-offset-2">Agent chat setup</a>`;
      return this.emptyHtml("Agent chat is off", `${escapeHtml(AGENT_CHAT_SETUP_SENTENCE)} ${link}`);
    }
    if (m.state === "no-agent") {
      return this.emptyHtml("No chats", "This session's harness has no chat handle.");
    }
    if (m.chats.length === 0) {
      return this.emptyHtml("No chats", `One appears here when ${escapeHtml(m.agent)} is in it.`);
    }
    const live = m.live.map((chat) => this.chatHtml(chat)).join("");
    return `
      <div data-chats-list class="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-(--space-5) pt-3.5 pb-(--space-5)">
        ${live}
        ${this.earlierHtml()}
      </div>
    `;
  }

  // The empty tier: icon, title, one line. You cannot start a
  // chat, so the empty state promises nothing; body may carry a link.
  private emptyHtml(title: string, body: string): string {
    return `
      <div data-chats-empty class="flex flex-1 flex-col items-center justify-center gap-2 p-5 text-center">
        <span class="text-text-tertiary">${lucideIcon("message-square", 26)}</span>
        <div class="text-body-sm font-medium text-text-primary">${escapeHtml(title)}</div>
        <p class="max-w-64 text-caption text-text-tertiary">${body}</p>
      </div>
    `;
  }

  // Up to three members are named. From four the opener keeps its handle
  // and the rest fold to tiles and a count, and the line becomes a
  // disclosure button that expands it in place.
  private membersHtml(chat: Chat): string {
    const expanded = this.expanded.has(chat.id);
    const line = memberLine(chat, expanded);
    const named = line.named.map((handle) => memberUnit(chat, handle)).join("");
    const folded = line.folded.length === 0
      ? ""
      : `<span data-chats-members-folded class="inline-flex items-center gap-2"><span class="inline-flex gap-1">${line.folded.map((h) => tile(chat, h)).join("")}</span><span class="text-caption text-text-tertiary">${escapeHtml(line.more)}</span></span>`;
    const layout = "flex min-w-0 flex-1 flex-wrap items-center gap-x-3.5 gap-y-1.5";
    if (!line.foldable) return `<span data-chats-members data-foldable="false" class="${layout}">${named}</span>`;
    return `<button type="button" data-chats-members data-foldable="true" aria-expanded="${expanded}"
      data-hint="${escapeHtml(memberHint(chat))}" class="${layout} cursor-pointer rounded-control-sm text-left focus-visible:bg-wash-hover">${named}${folded}</button>`;
  }

  // The Details line under a chat's header: its id, when it opened and who
  // opened it. Empty while Details is off.
  private detailHtml(chat: Chat): string {
    if (!this.details) return "";
    return `
        <div data-chat-detail class="-mt-1.5 mb-0.5 ml-[18px] flex items-baseline gap-2 whitespace-nowrap text-caption text-text-tertiary">
          <span class="flex-none">${escapeHtml(chat.id)}</span>
          <span class="min-w-0 truncate">opened ${escapeHtml(clock(chat.opened_at))}${chat.opened_by === null ? "" : ` by ${escapeHtml(chat.opened_by)}`}</span>
        </div>`;
  }

  private chatHtml(chat: Chat): string {
    const word = stateWord(chat);
    const detail = this.detailHtml(chat);
    return `
      <section data-chat="${escapeHtml(chat.id)}" data-chat-state="${escapeHtml(chat.state)}" class="flex flex-col gap-2">
        <header data-hint="${escapeHtml(memberHint(chat))}" class="flex min-h-[22px] items-center gap-2">
          ${this.membersHtml(chat)}
          <span data-chat-word class="flex-none text-caption ${CHAT_STATE_CLASS[chat.state] ?? "text-text-tertiary"}">${escapeHtml(word)}</span>
        </header>
        ${detail}
        <div data-chat-rows class="flex flex-col gap-px border-t border-border pt-1">
          ${this.rowsHtml(chat, chat.rows)}
        </div>
        ${chat.handshake.length > 0 ? this.handshakeHtml(chat, chat.handshake) : ""}
      </section>
    `;
  }

  private rowsHtml(chat: Chat, rows: ChatRow[]): string {
    const out: string[] = [];
    rows.forEach((row, i) => {
      if (!this.details && stampBefore(rows[i - 1], row)) {
        out.push(`<div data-chat-stamp class="pt-2.5 pr-1 pb-1 pl-5 text-caption text-text-tertiary">${escapeHtml(clock(row.at))}</div>`);
      }
      out.push(this.rowHtml(chat, row, i === rows.length - 1));
    });
    return out.join("");
  }

  private rowHtml(chat: Chat, row: ChatRow, newest: boolean): string {
    const w = worst(row);
    const word = rowWord(row, newest);
    const settled = rowSettled(row);
    const text = row.kind === "added"
      ? `${row.from} added ${row.member ?? ""}`
      : row.kind === "left"
        ? `${row.from} left${row.text ? `: ${row.text}` : ""}`
        : row.kind === "declined"
          ? `${row.from} declined${row.text ? `: ${row.text}` : ""}`
          : row.text;
    const time = this.details ? `<span data-chat-row-time class="text-text-tertiary">${escapeHtml(clock(row.at, true))}</span>` : "";
    return `
      <div data-chat-row="${escapeHtml(row.id)}" data-chat-row-state="${w ?? ""}" data-chat-row-settled="${settled}" data-hint="${escapeHtml(rowRecord(row, this.details))}"
        class="grid grid-cols-[10px_minmax(0,1fr)_auto] items-start gap-2.5 rounded-control-sm px-1 py-[7px] hover:bg-wash-hover">
        ${tile(chat, row.from, "mt-1")}
        <span class="min-w-0 text-body-sm leading-[1.4] text-pretty ${settled ? "text-text-tertiary" : "text-text-primary"}">${escapeHtml(text)}</span>
        <span class="flex min-w-0 flex-col items-end whitespace-nowrap text-caption ${ROW_LINE}">
          <span data-chat-row-word class="${word === null ? "" : STATE_CLASS[word]}">${word === null ? "" : escapeHtml(word)}</span>
          ${time}
        </span>
      </div>
    `;
  }

  // One handshake row per member, in the ledger's columns. Agreed rows
  // carry a check and a time; waiting rows carry nothing; a member who
  // left reads left with the time and is out of the quorum.
  private handshakeHtml(chat: Chat, handshake: ChatHandshake[]): string {
    const rows = handshake.map((h) => {
      const text = h.left ? `${h.member} left` : h.agreed ? `${h.member} agreed` : `${h.member} — waiting`;
      const at = h.left ? h.left_at : h.agreed ? h.at : null;
      const hint = h.left
        ? hints.CHAT_MEMBER_LEFT_QUORUM
        : h.agreed
          ? hints.chatMemberAgreed(h.member)
          : hints.chatMemberWaiting(h.member);
      return `
        <div data-chat-handshake="${escapeHtml(h.member)}" data-hint="${escapeHtml(hint)}" class="grid grid-cols-[10px_minmax(0,1fr)_auto] items-center gap-2.5 px-1 py-2">
          ${tile(chat, h.member)}
          <span class="flex min-w-0 items-center gap-2 truncate text-body-sm ${h.agreed ? "text-text-secondary" : "text-text-tertiary"}">
            <span class="inline-flex w-3 flex-none text-text-secondary">${h.agreed && !h.left ? lucideIcon("check", 12) : ""}</span>${escapeHtml(text)}
          </span>
          <span class="text-caption text-text-tertiary">${escapeHtml(clock(at, true))}</span>
        </div>`;
    }).join("");
    return `<div data-chat-handshake-rows class="flex flex-col gap-px border-t border-border pt-1.5">${rows}</div>`;
  }

  // An ended chat is a row that opens in place. The row is the chat's
  // member line in the header's fold (the opener named, the rest tiles and
  // a count, wrapping rather than running off the panel when the chat had
  // sixteen members), its message count and how it ended. Open, the chat's
  // rows and closing handshake read below it exactly as a live chat's do.
  private earlierChatHtml(chat: Chat): string {
    const open = this.opened.has(chat.id);
    const line = memberLine(chat, false, chat.members);
    const named = line.named.map((handle) => memberUnit(chat, handle)).join("");
    const folded = line.folded.length === 0
      ? ""
      : `<span data-chats-members-folded class="inline-flex items-center gap-2"><span class="inline-flex flex-wrap gap-1">${line.folded.map((h) => tile(chat, h)).join("")}</span><span class="text-caption text-text-tertiary">${escapeHtml(line.more)}</span></span>`;
    const count = `${chat.messages} message${chat.messages === 1 ? "" : "s"}`;
    const body = open
      ? `
        ${this.detailHtml(chat)}
        <div data-chat-rows class="flex flex-col gap-px border-t border-border pt-1">
          ${this.rowsHtml(chat, chat.rows)}
        </div>
        ${chat.handshake.length > 0 ? this.handshakeHtml(chat, chat.handshake) : ""}`
      : "";
    return `
      <section data-chat-earlier="${escapeHtml(chat.id)}" data-chat-state="${escapeHtml(chat.state)}" class="flex flex-col gap-2">
        <button type="button" data-chat-earlier-open aria-expanded="${open}" data-hint="${escapeHtml(endedHint(chat, this.model.agent))}"
          class="flex min-h-[22px] w-full min-w-0 flex-wrap items-center gap-x-3.5 gap-y-1.5 rounded-control-sm px-1 py-2 text-left hover:bg-wash-hover focus-visible:bg-wash-hover">
          <span data-chats-members class="flex min-w-0 flex-1 flex-wrap items-center gap-x-3.5 gap-y-1.5">${named}${folded}</span>
          <span class="flex-none text-caption text-text-tertiary">${count}</span>
          <span class="min-w-[52px] flex-none text-right text-caption text-text-tertiary">${escapeHtml(chat.state)}</span>
        </button>
        ${body}
      </section>`;
  }

  private earlierHtml(): string {
    const earlier = this.model.earlier;
    if (earlier.length === 0) return "";
    const rows = this.earlierOpen ? earlier.map((chat) => this.earlierChatHtml(chat)).join("") : "";
    return `
      <section data-chats-earlier class="mt-2 flex flex-col gap-1">
        <button type="button" data-chats-earlier-toggle aria-expanded="${this.earlierOpen}"
          data-hint="${this.earlierOpen ? hints.FOLD_EARLIER_CHATS : hints.UNFOLD_EARLIER_CHATS}"
          class="flex h-7 items-center gap-1.5 text-caption text-text-tertiary hover:text-text-secondary">
          ${lucideIcon(this.earlierOpen ? "chevron-down" : "chevron-right", 12)}
          <span class="uppercase tracking-wide">Earlier</span>
          <span>${earlier.length}</span>
        </button>
        ${rows}
      </section>
    `;
  }
}
