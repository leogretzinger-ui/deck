// Modeled DOM regressions for the recovery pane under the status poll. Use the
// real recovery renderers and extract only the workspace methods under test:
// importing the full workspace would load terminal/browser dependencies into
// node --test. A real browser probe still owns layout and scrolling.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { stripTypeScriptTypes } from "node:module";
import test from "node:test";
import type { SessionDetail } from "../api/sessions.ts";
import {
  RECOVERY_MONO, recoveryBannerHtml, recoveryFromSession, recoveryRegisterHtml, recoveryStreamHtml,
} from "./recovery.ts";

const source = readFileSync(new URL("./live.ts", import.meta.url), "utf8");

const datasetKey = (attr: string): string => attr.slice(5).replace(/-([a-z])/g, (_, c: string) => c.toUpperCase());

class FakeElement {
  dataset: Record<string, string> = {};
  children: FakeElement[] = [];
  className = "";
  textContent = "";
  scrollTop = 0;
  matches(selector: string): boolean {
    const match = /^\[(data-[\w-]+)\]$/.exec(selector);
    return match !== null && Object.hasOwn(this.dataset, datasetKey(match[1]!));
  }
  querySelector(selector: string): FakeElement | null {
    for (const node of this.children) {
      if (node.matches(selector)) return node;
      const nested = node.querySelector(selector);
      if (nested !== null) return nested;
    }
    return null;
  }
  querySelectorAll(selector: string): FakeElement[] {
    return this.children.flatMap(node => [...(node.matches(selector) ? [node] : []), ...node.querySelectorAll(selector)]);
  }
  prepend(node: FakeElement): void { this.children.unshift(node); }
}

class FakePane extends FakeElement {
  mounts = 0;
  set innerHTML(html: string) {
    this.mounts++;
    this.children = [];
    // Flat: every tag carrying a data hook becomes a direct child, fresh per write.
    for (const tag of html.matchAll(/<(button|span|div|p)\b([^>]*)>/g)) {
      const node = new FakeElement();
      for (const attr of tag[2]!.matchAll(/\b(data-[\w-]+)(?:="([^"]*)")?/g)) node.dataset[datasetKey(attr[1]!)] = attr[2] ?? "";
      if (Object.keys(node.dataset).length > 0) this.children.push(node);
    }
  }
}

interface Harness {
  renderRecovery(detail: SessionDetail): void;
  refreshStatus(epoch: number): Promise<void>;
}

function method(name: string): string {
  const start = Math.max(source.indexOf(`  private ${name}(`), source.indexOf(`  private async ${name}(`));
  assert.ok(start >= 0, `workspace method ${name} exists`);
  const end = source.indexOf("\n  private ", start + 1);
  assert.ok(end > start, `workspace method ${name} has a bounded end`);
  return source.slice(start, end).trim();
}

function session(fields: Partial<SessionDetail>): SessionDetail {
  return { id: "s1", project_id: "p1", provider: "claude", status: "exited", exit_code: 1, ...fields } as SessionDetail;
}

function fixture(first: SessionDetail) {
  const pane = new FakePane();
  const screens: Array<(screen: string) => void> = [];
  let binds = 0;
  const collaborators = {
    recoveryFromSession, recoveryBannerHtml, recoveryStreamHtml, recoveryRegisterHtml, RECOVERY_MONO,
    region: () => pane,
    isVerifiedRunningSession: () => false,
    document: { createElement: () => new FakeElement() },
  };
  const names = ["renderRecovery", "recoveryKeyOf", "mountRetainedScreen", "refreshStatus"];
  const code = stripTypeScriptTypes(`class RecoveryHarness {\n${names.map(method).join("\n")}\n}`);
  const Constructor = new Function(...Object.keys(collaborators), `${code}\nreturn RecoveryHarness;`)(...Object.values(collaborators)) as new () => Harness;
  const workspace = new Constructor();
  let next = first;
  const statusModel = { session: first, async refresh() { statusModel.session = next; } };
  Object.assign(workspace, {
    root: {}, disposed: false, epoch: 1, terminal: null, statusModel, recoveryKey: null,
    api: { sessionScreen: () => new Promise<string>(resolve => { screens.push(resolve); }) },
    syncDock() {}, applyTerminalGround() {}, refreshOpenPopover() {}, renderChrome() {},
    bindRecoveryActions() { binds++; },
  });
  return {
    workspace, pane, screens,
    get binds(): number { return binds; },
    get scrollback(): FakeElement { const node = pane.querySelector("[data-recovery-scrollback]"); assert.ok(node); return node; },
    async poll(detail: SessionDetail): Promise<void> { next = detail; await workspace.refreshStatus(1); },
  };
}

const settle = async (): Promise<void> => { for (let i = 0; i < 4; i++) await Promise.resolve(); };

test("an unchanged recovery poll keeps the pane, its scroll and one screen fetch", async () => {
  const ended = (): SessionDetail => session({ presentation_state: "resume_ended", native_session_id: "abc123", recovery_refusal: "provider" });
  const f = fixture(ended());
  f.workspace.renderRecovery(ended());
  f.screens[0]!("provider stopped here");
  await settle();
  const scrollback = f.scrollback;
  scrollback.scrollTop = 500;
  for (let i = 0; i < 5; i++) await f.poll(ended());
  assert.equal(f.pane.mounts, 1);
  assert.equal(f.scrollback, scrollback);
  assert.equal(scrollback.scrollTop, 500);
  assert.equal(f.binds, 1);
  assert.equal(f.screens.length, 1);
  assert.equal(f.pane.querySelectorAll("[data-recovery-screen]").length, 1);
});

test("a changed recovery presentation rebuilds the pane", async () => {
  const f = fixture(session({ presentation_state: "recovering", resume_attempts: 1 }));
  f.workspace.renderRecovery(session({ presentation_state: "recovering", resume_attempts: 1 }));
  await f.poll(session({ presentation_state: "recovering", resume_attempts: 1 }));
  assert.equal(f.pane.mounts, 1);
  await f.poll(session({ presentation_state: "recovering", resume_attempts: 2 }));
  assert.equal(f.pane.mounts, 2);
  await f.poll(session({ presentation_state: "attachment_unavailable" }));
  assert.equal(f.pane.mounts, 3);
  assert.equal(f.binds, 3);
});

test("overlapping screen fetches mount the retained screen once", async () => {
  const f = fixture(session({ presentation_state: "resume_ended", recovery_refusal: "provider" }));
  f.workspace.renderRecovery(session({ presentation_state: "resume_ended", recovery_refusal: "provider" }));
  await f.poll(session({ presentation_state: "resume_ended", recovery_refusal: "store_gone" }));
  assert.equal(f.pane.mounts, 2);
  assert.equal(f.screens.length, 2);
  f.screens[0]!("provider stopped here");
  f.screens[1]!("provider stopped here");
  await settle();
  assert.equal(f.scrollback.querySelectorAll("[data-recovery-screen]").length, 1);
});
