// The account-level Git identity dialog, and the one
// thing its field recipe must not do to the manager that shares it.
import assert from "node:assert/strict";
import test from "node:test";
import { githubLineHtml, gitIdentityOnboardingHtml, gitIdentitySettingsHtml, type GitIdentityFormState } from "./git-identity.ts";

const form = (overrides: Partial<GitIdentityFormState> = {}): string => gitIdentityOnboardingHtml({
  busy: false, error: null, name: "", email: "", github: null,
  desc: "Applies to projects you open next.", note: null, back: false, ...overrides,
});
const github = { name: "Octo Cat", email: "583231+octocat@users.noreply.github.com" };

test("without GitHub the form offers to connect before the required author fields", () => {
  const html = form();
  assert.ok(html.includes(">Git identity</h2>"));
  assert.ok(html.includes('class="dialog-desc">Applies to projects you open next.</span>'), "the description rides in the head with the title");
  assert.ok(html.includes("data-onboarding-git-name required"), "the name is required");
  assert.ok(html.includes('data-onboarding-git-email required type="email"'), "the email is required and typed");
  assert.ok(html.includes("repeat(auto-fit,minmax(184px,1fr))"), "the fields sit side by side and collapse by container width");
  assert.ok(html.includes(">Save identity</button>"));
  assert.ok(html.indexOf("data-github-connect") < html.indexOf("data-onboarding-git-name"));
  assert.ok(html.includes('type="button" data-github-connect'), "connection does not submit the author form");
  assert.match(html, /data-github-connect [^>]*data-hint="[^"]+"/, "Connect GitHub explains itself in the info bar");
  assert.ok(!/>Cancel</.test(html), "no Cancel: the close X leaves");
  assert.ok(html.includes("data-git-identity-onboarding-close"), "the form can be closed");
  assert.ok(html.includes("data-git-identity-from-github hidden"), "nothing claims to come from GitHub");
  assert.ok(!html.includes("bg-error-tint"), "no error card without an error");
});

test("with GitHub connected the fields arrive filled and editable", () => {
  const html = form({ github, name: github.name, email: github.email });
  assert.ok(!html.includes("data-github-connect"), "no Connect GitHub once connected");
  assert.ok(html.includes(`value="${github.name}"`));
  assert.ok(html.includes(`value="${github.email}"`));
  assert.ok(!/data-onboarding-git-(name|email)[^>]*readonly/.test(html), "the filled fields stay editable");
  assert.ok(html.includes("Filled in from your GitHub account."));
  const edited = form({ github, name: "Someone Else", email: github.email });
  assert.ok(edited.includes("data-git-identity-from-github hidden"), "an edited field no longer claims to come from GitHub");
  assert.ok(!html.includes("data-git-identity-from-github hidden"));
});

test("inside a flow the form names what waits on it and can step back", () => {
  const html = form({ desc: "Your commits use this name and email.", note: "Codex starts as soon as you save this.", back: true });
  assert.ok(html.includes(">Codex starts as soon as you save this.</p>"));
  assert.ok(!html.includes("bg-wash-selected"), "the note is plain text, not a box");
  assert.match(html, /data-git-identity-onboarding-back [^>]*data-hint="[^"]+"[^>]*>Back<\/button>/);
  assert.ok(!form().includes("data-git-identity-onboarding-back"), "the settings door has no Back");
});

test("a connected account replaces the offer to connect and keeps the author fields", () => {
  const html = form({ githubLogin: "octo<cat>" });
  assert.ok(!html.includes("data-github-connect "), "nothing left to connect");
  assert.ok(!html.includes("data-github-connect-prompt"));
  assert.match(html, /<p data-github-connected role="status"/);
  assert.ok(html.includes('Connected to GitHub as <span data-github-login class="font-medium">octo&lt;cat&gt;</span>'));
  assert.ok(html.indexOf("data-github-connected") < html.indexOf("data-onboarding-git-name"));
  assert.ok(html.includes(">Save identity</button>"));
  assert.ok(form({ githubLogin: "" }).includes(">Connected to GitHub</span>"));
  assert.ok(form({ github, githubLogin: "octocat" }).includes("data-github-connected"), "a prefilled form still names the account");
});

test("while the connection is read the GitHub line offers nothing, in a slot it can be patched into", () => {
  const html = form({ githubChecking: true });
  assert.ok(html.includes('<div data-github-line class="contents"></div>'), "neither the offer nor an account");
  assert.ok(html.indexOf("data-github-line") < html.indexOf("data-onboarding-git-name"));
  assert.ok(form({ githubChecking: true, githubLogin: "octocat" }).includes("data-github-connected"), "a known account shows at once");
  const line = (overrides: Partial<GitIdentityFormState>) => githubLineHtml({
    busy: false, error: null, name: "", email: "", github: null, desc: "", note: null, back: false, ...overrides,
  });
  assert.ok(line({}).includes("data-github-connect "), "no account: the offer to connect");
  assert.ok(line({ githubChecking: false }).includes("data-github-connect "), "a settled read with no account offers to connect");
  assert.equal(line({ github }), "", "a prefilled form needs no offer");
  assert.ok(form().includes(`<div data-github-line class="contents">${line({})}</div>`), "the form draws the same line a patch writes");
});

test("an error renders once as a card below the fields, and busy holds the primary", () => {
  const html = form({ busy: true, error: "The write failed." });
  assert.equal(html.split("bg-error-tint").length - 1, 1, "one error card");
  assert.ok(html.includes("The write failed."));
  assert.ok(/data-git-identity-save disabled/.test(html), "Save identity is disabled while busy");
  assert.ok(html.indexOf("data-onboarding-git-email") < html.indexOf("bg-error-tint"), "the error sits below the fields");
});

test("the manager's editor keeps its space above the shared fields", () => {
  const profile = { id: "id-1", name: "Test Author", email: "test@example.invalid", revision: 1, created_at: "t", updated_at: "t" };
  const html = gitIdentitySettingsHtml({
    catalog: { profiles: [profile], default_profile_id: profile.id, revision: 1 },
    defaultProfile: profile, editor: "new", deleteID: null, busy: false, error: null,
  });
  const heading = html.indexOf(">Add email</h3>");
  const fields = html.indexOf("data-editor-git-name");
  assert.ok(heading > 0 && fields > heading, "the editor renders its heading then the fields");
  assert.ok(html.slice(heading, fields).includes('class="mt-4"'), "the editor owns the space between its heading and the fields");
  assert.ok(!form().includes('class="mt-4"'), "the form body sits flush under its head");
});
