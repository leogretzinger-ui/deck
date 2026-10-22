// Browser-local presentation preference; never part of backend sidebar ordering.
export const DEFAULT_SIDEBAR_WIDTH = 272;
export const SIDEBAR_WIDTH_KEY = "deck.sidebar-width";
export const MIN_SIDEBAR_WIDTH = 220;
export const MAX_SIDEBAR_WIDTH = 480;

export function parseSidebarWidth(value: string | null): number {
  if (value === null || value.trim() === "") return DEFAULT_SIDEBAR_WIDTH;
  const width = Number(value);
  return Number.isFinite(width) && width >= MIN_SIDEBAR_WIDTH && width <= MAX_SIDEBAR_WIDTH
    ? Math.round(width) : DEFAULT_SIDEBAR_WIDTH;
}

export function sidebarWidthBounds(available: number): { min: number; max: number } {
  // Leave at least 480 CSS pixels for the workspace at desktop breakpoints.
  const max = Math.max(MIN_SIDEBAR_WIDTH, Math.min(MAX_SIDEBAR_WIDTH, Math.floor(available - 480)));
  return { min: MIN_SIDEBAR_WIDTH, max };
}

export class SidebarWidth {
  private preferred = DEFAULT_SIDEBAR_WIDTH;
  private pointer: number | null = null;
  private startX = 0;
  private startWidth = 0;
  private observer: ResizeObserver;
  private readonly events = new AbortController();
  private readonly mobile = window.matchMedia("(width < 48rem)");

  private workspace: HTMLElement;
  private handle: HTMLElement;

  constructor(workspace: HTMLElement, handle: HTMLElement) {
    this.workspace = workspace;
    this.handle = handle;
    try { this.preferred = parseSidebarWidth(window.localStorage.getItem(SIDEBAR_WIDTH_KEY)); } catch { /* storage may be blocked */ }
    const options = { signal: this.events.signal };
    handle.addEventListener("pointerdown", this.down, options);
    handle.addEventListener("pointermove", this.move, options);
    handle.addEventListener("pointerup", this.up, options);
    handle.addEventListener("pointercancel", this.cancel, options);
    handle.addEventListener("lostpointercapture", this.cancel, options);
    handle.addEventListener("dblclick", this.reset, options);
    handle.addEventListener("keydown", this.key, options);
    window.addEventListener("blur", this.cancel, options);
    window.addEventListener("resize", this.refresh, options);
    this.mobile.addEventListener("change", this.refresh, options);
    this.observer = new ResizeObserver(this.refresh);
    this.observer.observe(workspace);
    this.refresh();
  }

  private bounds() { return sidebarWidthBounds(this.workspace.getBoundingClientRect().width); }
  private enabled() {
    return !this.mobile.matches && !this.workspace.closest('[data-viewport="narrow"]')
      && this.workspace.dataset.collapsed !== "true";
  }
  refresh = (): void => {
    const enabled = this.enabled();
    if (!enabled) this.cancel();
    const { min, max } = this.bounds();
    const width = Math.max(min, Math.min(max, this.preferred));
    // Mobile always uses the original drawer width, independent of desktop preference.
    this.workspace.style.setProperty("--sidebar-w", `${this.mobile.matches || this.workspace.closest('[data-viewport="narrow"]') ? DEFAULT_SIDEBAR_WIDTH : width}px`);
    const wasFocused = document.activeElement === this.handle;
    this.handle.hidden = !enabled;
    this.handle.tabIndex = enabled ? 0 : -1;
    this.handle.setAttribute("aria-valuemin", String(min));
    this.handle.setAttribute("aria-valuemax", String(max));
    this.handle.setAttribute("aria-valuenow", String(width));
    this.handle.setAttribute("aria-valuetext", `${width} pixels`);
    if (!enabled && wasFocused) {
      const candidates = this.workspace.querySelectorAll<HTMLElement>(
        '[data-region="sidebar-expand"], [data-live-sessions], [data-titlebar-sessions], [data-collapse], [data-sidebar-sheet-close]');
      const target = Array.from(candidates).find(element =>
        element.getClientRects().length > 0 && getComputedStyle(element).visibility !== "hidden"
        && !element.closest("[inert]"));
      target?.focus();
    }
  };
  private save(width: number): void {
    const { min, max } = this.bounds();
    this.preferred = Math.round(Math.max(min, Math.min(max, width)));
    this.refresh();
    try { window.localStorage.setItem(SIDEBAR_WIDTH_KEY, String(this.preferred)); } catch { /* in-memory resizing still works */ }
  }
  private down = (event: PointerEvent): void => {
    if (!this.enabled() || event.button !== 0 || !event.isPrimary || this.pointer !== null) return;
    // Keep terminal/input focus during pointer resizing.
    event.preventDefault();
    this.startX = event.clientX;
    this.startWidth = Number(this.handle.getAttribute("aria-valuenow"));
    this.pointer = event.pointerId;
    this.handle.setPointerCapture(event.pointerId);
    this.workspace.setAttribute("data-sidebar-resizing", "");
  };
  private move = (event: PointerEvent): void => {
    if (this.pointer !== event.pointerId) return;
    if (!this.enabled() || !(event.buttons & 1)) { this.cancel(); return; }
    this.save(this.startWidth + event.clientX - this.startX);
  };
  private up = (event: PointerEvent): void => {
    if (event.pointerId === this.pointer) this.cancel();
  };
  private cancel = (): void => {
    const pointer = this.pointer;
    this.pointer = null;
    if (pointer !== null && this.handle.hasPointerCapture(pointer)) this.handle.releasePointerCapture(pointer);
    this.workspace.removeAttribute("data-sidebar-resizing");
  };
  private reset = (): void => { if (this.enabled()) this.save(DEFAULT_SIDEBAR_WIDTH); };
  private key = (event: KeyboardEvent): void => {
    if (!this.enabled() || event.altKey || event.ctrlKey || event.metaKey) return;
    const width = Number(this.handle.getAttribute("aria-valuenow"));
    const step = event.shiftKey ? 32 : 8;
    const { min, max } = this.bounds();
    const next = event.key === "ArrowLeft" ? width - step : event.key === "ArrowRight" ? width + step
      : event.key === "Home" ? min : event.key === "End" ? max
      : event.key === "Enter" ? DEFAULT_SIDEBAR_WIDTH : null;
    if (next === null) return;
    event.preventDefault();
    this.cancel();
    this.save(next);
  };
  dispose(): void {
    this.cancel();
    this.events.abort();
    this.observer.disconnect();
  }
}
