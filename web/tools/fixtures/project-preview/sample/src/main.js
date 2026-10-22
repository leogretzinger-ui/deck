// The harness's sample app: exercises every transport the preview gateway
// must carry — modules, CSS, images, same-origin API GET/POST, an SSE
// stream, and a WebSocket echo — plus a client-side router so a nested SPA
// route works both from an in-page link and from a direct load. State lands
// on window.__sample so the harness asserts outcomes, not implementation.
import "./style.css";

const state = window.__sample = { apiSeen: 0, streamEvents: 0, wsEcho: false, route: "" };

function renderRoute() {
  state.route = location.pathname;
  document.querySelector("[data-sample-nested]").textContent =
    location.pathname === "/nested/deep" ? "nested route rendered" : "";
}

document.addEventListener("click", (event) => {
  const link = event.target.closest("a[data-sample-link]");
  if (link === null) return;
  event.preventDefault();
  history.pushState({}, "", link.getAttribute("href"));
  renderRoute();
});
window.addEventListener("popstate", renderRoute);

(async () => {
  renderRoute();
  try {
    const hello = await (await fetch("/api/hello")).json();
    document.querySelector("[data-sample-name]").textContent = hello.app;
  } catch {
    // Left as "loading"; the harness asserts the real value arrived.
  }
  try {
    const echoed = await (await fetch("/api/echo", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ n: 1 }),
    })).json();
    state.apiSeen = echoed.n;
  } catch {
    // state.apiSeen stays 0; the harness fails on that.
  }
  const stream = new EventSource("/api/stream");
  stream.onmessage = () => { state.streamEvents += 1; };
  const ws = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/ws`);
  ws.onopen = () => ws.send("ping");
  ws.onmessage = () => { state.wsEcho = true; };
})();
