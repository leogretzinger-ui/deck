// escapeHtml is the only sanitization the DOM shells need: project
// names, paths, statuses, and task titles are user/server data
// rendered as text.
export function escapeHtml(s: string): string {
  return s
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}
