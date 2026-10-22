import type { SessionSummary } from "../api/sessions.ts";

// Acknowledgment is about the exact visible snapshot, never selection alone.
// Pending input/approval is deliberately unaffected by this controller.
export class CompletionReads {
  private readonly send: (id: string, generation: number, revision: string) => Promise<void>;
  private readonly pending = new Set<string>();
  private readonly seen = new Set<string>();
  private disposed = false;

  constructor(send: (id: string, generation: number, revision: string) => Promise<void>) { this.send = send; }

  async observe(session: SessionSummary | undefined, visible: boolean, changed: () => void): Promise<void> {
    if (this.disposed || !visible || !session?.completion_unread || !session.completion_revision) return;
    const revision = session.completion_revision;
    const key = `${session.id}:${session.generation}:${revision}`;
    if (this.seen.has(key)) { session.completion_unread = false; changed(); return; }
    if (this.pending.has(key)) return;
    this.pending.add(key);
    try {
      await this.send(session.id, session.generation, revision);
      if (this.disposed) return;
      this.seen.add(key);
      if (this.seen.size > 128) this.seen.delete(this.seen.values().next().value!);
      if (session.completion_revision === revision) session.completion_unread = false;
      changed();
    } catch { /* Keep unread on failure; the next fresh render/reconnect retries. */ }
    finally { this.pending.delete(key); }
  }
  dispose(): void { this.disposed = true; this.seen.clear(); }
}

export function conversationVisible(root: HTMLElement): boolean {
  if (document.visibilityState !== "visible" || !root.isConnected) return false;
  const pane = root.querySelector<HTMLElement>('[data-region="terminal"]');
  if (!pane || pane.getBoundingClientRect().width <= 0 || pane.getBoundingClientRect().height <= 0
    || getComputedStyle(pane).visibility === "hidden") return false;
  if (root.querySelector('[data-region="files"][data-open="true"][data-expanded="true"]')) return false;
  const narrow = window.matchMedia("(width < 48rem)").matches || root.closest('[data-viewport="narrow"]') !== null;
  if (narrow && (root.querySelector('[data-workspace][data-collapsed="false"]')
    || root.querySelector('aside[data-open="true"]'))) return false;
  return !Array.from(root.querySelectorAll<HTMLElement>('[role="dialog"], [aria-modal="true"]'))
    .some(dialog => dialog.getClientRects().length > 0 && getComputedStyle(dialog).visibility !== "hidden");
}
