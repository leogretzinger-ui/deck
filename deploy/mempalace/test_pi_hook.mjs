import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));

test("Pi-family lifecycle events forward bounded session identity", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "deck-mempalace-pi-hook-"));
  const capture = path.join(root, "capture.jsonl");
  const helper = path.join(root, "capture.py");
  await writeFile(
    helper,
    [
      "import json, os, sys",
      "record = {'args': sys.argv[1:], 'payload': json.load(sys.stdin)}",
      "with open(os.environ['NOISEDECK_TEST_CAPTURE'], 'a', encoding='utf-8') as stream:",
      "    stream.write(json.dumps(record) + '\\n')",
    ].join("\n"),
  );

  const prior = { ...process.env };
  try {
    process.env.NOISEDECK_MEMPALACE_FORWARDER_PATH = helper;
    process.env.NOISEDECK_MEMPALACE_PYTHON = process.env.PYTHON ?? "python3";
    process.env.NOISEDECK_TEST_CAPTURE = capture;
    delete process.env.NOISEDECK_MEMPALACE_HARNESS;

    const module = await import(
      `${pathToFileURL(path.join(here, "hooks", "mempal-pi-hook.js"))}?test=${Date.now()}`
    );
    const handlers = new Map();
    module.default({
      on(event, handler) {
        handlers.set(event, handler);
      },
    });

    assert.deepEqual(
      [...handlers.keys()].sort(),
      ["agent_end", "session_before_compact", "session_shutdown", "session_start"],
    );
    const context = {
      sessionManager: {
        getSessionId: () => "omp-session",
        getSessionFile: () => "/home/agent/.omp/agent/sessions/session.jsonl",
        getCwd: () => "/workspace/deck",
      },
    };
    for (const event of handlers.keys()) await handlers.get(event)({}, context);

    const records = (await readFile(capture, "utf8"))
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line));
    assert.deepEqual(
      records.map((record) => record.args),
      [
        ["--hook", "session-start", "--harness", "omp"],
        ["--hook", "stop", "--harness", "omp"],
        ["--hook", "precompact", "--harness", "omp"],
        ["--hook", "session-end", "--harness", "omp"],
      ],
    );
    for (const record of records) {
      assert.deepEqual(record.payload, {
        session_id: "omp-session",
        transcript_path: "/home/agent/.omp/agent/sessions/session.jsonl",
        cwd: "/workspace/deck",
      });
    }
  } finally {
    process.env = prior;
    await rm(root, { recursive: true, force: true });
  }
});
