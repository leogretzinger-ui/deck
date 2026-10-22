import { spawn } from "node:child_process";

const FORWARDER =
  process.env.NOISEDECK_MEMPALACE_FORWARDER_PATH ??
  "/usr/local/lib/noisedeck/mempalace-hook-forwarder.py";
const PYTHON = process.env.NOISEDECK_MEMPALACE_PYTHON ?? "/usr/bin/python3";
const TIMEOUT_MS = 5000;

function harness(ctx) {
  const configured = process.env.NOISEDECK_MEMPALACE_HARNESS;
  if (configured === "pi" || configured === "omp") return configured;
  const transcript = ctx?.sessionManager?.getSessionFile?.() ?? "";
  return transcript.includes("/.omp/") ? "omp" : "pi";
}

function payload(ctx) {
  return {
    session_id: ctx?.sessionManager?.getSessionId?.() ?? "",
    transcript_path: ctx?.sessionManager?.getSessionFile?.() ?? "",
    cwd: ctx?.sessionManager?.getCwd?.() ?? "",
  };
}

async function forward(hook, ctx) {
  await new Promise((resolve) => {
    let settled = false;
    let timer;
    const child = spawn(
      PYTHON,
      [FORWARDER, "--hook", hook, "--harness", harness(ctx)],
      { stdio: ["pipe", "ignore", "ignore"] },
    );
    const finish = () => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve();
    };
    timer = setTimeout(() => {
      child.kill("SIGKILL");
      finish();
    }, TIMEOUT_MS);
    child.once("error", finish);
    child.once("close", finish);
    child.stdin.once("error", finish);
    child.stdin.end(JSON.stringify(payload(ctx)));
  });
}

export default function (pi) {
  pi.on("session_start", async (_event, ctx) => {
    await forward("session-start", ctx);
  });
  pi.on("agent_end", async (_event, ctx) => {
    await forward("stop", ctx);
  });
  pi.on("session_before_compact", async (_event, ctx) => {
    await forward("precompact", ctx);
  });
  pi.on("session_shutdown", async (_event, ctx) => {
    await forward("session-end", ctx);
  });
}
