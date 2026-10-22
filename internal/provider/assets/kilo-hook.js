// Provisioned Kilo identity plugin. Kilo creates native sessions lazily, so
// the documented session.created event is the first authoritative identity.
import { request as httpRequest } from "node:http"

// A configured private socket is the only transport for this launch. Its
// failure must not disclose a capture token or payload to a TCP listener.
const post = (url, options, socketPath) => {
  if (!socketPath) return fetch(url, options)
  return new Promise((resolve, reject) => {
    const target = new URL(url)
    const request = httpRequest({
      socketPath,
      path: target.pathname + target.search,
      method: options.method,
      headers: options.headers,
      signal: options.signal,
      agent: false,
    }, (response) => {
      const status = response.statusCode ?? 0
      response.destroy()
      resolve({ status })
    })
    request.on("error", reject)
    request.end(options.body)
  })
}

const DELIVERY_DEADLINE_MS = 8000
const ATTEMPT_TIMEOUT_MS = 2500
const MAX_DELIVERY_ATTEMPTS = 3

const deliver = async (url, token, body) => {
  const socketPath = process.env.NOISEDECK_SESSION_HOOK_SOCKET
  const deadline = Date.now() + DELIVERY_DEADLINE_MS
  for (let attempt = 0; attempt < MAX_DELIVERY_ATTEMPTS; attempt += 1) {
    const remaining = deadline - Date.now()
    if (remaining <= 0) return
    try {
      const response = await post(url, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Noisedeck-Hook-Token": token },
        body,
        redirect: "manual",
        signal: AbortSignal.timeout(Math.min(ATTEMPT_TIMEOUT_MS, remaining)),
      }, socketPath)
      await response.body?.cancel().catch(() => {})
      if (response.status >= 200 && response.status < 300) return
      if (response.status >= 400 && response.status < 500) return
      if (response.status < 500 || response.status >= 600) return
    } catch {}
    const pause = Math.min(500 * (attempt + 1), deadline - Date.now())
    if (pause > 0) await new Promise((resolve) => setTimeout(resolve, pause))
  }
}

const server = async ({ directory }) => ({
  event: async ({ event }) => {
    if (event?.type !== "session.created") return
    const id = event?.properties?.info?.id
    const url = process.env.NOISEDECK_SESSION_HOOK_URL
    const token = process.env.NOISEDECK_SESSION_HOOK_TOKEN
    if (!id || !url || !token) return
    const body = JSON.stringify({ hook_event_name: "SessionStart", session_id: id, cwd: directory, source: "startup" })
    await deliver(url, token, body)
  },
})

export default { id: "noisedeck-provider-hook", server }
