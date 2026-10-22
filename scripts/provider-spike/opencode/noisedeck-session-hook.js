// noisedeck-session-hook.js — opencode plugin under test for the Phase-0 identity
// contract. Mirrors the codex/kimi SessionStart-hook pattern:
// the provider allocates its own native session id; this plugin observes the
// documented session.created event and POSTs the id to the per-launch
// one-shot capture endpoint whose URL/token arrive in the environment.
// Without those variables it is a quiet no-op (same contract as
// deploy/provider-hook/noisedeck-provider-hook).
//
// It also appends one line per observed event to $OC_SPIKE_EVENTS so the
// spike can measure WHICH events fire and WHEN (at process launch vs after
// the first submission). Only event types, ids, and timestamps are logged —
// never message content.

import { appendFileSync } from "node:fs"

const log = (line) => {
  const f = process.env.OC_SPIKE_EVENTS
  if (!f) return
  try {
    appendFileSync(f, `${Date.now()} ${line}\n`)
  } catch {}
}

export const AcSessionHook = async ({ directory }) => {
  log("plugin.loaded")
  return {
    event: async ({ event }) => {
      const info = event?.properties?.info
      log(`event ${event?.type} id=${info?.id ?? "-"}`)
      if (event?.type !== "session.created") return
      const url = process.env.NOISEDECK_SESSION_HOOK_URL
      const token = process.env.NOISEDECK_SESSION_HOOK_TOKEN
      if (!url || !token) return
      const body = JSON.stringify({
        hook_event_name: "SessionStart",
        session_id: info?.id,
        cwd: directory,
        source: "startup",
      })
      try {
        await fetch(url, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            "X-Noisedeck-Hook-Token": token,
          },
          body,
          signal: AbortSignal.timeout(5000),
        })
        log("hook.posted")
      } catch (e) {
        log(`hook.failed ${e?.name ?? "error"}`)
      }
    },
  }
}
