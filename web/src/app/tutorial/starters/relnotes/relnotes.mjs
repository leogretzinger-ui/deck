#!/usr/bin/env node
// Writes release notes from a git log. See PRD.md.
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parse } from "../ccparse/index.mjs";

const here = dirname(fileURLToPath(import.meta.url));

const SECTIONS = [
  ["Breaking changes", "breaking"],
  ["Features", "feat"],
  ["Fixes", "fix"],
  ["Other", "other"],
];

function sectionFor(commit) {
  const parsed = parse(commit.subject);
  if (parsed.breaking) return { key: "breaking", text: bullet(parsed) };
  if (parsed.type === "feat") return { key: "feat", text: bullet(parsed) };
  if (parsed.type === "fix") return { key: "fix", text: bullet(parsed) };
  return { key: "other", text: parsed.type ? bullet(parsed) : commit.subject };
}

function bullet(parsed) {
  return parsed.scope ? `(${parsed.scope}) ${parsed.description}` : parsed.description;
}

// Turns a list of {subject, body} into Markdown. Pure, so the tests can use it.
export function render(commits) {
  const groups = new Map(SECTIONS.map(([, key]) => [key, []]));
  for (const commit of commits) {
    const { key, text } = sectionFor(commit);
    groups.get(key).push(text);
  }
  const out = [];
  for (const [heading, key] of SECTIONS) {
    const items = groups.get(key);
    if (items.length === 0) continue;
    out.push(`## ${heading}`, ...items.map((item) => `- ${item}`), "");
  }
  return out.join("\n").trimEnd() + "\n";
}

function readLog(range) {
  const args = ["log", "--format=%s%x00%b%x1e"];
  if (range) args.push(range);
  else {
    let tag = "";
    try { tag = execFileSync("git", ["describe", "--tags", "--abbrev=0"], { encoding: "utf8" }).trim(); } catch {}
    if (tag) args.push(`${tag}..HEAD`);
  }
  const raw = execFileSync("git", args, { encoding: "utf8" });
  return raw.split("\x1e").map((entry) => entry.trim()).filter(Boolean).map((entry) => {
    const [subject, body = ""] = entry.split("\x00");
    return { subject: subject.trim(), body: body.trim() };
  });
}

function checkPin() {
  const pinned = readFileSync(join(here, "ccparse.pin"), "utf8").trim();
  const installed = JSON.parse(readFileSync(join(here, "../ccparse/package.json"), "utf8")).version;
  if (pinned !== installed) {
    process.stderr.write(`relnotes: ccparse.pin says ${pinned} but ../ccparse is ${installed}\n`);
  }
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  checkPin();
  process.stdout.write(render(readLog(process.argv[2])));
}
