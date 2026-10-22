// The docked-panel coordinator. The panels are siblings in the same
// content row, so at most ONE of them may be open: two of them at once
// would split the row three ways and starve the terminal of its 360px
// floor. Independent booleans can disagree about that, so the open panel
// is a single value and every gesture goes through here.
//
// The hosts are resolved lazily rather than captured: after auth teardown
// the root no longer carries workspace regions, and closing a panel then
// must be a no-op rather than a throw.
export type DockedPanelName = "files" | "tasks" | "chats" | "assets";
export type OpenPanel = DockedPanelName | null;

const PANELS: readonly DockedPanelName[] = ["files", "tasks", "chats", "assets"];

export interface DockedPanelCallbacks {
  // onOpen/onClose fire only on a real state change, so reapplying the
  // current state (after a remount) does not re-read disk state.
  onOpen?: (name: DockedPanelName) => void;
  onClose?: (name: DockedPanelName) => void;
  // onChange fires after every application, changed or not — the
  // chrome's pressed state follows this one value instead of tracking
  // its own copy.
  onChange?: (open: OpenPanel) => void;
}

export class DockedPanels {
  private openPanel: OpenPanel = null;
  private gestureCount = 0;
  private resolve: (name: DockedPanelName) => HTMLElement | null;
  private cb: DockedPanelCallbacks;

  constructor(
    resolve: (name: DockedPanelName) => HTMLElement | null,
    cb: DockedPanelCallbacks = {},
  ) {
    this.resolve = resolve;
    this.cb = cb;
  }

  get open(): OpenPanel {
    return this.openPanel;
  }

  isOpen(name: DockedPanelName): boolean {
    return this.openPanel === name;
  }

  // Bumped by every open or close except those whose caller passes
  // gesture=false (teardown, remount, the tutorial), so a changed count
  // means the user acted on a panel.
  get gestures(): number {
    return this.gestureCount;
  }

  set(next: OpenPanel, gesture = true): void {
    if (gesture) this.gestureCount++;
    const previous = this.openPanel;
    this.openPanel = next;
    const hosts = PANELS.map((name) => [name, this.resolve(name)] as const);
    const update = (): void => {
      for (const [name, host] of hosts) {
        if (host === null) continue;
        const open = name === next;
        host.setAttribute("data-open", open ? "true" : "false");
        host.setAttribute("aria-hidden", open ? "false" : "true");
        host.inert = !open;
      }
    };
    update();
    if (previous !== next) {
      if (previous !== null) this.cb.onClose?.(previous);
      if (next !== null) this.cb.onOpen?.(next);
    }
    this.cb.onChange?.(next);
  }

  toggle(name: DockedPanelName): void {
    this.set(this.openPanel === name ? null : name);
  }

  close(gesture = true): void {
    this.set(null, gesture);
  }

  // reapply pushes the current value back onto the DOM without firing a
  // transition: a panel remounted into a fresh region starts closed
  // unless the coordinator says otherwise.
  reapply(): void {
    this.set(this.openPanel, false);
  }
}
