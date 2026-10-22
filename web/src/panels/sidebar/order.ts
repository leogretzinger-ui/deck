import type { SessionSummary } from "../../api/sessions.ts";
import type { ProjectWithSessions } from "./sidebar.ts";

export function reconcileCreatedProjectExpansion(
  expanded: Set<string>,
  pendingProjectID: string | null,
  groups: ProjectWithSessions[],
): string | null {
  if (pendingProjectID === null || !groups.some((g) => g.project.id === pendingProjectID)) {
    return pendingProjectID;
  }
  expanded.add(pendingProjectID);
  return null;
}

// moveProjectRelative is the pure core of the menu-driven project reorder:
// it returns the order with `from` removed and reinserted on the named side
// of `target`. An unknown target appends, so a move against a row that left
// the list (a concurrent refresh) still settles somewhere visible. Project
// rows are never draggable — the row menu is the only way to move one.
export function moveProjectRelative(
  ids: readonly string[],
  from: string,
  target: string,
  after: boolean,
): string[] {
  const next = ids.filter((id) => id !== from);
  const index = next.indexOf(target);
  if (index === -1) next.push(from);
  else next.splice(after ? index + 1 : index, 0, from);
  return next;
}

// moveProjectToTop is the long move: the project leaves its place and the
// rows above it each shift down one. A project already first, or one not in
// the list, changes nothing.
export function moveProjectToTop(ids: readonly string[], projectID: string): string[] {
  const index = ids.indexOf(projectID);
  if (index <= 0) return [...ids];
  return [projectID, ...ids.slice(0, index), ...ids.slice(index + 1)];
}

export function moveProjectOneStep(
  ids: readonly string[],
  projectID: string,
  direction: "up" | "down",
): string[] {
  const index = ids.indexOf(projectID);
  const targetIndex = index + (direction === "up" ? -1 : 1);
  if (index === -1 || targetIndex < 0 || targetIndex >= ids.length) return [...ids];
  return moveProjectRelative(ids, projectID, ids[targetIndex]!, direction === "down");
}

// presentedProjectOrder is the id sequence in exactly the order the rows
// render: alphabetical until your menu-built order exists, then
// that order applied on top. The model's server order is never consulted.
// It is also the move's ground truth — a menu move seeds from these ids, so
// it reorders exactly the visible neighbors.
export function presentedProjectOrder(
  groups: readonly ProjectWithSessions[],
  savedOrder: readonly string[] | null,
): string[] {
  return presentedRowOrder(groups.map((g) => g.project), savedOrder).map((p) => p.id);
}

// presentedRowOrder applies the project rule to any named rows. An import
// is placed by it under the name its project will carry, so the project
// lands exactly where the import row stood: a row the saved order does not
// name sorts after the ones it does, alphabetically among the rest.
export function presentedRowOrder<T extends { id: string; name: string }>(
  rows: readonly T[],
  savedOrder: readonly string[] | null,
): T[] {
  const byName = [...rows].sort((a, b) => a.name.localeCompare(b.name));
  if (savedOrder === null) return byName;
  const position = new Map(savedOrder.map((id, index) => [id, index]));
  return byName.sort((a, b) =>
    (position.get(a.id) ?? Number.MAX_SAFE_INTEGER)
      - (position.get(b.id) ?? Number.MAX_SAFE_INTEGER));
}

// moveSessionRelative is the pure core of the session-row drag reorder: it
// returns the order with `from` removed and reinserted on the named side of
// `target`. An unknown target appends, so a drag onto a row that left the
// list (a concurrent refresh) still settles somewhere visible.
export function moveSessionRelative(
  ids: readonly string[],
  from: string,
  target: string,
  after: boolean,
): string[] {
  const next = ids.filter((id) => id !== from);
  const index = next.indexOf(target);
  if (index === -1) next.push(from);
  else next.splice(after ? index + 1 : index, 0, from);
  return next;
}

// presentedSessionOrder is the id sequence in exactly the order the rows
// render: the server's order until your drag-built order exists,
// then that order applied on top. A session the saved order does not name
// sorts after the ones it does, so a new session appears predictably rather
// than jumping to the top.
export function presentedSessionOrder(
  sessions: readonly SessionSummary[],
  savedOrder: readonly string[] | null,
): string[] {
  const serverOrder = sessions.map((s) => s.id);
  if (savedOrder === null) return serverOrder;
  const position = new Map(savedOrder.map((id, index) => [id, index]));
  return [...serverOrder].sort((a, b) =>
    (position.get(a) ?? Number.MAX_SAFE_INTEGER)
      - (position.get(b) ?? Number.MAX_SAFE_INTEGER));
}

// Sort by name is a ONE-SHOT action, not a mode: it returns the ids in
// case-insensitive display-name order for the caller to store as the manual
// order, which stays hand-editable afterwards — nothing persists as a sort
// preference.
export function sortedProjectOrderByName(
  groups: readonly ProjectWithSessions[],
): string[] {
  return [...groups]
    .sort((a, b) => a.project.name.toLowerCase().localeCompare(b.project.name.toLowerCase()))
    .map((g) => g.project.id);
}

// The header menu's New session names its target: the project in context,
// else the first row as presented. Null only when no projects exist.
export function newSessionTargetProject(
  groups: readonly ProjectWithSessions[],
  order: readonly string[],
  selectedProjectID: string | null,
): { id: string; name: string } | null {
  const id = selectedProjectID !== null && order.includes(selectedProjectID)
    ? selectedProjectID
    : order[0];
  const project = groups.find((g) => g.project.id === id)?.project;
  return project === undefined ? null : { id: project.id, name: project.name };
}
