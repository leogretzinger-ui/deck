import assert from "node:assert/strict";
import test from "node:test";
import type { GitHubStatus } from "../github-connect/client.ts";
import type { GitHubRepositoriesClient, GitHubRepository, GitHubRepositoryList, GitHubRepositoryRead } from "./client.ts";
import { filterRepositories, GitHubRepositoriesModel } from "./model.ts";
import type { GitHubAccountStatus } from "./model.ts";

const alpha: GitHubRepository = { id: 1, name: "alpha", full_name: "fixture-user/alpha", owner: "fixture-user", owner_type: "User", private: false, description: "Personal site", clone_url: "https://github.com/fixture-user/alpha.git" };
const collaborator: GitHubRepository = { id: 2, name: "collaborator", full_name: "another-owner/collaborator", owner: "another-owner", owner_type: "User", private: true, description: "Shared repository", clone_url: "https://github.com/another-owner/collaborator.git" };
const library: GitHubRepository = { id: 3, name: "library", full_name: "sample-org/library", owner: "sample-org", owner_type: "Organization", private: true, description: "Organization repository", clone_url: "https://github.com/sample-org/library.git" };

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

function fixture() {
  const calls: string[] = [];
  const signals: AbortSignal[] = [];
  let status: GitHubStatus = { outcome: "authenticated", login: "fixture-user" };
  let list: GitHubRepositoryList = { outcome: "ok", account: "fixture-user", repositories: [alpha, collaborator, library], complete: true };
  const accountStatus: GitHubAccountStatus = {
    status: async signal => { signals.push(signal); calls.push("status"); return status; },
  };
  const client: GitHubRepositoriesClient = {
    list: async (account, refresh, signal) => { signals.push(signal); calls.push(`list:${account}${refresh ? ":refresh" : ""}`); return list; },
    repository: async (account, id, signal) => {
      signals.push(signal);
      calls.push(`repository:${account}:${id}`);
      const found = list.repositories.find(repository => repository.id === id);
      if (!found) throw new Error("not found");
      return { outcome: "ok", account, repository: found };
    },
  };
  let changes = 0;
  const model = new GitHubRepositoriesModel(accountStatus, client, () => changes++);
  return {
    model, accountStatus, client, calls, signals, changes: () => changes,
    status: (value: GitHubStatus) => { status = value; },
    list: (value: GitHubRepositoryList) => { list = value; },
  };
}

test("load reads the native status first and lists only for an authenticated login", async () => {
  const f = fixture();
  f.status({ outcome: "no-account" });
  await f.model.load();
  assert.deepEqual(f.calls, ["status"]);
  assert.equal(f.model.state.phase, "ready");
  assert.equal(f.model.state.outcome, "no-account");
  assert.equal(f.model.state.account, null);
  assert.deepEqual(f.model.state.repositories, []);
  assert.equal(await f.model.select(1), null, "without an account there is nothing to select from");
  f.status({ outcome: "auth-issue", login: "fixture-user" });
  await f.model.load();
  assert.deepEqual(f.calls, ["status", "status"], "a login without an authenticated outcome never lists");
  f.status({ outcome: "authenticated", login: "fixture-user" });
  await f.model.load();
  assert.deepEqual(f.calls, ["status", "status", "status", "list:fixture-user"]);
  assert.equal(f.model.state.account, "fixture-user");
  assert.equal(f.model.state.repositories.length, 3);
  assert.equal(f.model.state.complete, true);
  assert.deepEqual(f.model.owners, ["another-owner", "fixture-user", "sample-org"]);
});

test("partial SSO keeps listed repositories selectable and clears on refresh", async () => {
  const f = fixture();
  f.list({ outcome: "ok", account: "fixture-user", repositories: [alpha], complete: true, ssoPartial: true });
  await f.model.load();
  assert.equal(f.model.state.outcome, "ok");
  assert.equal(f.model.state.ssoPartial, true);
  assert.deepEqual(f.model.visible, [alpha]);
  assert.deepEqual(await f.model.select(alpha.id), alpha);
  f.list({ outcome: "ok", account: "fixture-user", repositories: [alpha, library], complete: true });
  await f.model.refresh();
  assert.equal(f.model.state.ssoPartial, false);
  assert.deepEqual(f.model.visible, [alpha, library]);
});

test("a completed connection replaces an older status read with a validated listing", async () => {
  const f = fixture();
  const old = deferred<GitHubStatus>();
  f.accountStatus.status = async () => old.promise;
  const loading = f.model.load();
  await f.model.connected("fixture-user");
  assert.deepEqual(f.calls, ["list:fixture-user:refresh"]);
  assert.deepEqual(f.model.visible, [alpha, collaborator, library]);
  old.resolve({ outcome: "no-account" });
  await loading;
  assert.equal(f.model.state.outcome, "ok");
  assert.equal(f.model.state.account, "fixture-user");
  f.model.close();
});

test("a completed connection cannot override a refused or changed-account listing", async () => {
  for (const list of [
    { outcome: "no-account", account: "fixture-user", repositories: [], complete: false },
    { outcome: "ok", account: "another-user", repositories: [alpha], complete: true },
  ]) {
    const f = fixture();
    f.list(list);
    await f.model.connected("fixture-user");
    assert.deepEqual(f.model.visible, []);
    assert.equal(f.model.state.complete, false);
    assert.equal(await f.model.select(alpha.id), null);
    f.model.close();
  }
});

test("query and owner filter the fetched set: collaborator and organization rows match", async () => {
  const f = fixture();
  await f.model.load();
  f.model.setQuery("shared");
  assert.deepEqual(f.model.visible.map(repository => repository.id), [2], "description match finds the collaborator row");
  f.model.setQuery("COLLABORATOR");
  assert.deepEqual(f.model.visible.map(repository => repository.id), [2], "name match is case-insensitive");
  f.model.setQuery("sample-org");
  assert.deepEqual(f.model.visible.map(repository => repository.id), [3], "full_name match finds the organization row");
  f.model.setQuery("");
  f.model.setOwner("sample-org");
  assert.deepEqual(f.model.visible.map(repository => repository.id), [3]);
  f.model.setOwner("ghost");
  assert.equal(f.model.state.owner, "sample-org", "an owner no row names cannot be filtered by");
  f.model.setOwner(null);
  assert.equal(f.model.visible.length, 3);
  assert.deepEqual(filterRepositories([alpha, collaborator], "personal", null).map(repository => repository.id), [1]);
  f.list({ outcome: "ok", account: "fixture-user", repositories: [alpha], complete: false });
  await f.model.refresh();
  assert.equal(f.model.state.complete, false, "a partial listing stays visibly partial");
  f.model.setQuery("shared");
  assert.deepEqual(f.model.visible, [], "the query covers the fetched set only, never the account");
  assert.deepEqual(f.calls.at(-1), "list:fixture-user:refresh");
});

test("a new list clears a stale owner filter and a selection the account no longer returns", async () => {
  const f = fixture();
  await f.model.load();
  f.model.setOwner("sample-org");
  assert.equal((await f.model.select(3))?.full_name, "sample-org/library");
  f.list({ outcome: "ok", account: "fixture-user", repositories: [alpha], complete: true });
  await f.model.refresh();
  assert.equal(f.model.state.owner, null, "the owner filter derives from the returned rows");
  assert.equal(f.model.state.selected, null, "a repository the fresh list does not return is not kept selected");
  assert.deepEqual(f.model.state.repositories, [alpha]);
});

test("selection revalidates the stable id and keeps a rename under the same account", async () => {
  const f = fixture();
  await f.model.load();
  f.client.repository = async (account, id) => ({
    outcome: "ok", account,
    repository: { ...alpha, id, name: "alpha-renamed", full_name: "fixture-user/alpha-renamed", clone_url: "https://github.com/fixture-user/alpha-renamed.git" },
  });
  const selected = await f.model.select(1);
  assert.equal(selected?.full_name, "fixture-user/alpha-renamed");
  assert.equal(f.model.state.selected?.full_name, "fixture-user/alpha-renamed");
  assert.equal(f.model.state.selected?.id, 1);
  assert.equal(f.model.state.pendingId, null);
  assert.equal(f.model.state.phase, "ready");
  f.model.clearSelection();
  assert.equal(f.model.state.selected, null);
});

test("an unconfirmed pick selects nothing", async () => {
  const f = fixture();
  await f.model.load();
  f.client.repository = async () => { throw new Error("gone"); };
  assert.equal(await f.model.select(2), null);
  assert.equal(f.model.state.selected, null);
  assert.equal(f.model.state.pendingId, null);
  assert.equal(f.model.state.phase, "ready");
  assert.match(f.model.state.message, /not selected/);
});

test("close aborts a pending selection and a late answer cannot land", async () => {
  const f = fixture();
  await f.model.load();
  const pending = deferred<GitHubRepositoryRead>();
  f.client.repository = () => pending.promise;
  const selecting = f.model.select(1);
  assert.equal(f.model.state.phase, "checking");
  assert.equal(f.model.state.pendingId, 1);
  f.model.close();
  const changes = f.changes();
  pending.resolve({ outcome: "ok", account: "fixture-user", repository: alpha });
  assert.equal(await selecting, null);
  assert.equal(f.model.state.phase, "disposed");
  assert.equal(f.model.state.selected, null);
  assert.equal(f.changes(), changes, "a late answer after close changes nothing");
  assert.equal(f.signals.at(-1)!.aborted, true);
  await f.model.load();
  assert.equal(f.model.state.phase, "disposed", "close is final");
});

test("a late selection cannot land after the account changed", async () => {
  const f = fixture();
  await f.model.load();
  const pending = deferred<GitHubRepositoryRead>();
  f.client.repository = async (account, id, signal) => { f.signals.push(signal); f.calls.push(`repository:${account}:${id}`); return pending.promise; };
  const selecting = f.model.select(1);
  const beta: GitHubRepository = { id: 9, name: "beta", full_name: "second-user/beta", owner: "second-user", owner_type: "User", private: false, description: "", clone_url: "https://github.com/second-user/beta.git" };
  f.status({ outcome: "authenticated", login: "second-user" });
  f.list({ outcome: "ok", account: "second-user", repositories: [beta], complete: true });
  const loading = f.model.load();
  pending.resolve({ outcome: "ok", account: "fixture-user", repository: alpha });
  assert.equal(await selecting, null);
  await loading;
  assert.equal(f.model.state.account, "second-user");
  assert.equal(f.model.state.selected, null);
  assert.equal(f.model.state.pendingId, null);
  assert.deepEqual(f.model.state.repositories, [beta]);
  assert.deepEqual(f.calls, ["status", "list:fixture-user", "repository:fixture-user:1", "status", "list:second-user"]);
});

test("a load requested while one is in flight supersedes it", async () => {
  const f = fixture();
  const earlier = deferred<GitHubStatus>();
  const status = f.accountStatus.status;
  f.accountStatus.status = signal => { f.signals.push(signal); f.calls.push("status"); return earlier.promise; };
  const first = f.model.load();
  f.accountStatus.status = status;
  const second = f.model.refresh();
  assert.equal(f.signals[0]!.aborted, true, "the superseded read is aborted");
  await second;
  earlier.resolve({ outcome: "no-account" });
  await first;
  assert.equal(f.model.state.phase, "ready");
  assert.equal(f.model.state.outcome, "ok");
  assert.equal(f.model.state.account, "fixture-user");
  assert.equal(f.model.state.repositories.length, 3, "the older answer cannot replace the newer one");
  assert.deepEqual(f.calls, ["status", "status", "list:fixture-user:refresh"]);
});

test("authorization loss clears rows and selection and never retries on its own", async () => {
  const f = fixture();
  await f.model.load();
  assert.equal((await f.model.select(1))?.id, 1);
  const issued = f.calls.length;
  f.model.authorizationLost();
  assert.equal(f.model.state.phase, "error");
  assert.deepEqual(f.model.state.repositories, []);
  assert.equal(f.model.state.selected, null);
  assert.equal(f.model.state.account, null);
  assert.equal(f.calls.length, issued, "permission loss triggers no automatic retry");
  await f.model.load();
  assert.equal(f.model.state.phase, "ready");
  assert.equal(f.model.state.repositories.length, 3, "an explicit load is the way back");
});

test("a timed-out read aborts and a late answer cannot confirm it", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const f = fixture();
  const pending = deferred<GitHubRepositoryList>();
  f.client.list = () => pending.promise;
  const loading = f.model.load();
  t.mock.timers.tick(75000);
  pending.resolve({ outcome: "ok", account: "fixture-user", repositories: [alpha], complete: true });
  await loading;
  assert.equal(f.model.state.phase, "error");
  assert.deepEqual(f.model.state.repositories, []);
  assert.equal(f.signals.at(-1)!.aborted, true);
});

test("reset aborts the in-flight read and returns to idle", async () => {
  const f = fixture();
  const pending = deferred<GitHubRepositoryList>();
  f.client.list = () => pending.promise;
  const loading = f.model.load();
  f.model.reset();
  pending.resolve({ outcome: "ok", account: "fixture-user", repositories: [alpha], complete: true });
  await loading;
  assert.equal(f.model.state.phase, "idle");
  assert.deepEqual(f.model.state.repositories, []);
  assert.equal(f.signals.at(-1)!.aborted, true);
  f.client.list = async (account, _refresh, signal) => { f.signals.push(signal); f.calls.push(`list:${account}`); return { outcome: "ok", account, repositories: [alpha, collaborator, library], complete: true }; };
  await f.model.load();
  assert.equal(f.model.state.repositories.length, 3, "reset is not close: an explicit load works again");
});


test("incomplete and refused listings discard every row and cannot be selected", async () => {
  for (const result of [
    { outcome: "ok", complete: false },
    { outcome: "sso-required", complete: false },
    { outcome: "account-changed", complete: true },
  ]) {
    const f = fixture();
    await f.model.load();
    f.list({ ...result, account: "fixture-user", repositories: [alpha] });
    await f.model.refresh();
    assert.deepEqual(f.model.visible, []);
    assert.equal(f.model.state.complete, false);
    assert.equal(await f.model.select(alpha.id), null);
    assert.ok(!f.calls.some(call => call.startsWith("repository:")));
  }
});

test("selection access refusal removes stale rows and preserves the structured reason", async () => {
  const f = fixture(); await f.model.load();
  f.client.repository = async () => ({ outcome: "sso-required", account: "fixture-user", repository: null });
  assert.equal(await f.model.select(alpha.id), null);
  assert.equal(f.model.state.outcome, "sso-required");
  assert.deepEqual(f.model.visible, []);
  assert.equal(f.model.state.selected, null);
});
