import assert from "node:assert/strict";
import test from "node:test";
import type { GitHubConnectScreen } from "./model.ts";
import { githubFootHtml, githubScreenHtml } from "./view.ts";

const screens: GitHubConnectScreen[] = [
  { kind: "checking" },
  { kind: "starting", running: false },
  { kind: "starting", running: true },
  { kind: "code", code: "2DFA-5170" },
  { kind: "connected", login: "octo" },
  { kind: "missing-cli", platform: "darwin" },
  { kind: "missing-cli", platform: "linux" },
  { kind: "ended" },
  { kind: "notice", message: "Your GitHub connection needs attention. Check it with the GitHub CLI." },
  { kind: "failed", message: "Deck couldn't read your GitHub connection." },
];

function text(html: string): string {
  return html.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();
}

test("every screen shows one message, no Read status, and hinted buttons only", () => {
  for (const screen of screens) {
    const html = githubScreenHtml(screen) + githubFootHtml(screen);
    assert.equal((html.match(/data-github-message/g) ?? []).length, screen.kind === "code" ? 0 : 1, screen.kind);
    assert.ok(!/Read status|sign-in process ended|No GitHub account is connected|unavailable in this Deck instance/.test(html), screen.kind);
    for (const button of html.match(/<button[^>]*>/g) ?? []) assert.match(button, /data-hint="[^"]+"/, button);
  }
});

test("the code screen is the code, one action, and one line", () => {
  const screen: GitHubConnectScreen = { kind: "code", code: "2DFA-5170" };
  assert.equal(text(githubScreenHtml(screen)), "2DFA-5170 Copy code and open GitHub Deck connects as soon as you approve it on GitHub.");
  assert.match(githubScreenHtml(screen), /<output data-github-code[^>]*select-all[^>]*font-mono[^>]*text-title-2/);
  assert.equal(text(githubFootHtml(screen)), "Cancel");
});

test("connected names the account and says how to use a different one", () => {
  assert.equal(text(githubScreenHtml({ kind: "connected", login: "<octo>" })),
    "Connected as &lt;octo&gt; To use a different account, sign out with the GitHub CLI and connect again.");
  assert.equal(text(githubFootHtml({ kind: "connected", login: "octo" })), "Done");
});

test("checking is neutral and offers nothing", () => {
  assert.equal(text(githubScreenHtml({ kind: "checking" })), "Checking your GitHub connection…");
  assert.equal(githubFootHtml({ kind: "checking" }), "");
});

test("a missing GitHub CLI names it and gives the install step for the host", () => {
  assert.equal(text(githubScreenHtml({ kind: "missing-cli", platform: "darwin" })),
    "Deck can't find the GitHub CLI (gh). Install it, then try again. On macOS, run brew install gh .");
  assert.match(githubScreenHtml({ kind: "missing-cli", platform: "linux" }), /href="https:\/\/github.com\/cli\/cli#installation"/);
  assert.equal(text(githubFootHtml({ kind: "missing-cli", platform: "darwin" })), "Try again");
});

test("ended and failed screens offer Try again; notices offer Close", () => {
  assert.equal(text(githubScreenHtml({ kind: "ended" })), "Sign-in ended before GitHub approved it.");
  assert.equal(text(githubFootHtml({ kind: "ended" })), "Try again");
  assert.equal(text(githubFootHtml({ kind: "failed", message: "x" })), "Try again");
  assert.equal(text(githubFootHtml({ kind: "notice", message: "x" })), "Close");
  assert.equal(githubFootHtml({ kind: "starting", running: false }), "");
});
