// A row's box exactly as it laid out when a drag started.
export interface DragRowBox {
  id: string;
  top: number;
  bottom: number;
}

// dragRowBoxes measures a drag's target geometry from live rows. It runs at
// dragstart, and again mid-drag only when the row set itself changed (see
// refreshDragBoxes).
export function dragRowBoxes(
  rows: readonly HTMLElement[],
  idOf: (row: HTMLElement) => string,
): DragRowBox[] {
  const boxes: DragRowBox[] = [];
  for (const row of rows) {
    const rect = row.getBoundingClientRect();
    // bottom is derived, not read: every rect source agrees on top and
    // height, and reading .bottom outright breaks against partial rect
    // shapes (the unit fakes carry {top, height} only).
    boxes.push({ id: idOf(row), top: rect.top, bottom: rect.top + rect.height });
  }
  return boxes;
}

// refreshDragBoxes re-measures only when the row set changed — a mid-drag
// refresh that added or removed a row. Rows only change through a render,
// and a render rebuilds them without the gesture's gap classes, so the
// re-measured boxes are gap-free. An unchanged set keeps the dragstart
// boxes: those are the stable ground the gesture targets, not the live
// layout the opened gap itself shifted.
export function refreshDragBoxes(
  scope: Element,
  selector: string,
  idOf: (row: HTMLElement) => string,
  current: DragRowBox[] | null,
): DragRowBox[] {
  const rows = Array.from(scope.querySelectorAll<HTMLElement>(selector));
  if (current !== null && current.length === rows.length &&
      current.every((box, index) => box.id === idOf(rows[index]!))) {
    return current;
  }
  return dragRowBoxes(rows, idOf);
}

// rowDropSlot resolves the pointer against the dragstart geometry: the
// first row whose box reaches below the pointer is the target, `after` set
// by which side of that box's midline the pointer sits on. The source
// row's own box offers no slot (the row already occupies it), a pointer
// below every box — the rail's trailing space — offers none, and a source
// missing from the boxes offers none at all: the row being dragged left
// the rail in a mid-gesture refresh, and committing would re-insert its
// dead id into the saved order. Live rects cannot decide this: the gap is
// the target row's own margin, so the moment one opens the target and
// every row below it slide away from a pointer that never moved, and a
// live hit test then names a neighbour that slid under the pointer — the
// release commits the original order back and the drag reads as having
// bounced.
export function rowDropSlot(
  boxes: readonly DragRowBox[],
  clientY: number,
  sourceID: string,
): { id: string; after: boolean } | null {
  const source = boxes.find((box) => box.id === sourceID);
  if (source === undefined) return null;
  if (clientY >= source.top && clientY < source.bottom) return null;
  for (const box of boxes) {
    if (box.id === sourceID) continue;
    if (clientY < box.bottom) return { id: box.id, after: clientY > (box.top + box.bottom) / 2 };
  }
  return null;
}
