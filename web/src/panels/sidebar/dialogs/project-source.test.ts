// The project-source markup phases. These pin
// the dialog chrome both phases share, the four chooser labels, the picker's
// honest copy (a repository that never lists proves nothing about an owner's
// approval, and a partial list never reads as complete), and that a loading
// or incomplete list cannot be imported from — while Back and Close stay live.
import assert from "node:assert/strict";
import test from "node:test";
import { addProjectChooserHtml, githubRepositoryPickerHtml, type GitHubRepositoryPickerState, type GitHubRepositorySummary } from "./project-source.ts";

const repositories: readonly GitHubRepositorySummary[] = [
  { id: 101, full_name: "fable/tasks-engine", owner: "fable", private: true, description: "Task engine" },
  { id: 102, full_name: "fable/deck", owner: "fable", private: true, description: "" },
  { id: 103, full_name: "alexr/field-notes", owner: "alexr", private: false, description: "Research" },
];

function picker(over: Partial<GitHubRepositoryPickerState> = {}): GitHubRepositoryPickerState {
  return {
    account: "alexr",
    loading: false,
    checking: false,
    outcome: "",
    query: "",
    owner: "",
    repositories,
    owners: ["alexr", "fable", "northwind-labs"],
    imported: new Map([["fable/deck", { id: "p7", name: "Deck" }]]),
    complete: true,
    ...over,
  };
}

test("partial SSO shows a quiet authorization notice below the usable list", () => {
  const notice = "Some organizations need single sign-on before their repositories appear here.";
  const html = githubRepositoryPickerHtml(picker({ outcome: "ok", ssoPartial: true }));
  assert.ok(html.includes(notice));
  assert.ok(html.indexOf(notice) > html.indexOf('aria-label="GitHub repositories"'));
  assert.ok(html.includes('aria-label="Import fable/tasks-engine"'));
  assert.ok(html.includes('href="https://github.com/settings/applications" target="_blank" rel="noopener noreferrer"'));
  assert.ok(html.includes(">Authorize on GitHub</a>"));
  assert.ok(!html.includes("An organization requires single sign-on"));
  assert.ok(!githubRepositoryPickerHtml(picker()).includes(notice));
  assert.ok(githubRepositoryPickerHtml(picker({ repositories: [], ssoPartial: true })).includes(notice));
  const forbidden = githubRepositoryPickerHtml(picker({ outcome: "sso-required", repositories: [], complete: false }));
  assert.ok(forbidden.includes("An organization requires single sign-on"));
  assert.ok(!forbidden.includes(notice));
});

test("the chooser is a labelled dialog with the four choices, sentence case", () => {
  const html = addProjectChooserHtml({ tutorialAvailable: true });
  assert.ok(!html.includes("scrim"), "the modal host owns the one scrim");
  assert.ok(html.includes('data-add-project'));
  assert.ok(html.includes('data-project-source-dialog'));
  assert.ok(html.includes('role="dialog"'));
  assert.ok(html.includes('aria-label="Add project"'));
  assert.ok(html.includes('data-project-cancel'));
  for (const [source, label] of [["github", "GitHub"], ["local", "Local folder"], ["url", "Repository URL"], ["tutorial", "Start with the tutorial project"]] as const) {
    assert.ok(html.includes(`data-project-source="${source}"`), `source ${source}`);
    assert.ok(html.includes(label), `label ${label}`);
  }
  assert.ok(!html.includes("Local Folder"), "sentence case, not Title Case");
  assert.ok(!html.includes("GitHub Projects"), "repositories, never the boards product");
  assert.ok(!/oauth/i.test(html), "no OAuth app claim");
});

test("the chooser hides the tutorial when the tutorial project is unavailable", () => {
  const html = addProjectChooserHtml({ tutorialAvailable: false });
  assert.ok(!html.includes('data-project-source="tutorial"'));
  assert.ok(!html.includes("New to Deck?"));
});

test("the picker is a labelled dialog with back and close that stay live while loading", () => {
  const html = githubRepositoryPickerHtml(picker({ loading: true }));
  assert.ok(html.includes('data-github-picker'));
  assert.ok(html.includes('data-phone-fullscreen'));
  assert.ok(html.includes('aria-label="Import from GitHub"'));
  const head = html.slice(html.indexOf('<header class="dialog-head">'), html.indexOf("</header>"));
  const back = head.indexOf('data-project-source="choose" aria-label="Back"');
  const title = head.indexOf("Import from GitHub");
  const close = head.indexOf("data-project-cancel");
  assert.ok(back >= 0 && title >= 0 && close >= 0, "the head carries back, title and close");
  assert.ok(back < title, "the back chevron leads the head");
  assert.ok(title < close, "close trails the title");
  for (const attr of ["data-project-source=\"choose\"", "data-project-cancel"]) {
    const tag = html.slice(html.indexOf(attr));
    assert.ok(!tag.slice(0, tag.indexOf(">")).includes("disabled"), `${attr} is never disabled`);
  }
  assert.ok(html.includes('role="status"'));
  assert.ok(html.includes("Loading repositories from GitHub…"));
});

test("the account label is the @login in interface voice, not mono", () => {
  const html = githubRepositoryPickerHtml(picker());
  assert.ok(html.includes("Connected as"));
  assert.ok(html.includes("@alexr"));
  const login = html.slice(html.indexOf("@alexr") - 120, html.indexOf("@alexr"));
  assert.ok(!login.includes("font-mono"), "the login is interface font");
});

test("search and owner controls carry their labels, All owners first", () => {
  const html = githubRepositoryPickerHtml(picker({ owner: "fable", query: "tasks" }));
  assert.ok(html.includes('data-github-repo-search'));
  assert.ok(html.includes('aria-label="Search repositories"'));
  assert.ok(html.includes('value="tasks"'));
  const select = html.slice(html.indexOf('data-github-repo-owner'), html.indexOf("</select>"));
  assert.ok(select.includes('aria-label="Repository owner"'));
  assert.ok(select.indexOf("All owners") >= 0 && select.indexOf("All owners") < select.indexOf("alexr"), "All owners leads");
  assert.ok(select.includes("northwind-labs"));
  assert.ok(/value="fable" selected/.test(select), "the active owner is selected");
});

test("rows render full names in machine text with public or private labels", () => {
  const html = githubRepositoryPickerHtml(picker());
  const row = html.slice(html.indexOf("fable/tasks-engine") - 200, html.indexOf("fable/tasks-engine") + 300);
  assert.ok(row.includes('data-mono="body-sm"'), "the repository identifier is machine text");
  assert.ok(html.includes("Private"));
  assert.ok(html.includes("Public"));
  assert.ok(html.includes('role="list"'));
  assert.ok(html.includes('aria-label="GitHub repositories"'));
});

test("an unimported repository offers Import, an imported one offers Open, matched case-insensitively", () => {
  const html = githubRepositoryPickerHtml(picker({
    repositories: [
      { id: 55, full_name: "Fable/Deck", owner: "Fable", private: true, description: "" },
      { id: 56, full_name: "fable/tasks-engine", owner: "fable", private: true, description: "" },
    ],
  }));
  assert.ok(html.includes('data-github-repo-open="p7"'), "imported matches the lowercased key");
  assert.ok(html.includes('aria-label="Open Fable/Deck"'));
  assert.ok(html.includes("In the sidebar as Deck"));
  assert.ok(!html.includes('aria-label="Import Fable/Deck"'), "an imported repository is not offered for import");
  assert.ok(html.includes('data-github-repo-select="56"'));
  assert.ok(html.includes('aria-label="Import fable/tasks-engine"'));
});

test("every server string is escaped, in text and in attributes", () => {
  const html = githubRepositoryPickerHtml(picker({
    account: `bad"><script>`,
    query: `x"><img`,
    owner: "",
    owners: [`evil"><svg>`, "fable"],
    repositories: [
      { id: 9, full_name: `o/r"><b>`, owner: "o", private: false, description: `desc</span>` },
      { id: 10, full_name: "a/b", owner: "a", private: true, description: "" },
    ],
    imported: new Map([["a/b", { id: `p"><i>`, name: `n</button>` }]]),
  }));
  assert.ok(!html.includes("<script>"));
  assert.ok(!html.includes('bad"><script>'));
  assert.ok(!html.includes('value="x"><img'));
  assert.ok(!html.includes('value="evil"><svg>'));
  assert.ok(!html.includes("o/r\"><b>"), "escaped in the aria-label and the row text");
  assert.ok(!html.includes('data-github-repo-open="p"><i>"'));
  assert.ok(html.includes("&lt;script&gt;"));
  assert.ok(html.includes("desc&lt;/span&gt;"));
  assert.ok(html.includes("n&lt;/button&gt;"));
});

test("loading and checking disable selection, and Back and Close remain the only way out", () => {
  for (const over of [{ loading: true }, { checking: true }] as const) {
    const html = githubRepositoryPickerHtml(picker(over));
    assert.ok(/data-github-repo-select="101"[^>]*disabled/.test(html) || /data-github-repo-select="101" disabled/.test(html), "import is disabled while busy");
    assert.ok(html.includes('data-github-repo-refresh disabled'), "refresh cannot be stacked");
  }
  const checking = githubRepositoryPickerHtml(picker({ checking: true }));
  assert.ok(checking.includes("Checking access with GitHub…"));
});

test("an empty result and an empty search are different copy, both honest about access", () => {
  const noQuery = githubRepositoryPickerHtml(picker({ repositories: [] }));
  assert.ok(noQuery.includes("No repositories to show"));
  assert.ok(noQuery.includes("does not establish why it is unavailable"), "a missing repository proves nothing about org approval");
  const noMatch = githubRepositoryPickerHtml(picker({ repositories: [], query: "zz" }));
  assert.ok(noMatch.includes("Nothing matches “zz”"));
  assert.ok(noMatch.includes("Search covers only the repositories GitHub listed"));
  assert.ok(!noMatch.includes("No repositories to show"), "a no-match search does not claim the account is empty");
});

test("loading and checking never paint an empty state over a pending answer", () => {
  for (const over of [{ loading: true }, { checking: true }] as const) {
    const html = githubRepositoryPickerHtml(picker({ ...over, repositories: [] }));
    assert.ok(!html.includes("No repositories to show"));
  }
});

test("every structured outcome renders plain copy with a refresh, never a dead end", () => {
  const cases: ReadonlyArray<readonly [string, string]> = [
    ["rate-limited", "GitHub is rate limiting this machine"],
    ["sso-required", "An organization requires single sign-on"],
    ["forbidden", "GitHub refused access"],
    ["account-changed", "The connected account changed"],
    ["unavailable", "GitHub access could not be confirmed"],
    ["not-found", "A listed repository is no longer visible"],
    ["limit", "The repository list exceeded Deck’s limits"],
    ["malformed", "GitHub's answer could not be read"],
  ];
  for (const [outcome, title] of cases) {
    const html = githubRepositoryPickerHtml(picker({ outcome }));
    assert.ok(html.includes(title), `${outcome} states what GitHub said`);
    assert.ok(html.includes("data-github-repo-refresh"), `${outcome} keeps a manual recheck`);
    assert.ok(!/oauth/i.test(html), `${outcome} makes no OAuth app claim`);
    assert.ok(!html.includes("GitHub Projects"), `${outcome} carries no boards disclaimer`);
  }
  const sso = githubRepositoryPickerHtml(picker({ outcome: "forbidden" }));
  assert.ok(sso.includes("does not establish whether organization approval is needed"));
  const fallback = githubRepositoryPickerHtml(picker({ outcome: "something-unlisted" }));
  assert.ok(fallback.includes("The repository list could not be loaded"), "an unknown outcome still lands on words");
});

test("authentication-required replaces the list with the connect action", () => {
  const html = githubRepositoryPickerHtml(picker({ account: "", outcome: "authentication-required", repositories: [], owners: [] }));
  assert.ok(html.includes("data-github-repo-connect"));
  assert.ok(html.includes("Connect GitHub"));
  assert.ok(!html.includes("data-github-repo-refresh"), "Connect GitHub reloads the picker itself, so it stands alone");
  assert.ok(!html.includes("GitHub CLI"), "the prompt says what connecting does, not how");
  assert.ok(!html.includes("data-github-repo-search"), "no list chrome without an account");
  assert.ok(!html.includes("Connected as"), "no account, no account line");
  assert.ok(!/oauth/i.test(html));
});

test("the renderer draws exactly the list it is handed — filtering is the model's job", () => {
  const html = githubRepositoryPickerHtml(picker({ query: "tasks", owner: "fable" }));
  assert.ok(html.includes("alexr/field-notes"), "a row that matches neither query nor owner still renders, because the parent filters");
});

test("the chooser reports an unavailable tutorial without hiding its alternatives", () => {
  const html = addProjectChooserHtml({ tutorialAvailable: true, message: "Still loading <tutorial>" });
  assert.ok(html.includes('role="status"'));
  assert.ok(html.includes("Still loading &lt;tutorial&gt;"));
  assert.ok(html.includes('data-project-source="tutorial"'));
  assert.ok(html.includes('data-project-source="github"'));
});
