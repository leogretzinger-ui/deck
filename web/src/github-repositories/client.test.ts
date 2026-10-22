import assert from "node:assert/strict";
import test from "node:test";
import { githubRemoteIdentity, HttpGitHubRepositoriesClient, parseGitHubRepository, readGitHubAccountIdentity } from "./client.ts";

const row = {
  id: 3, name: "library", full_name: "sample-org/library", owner: "sample-org",
  owner_type: "Organization", private: true, description: null,
  clone_url: "https://github.com/sample-org/library.git",
};

test("reads are GETs with bounded fetch options, account and refresh in the query", async t => {
  const calls: Array<{ path: string; init: RequestInit }> = [];
  t.mock.method(globalThis, "fetch", async (path: string, init: RequestInit) => {
    calls.push({ path, init });
    const body = path.includes("/api/github/repositories/3")
      ? { outcome: "ok", account: "fixture-user", repository: row }
      : { outcome: "ok", account: "fixture-user", repositories: [row], complete: true };
    return Response.json(body);
  });
  const client = new HttpGitHubRepositoriesClient();
  const signal = new AbortController().signal;
  const list = await client.list("fixture-user", false, signal);
  assert.equal(list.repositories[0]!.description, "", "a null description reads as empty, never as unknown");
  assert.equal(list.complete, true);
  await client.list("fixture-user", true, signal);
  const read = await client.repository("fixture-user", 3, signal);
  assert.equal(read.repository?.full_name, "sample-org/library");
  assert.deepEqual(calls.map(call => call.path), [
    "/api/github/repositories?account=fixture-user",
    "/api/github/repositories?account=fixture-user&refresh=1",
    "/api/github/repositories/3?account=fixture-user",
  ]);
  for (const { init } of calls) {
    assert.equal(init.method, "GET");
    assert.equal(init.cache, "no-store");
    assert.equal(init.redirect, "error");
    assert.equal(init.referrerPolicy, "no-referrer");
    assert.equal(init.credentials, "same-origin");
    assert.equal(init.signal, signal);
  }
});

test("the list carries the partial SSO flag alongside repositories", async t => {
  const client = new HttpGitHubRepositoriesClient();
  for (const ssoPartial of [true, false, undefined]) {
    t.mock.method(globalThis, "fetch", async () => Response.json({
      outcome: "ok", account: "fixture-user", repositories: [row], complete: true, ssoPartial,
    }));
    const list = await client.list("fixture-user", false, new AbortController().signal);
    assert.equal(list.ssoPartial, ssoPartial === true);
    assert.equal(list.outcome, "ok");
    assert.equal(list.repositories[0]?.id, row.id);
  }
});

test("repository rows are validated before they reach the caller", async t => {
  t.mock.method(globalThis, "fetch", async () => Response.json({ outcome: "ok", account: "a", repositories: [], complete: true }));
  const client = new HttpGitHubRepositoriesClient();
  const signal = new AbortController().signal;
  for (const bad of [
    { ...row, id: 0 }, { ...row, id: -2 }, { ...row, id: 2 ** 53 }, { ...row, id: 1.5 },
    { ...row, private: "yes" },
    { ...row, full_name: "sample-org/other" },
    { ...row, owner: "other" },
    { ...row, description: 7 },
    { ...row, clone_url: "http://github.com/sample-org/library.git" },
    { ...row, clone_url: "https://evil.example/sample-org/library.git" },
    { ...row, clone_url: "https://github.com/sample-org/other.git" },
    { ...row, clone_url: "https://token@github.com/sample-org/library.git" },
    { ...row, clone_url: "git@github.com:sample-org/library.git" },
  ]) {
    assert.throws(() => parseGitHubRepository(bad), { message: "Unreadable GitHub repository" }, JSON.stringify(bad));
  }
  assert.equal(parseGitHubRepository(row).id, 3);
  const empty = await client.list("a", false, signal);
  assert.deepEqual(empty.repositories, []);
  t.mock.method(globalThis, "fetch", async () => Response.json({ outcome: "ok", account: "a", repositories: [{ ...row, id: -1 }], complete: true }));
  await assert.rejects(client.list("a", false, signal), { message: "Unreadable GitHub repository" });
  t.mock.method(globalThis, "fetch", async () => Response.json({ outcome: "ok", account: "a", repositories: "many", complete: true }));
  await assert.rejects(client.list("a", false, signal), { message: "Unreadable GitHub repository list" });
  t.mock.method(globalThis, "fetch", async () => Response.json({ outcome: "ok", account: "someone-else", repositories: [], complete: true }));
  await assert.rejects(client.list("a", false, signal), { message: "Unreadable GitHub repository response" });
  t.mock.method(globalThis, "fetch", async () => Response.json({ outcome: "ok", account: "a", repository: { ...row, id: 4 } }));
  await assert.rejects(client.repository("a", 3, signal), { message: "Unreadable GitHub repository" });
  t.mock.method(globalThis, "fetch", async () => Response.json({ outcome: "ok", account: "a", repositories: [row] }));
  const partial = await client.list("a", false, signal);
  assert.equal(partial.complete, false, "an absent complete flag reads as partial, never as complete");
});

test("a failed request reports no server body and notifies authorization loss once", async t => {
  t.mock.method(globalThis, "fetch", async () => new Response("private diagnostic", { status: 401 }));
  let lost = 0;
  const client = new HttpGitHubRepositoriesClient(() => lost++);
  await assert.rejects(client.list("fixture-user", false, new AbortController().signal), { message: "GitHub repository request was not confirmed" });
  assert.equal(lost, 1);
});

test("an aborted request cannot invoke a late authorization callback", async t => {
  const controller = new AbortController();
  t.mock.method(globalThis, "fetch", async () => { controller.abort(); return new Response(null, { status: 401 }); });
  let lost = 0;
  const client = new HttpGitHubRepositoriesClient(() => lost++);
  await assert.rejects(client.repository("fixture-user", 3, controller.signal));
  assert.equal(lost, 0);
});

test("githubRemoteIdentity parses only the two canonical github.com remote forms", () => {
  assert.deepEqual(githubRemoteIdentity("https://github.com/owner/repo.git"), { owner: "owner", name: "repo", full_name: "owner/repo" });
  assert.deepEqual(githubRemoteIdentity("https://github.com/owner/repo"), { owner: "owner", name: "repo", full_name: "owner/repo" });
  assert.deepEqual(githubRemoteIdentity("git@github.com:owner/repo.git"), { owner: "owner", name: "repo", full_name: "owner/repo" });
  assert.deepEqual(githubRemoteIdentity("git@github.com:owner/repo"), { owner: "owner", name: "repo", full_name: "owner/repo" });
  assert.deepEqual(githubRemoteIdentity("https://github.com/Owner-1/re.po_2.git")!.full_name, "Owner-1/re.po_2");
  for (const rejected of [
    "", "owner/repo", "https://github.com/owner", "https://github.com/owner/repo/extra",
    "https://github.com/owner/repo.git/", "https://github.com/owner/repo.git?x=1",
    "http://github.com/owner/repo.git", "https://github.com:443/owner/repo.git",
    "https://token@github.com/owner/repo.git", "https://user:pass@github.com/owner/repo.git",
    "ssh://git@github.com/owner/repo.git", "git@gitlab.com:owner/repo.git",
    "https://github.com/-owner/repo.git", "https://github.com/owner/../repo",
    "https://github.com/owner/..", "git@github.com:owner/.git",
  ]) {
    assert.equal(githubRemoteIdentity(rejected), null, rejected);
  }
  assert.equal(githubRemoteIdentity(42), null);
  assert.equal(githubRemoteIdentity(undefined), null);
});


test("structured access refusals need no repository body", async t => {
  t.mock.method(globalThis, "fetch", async () => Response.json({ outcome: "sso-required", account: "fixture-user" }));
  const result = await new HttpGitHubRepositoriesClient().repository("fixture-user", 3, new AbortController().signal);
  assert.deepEqual(result, { outcome: "sso-required", account: "fixture-user", repository: null });
});

test("the account identity read is a bounded GET that yields the noreply address", async t => {
  const calls: Array<{ path: string; init: RequestInit }> = [];
  let body: unknown = { outcome: "ok", account: "octocat", name: "Octo Cat", email: "583231+octocat@users.noreply.github.com" };
  t.mock.method(globalThis, "fetch", async (path: string, init: RequestInit) => {
    calls.push({ path, init });
    return Response.json(body);
  });
  const signal = new AbortController().signal;
  assert.deepEqual(await readGitHubAccountIdentity(signal), { name: "Octo Cat", email: "583231+octocat@users.noreply.github.com" });
  assert.equal(calls[0]!.path, "/api/github/account");
  assert.equal(calls[0]!.init.method, "GET");
  assert.equal(calls[0]!.init.cache, "no-store");
  assert.equal(calls[0]!.init.redirect, "error");
  body = { outcome: "authentication-required", account: "" };
  assert.equal(await readGitHubAccountIdentity(signal), null, "no connected account fills nothing");
  for (const bad of [
    { outcome: "ok", account: "octocat", name: "Octo Cat", email: "octo@example.com" },
    { outcome: "ok", account: "octocat", name: "Octo Cat", email: "0+octocat@users.noreply.github.com" },
    { outcome: "ok", account: "octocat", name: "two\nlines", email: "1+octocat@users.noreply.github.com" },
    { outcome: "ok", account: "octocat", name: "", email: "1+octocat@users.noreply.github.com" },
  ]) {
    body = bad;
    await assert.rejects(readGitHubAccountIdentity(signal), { message: "Unreadable GitHub account" }, JSON.stringify(bad));
  }
});
