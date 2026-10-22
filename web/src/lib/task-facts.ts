// The task vocabulary every surface shares: id normalization, the
// priority scale, and the status labels/dots. Pure functions over task
// fields - the board, the task cards, and the hover previews must all
// say "To do" the same way.
export function taskID(id: unknown): string {
  if (typeof id === "string" || typeof id === "number") {
    return String(id);
  }
  return "?";
}

// normalizedPriority folds task-master's "med"/"medium" split and maps
// absence to "none" so the Filters popover has stable keys.
export function normalizedPriority(p: string | undefined): "high" | "medium" | "low" | "none" {
  switch (p) {
    case "high":
      return "high";
    case "med":
    case "medium":
      return "medium";
    case "low":
      return "low";
    default:
      return "none";
  }
}

// priorityLabel is the display name for a task's priority: the
// normalized name for the known scale, the raw value verbatim for
// anything out of schema — a task marked "critical" must not display
// as "none".
export function priorityLabel(p: string): string {
  const n = normalizedPriority(p);
  return n === "none" ? p : n;
}

export function priorityDotClass(p: string | undefined): string {
  switch (normalizedPriority(p)) {
    case "high":
      return "bg-error";
    case "medium":
      return "bg-warning";
    default:
      return "bg-text-tertiary";
  }
}

export interface StatusFacts {
  label: string;
  dot: string;
}

export function taskStatusFacts(t: { status: string }): StatusFacts {
  // The labels speak the board's vocabulary - a task reads "To do" in its
  // detail exactly as it does at the head of its column - and an unknown
  // status is title-cased rather than leaked as its raw enum value.
  const label = t.status === "pending" ? "To do"
    : t.status === "in-progress" ? "In progress"
    : t.status === "done" ? "Done"
    : t.status.charAt(0).toUpperCase() + t.status.slice(1).replaceAll("-", " ");
  const dot = t.status === "pending" ? "bg-text-tertiary"
    : t.status === "in-progress" ? "bg-accent"
    : t.status === "done" ? "bg-alive"
    : "bg-oversight";
  return { label, dot };
}
