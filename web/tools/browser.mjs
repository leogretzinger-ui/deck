// How the harnesses get a browser.
//
// The browser is a SERVICE, not a binary this container owns. Chromium and
// its ~24 system libraries live in the agent-browser sidecar, so nothing
// here needs them installed — the app image stays free of ~370MB it would
// otherwise carry for two dev harnesses.
//
// NOISE_BROWSER_WS names the sidecar's Playwright server. The sidecar
// shares the app's network namespace (network_mode: service:app), so a page
// served on 127.0.0.1 here is reachable at 127.0.0.1 there — which is why
// the harnesses can keep binding loopback and need no URL rewriting.
//
// Unset, this falls back to launching locally, so a workstation with its own
// browsers keeps working with no compose overlay.
//
// The sidecar image tag MUST match the playwright version in
// web/package.json: Playwright refuses a client/server version mismatch, and
// the failure is a connect error, not a subtle one. Bump both together.
import { chromium } from "playwright";

export const BROWSER_WS = process.env.NOISE_BROWSER_WS ?? "";

// browser returns a connected or launched browser plus how it was obtained,
// so a harness can say which in its output — a screenshot from a different
// browser than the reader assumes is a wrong measurement, not a missing one.
export async function browser() {
  if (BROWSER_WS !== "") {
    return { browser: await chromium.connect(BROWSER_WS), via: `sidecar ${BROWSER_WS}` };
  }
  return { browser: await chromium.launch(), via: "local launch" };
}
