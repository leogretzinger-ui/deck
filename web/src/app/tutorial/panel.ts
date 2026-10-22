import { escapeHtml } from "../../lib/escape.ts";
import {
  ALL_LESSONS,
  ALL_LESSONS_HINT,
  BACK_HINT,
  CLOSE_TUTORIAL_HINT,
  COLLAPSE_TUTORIAL_HINT,
  CONTINUE,
  CONTINUE_HINT,
  FINISH_HINT,
  HIDE_STEP,
  HIDE_STEP_HINT,
  NEXT_HINT,
  RESUME,
  RESUME_HINT,
  SHOW_STEP,
  SHOW_STEP_HINT,
  SHOW_TUTORIAL_HINT,
  SKIP_STEP,
  SKIP_STEP_HINT,
} from "../../lib/hints.ts";
import { lucideIcon } from "../../ui/icons.ts";
import { GHOST_SM_BUTTON, ICON_BUTTON, PRIMARY_MD_BUTTON, SECONDARY_SM_BUTTON } from "../../ui/controls.ts";
import { TutorialModel, type TutorialState } from "./model.ts";
import { stepControl } from "./events.ts";
import { renderLessonList } from "./lesson-list.ts";
import { unknownRequirementFacts, type RequirementFacts } from "./requirements.ts";
import {
  emptyStepContext,
  SECONDARY_ACTION,
  renderDoneAction,
  renderStepContent,
  renderWelcome,
  substitute,
  WELCOME_TITLE,
  type StepContext,
} from "./step-view.ts";

export class TutorialPanel {
  private readonly panel: HTMLElement;
  private readonly mobileBar: HTMLElement;
  // The desktop bar: the dock's chrome row while Files, Tasks or Chats is open.
  private readonly deskBar: HTMLElement;
  private readonly content: HTMLElement | null;
  private readonly announcer: HTMLElement;
  private announcedKey = "";
  private focusStep = false;
  private readonly boundary = window.matchMedia("(max-width: 600px)");
  private readonly resize: ResizeObserver;
  private state: TutorialState | null = null;
  private openPanel: string | null = null;
  private lessonsOpen = false;
  private wasResumePage = false;
  private disposed = false;
  private swipeStart: number | null = null;
  // The outcome of the last action, shown under the step it was taken on.
  private notice: { key: string; message: string } | null = null;
  private renderedKey = "";
  // The step whose prompt the user expanded on a phone.
  private promptOpenKey = "";
  private seenAdvances = 0;
  private seenStepKey = "";

  constructor(private readonly root: HTMLElement, private readonly model: TutorialModel,
    private readonly callbacks: {
      showStep(): void;
      onClose?(): void;
      sharedPreferences?: boolean;
      // What the surface knows about the user's choices so far.
      context?(): Partial<StepContext>;
      // What this installation can do, for the lesson list's requirement notes.
      requirementFacts?(): RequirementFacts;
      // The lesson list was opened; the surface may refresh its facts.
      onLessonsOpen?(): void;
      // text is the step's substituted prompt or command, empty otherwise.
      onAction?(action: string, text: string, arg: string | null): void;
    }) {
    this.panel = root.querySelector<HTMLElement>('[data-region="tutorial"]')!;
    this.mobileBar = root.querySelector<HTMLElement>('[data-region="tutorial-mobile-bar"]')!;
    this.deskBar = root.querySelector<HTMLElement>('[data-region="tutorial-bar"]')!;
    this.content = root.querySelector<HTMLElement>('[data-region="content"]');
    this.announcer = document.createElement("div");
    this.announcer.className = "sr-only";
    this.announcer.setAttribute("role", "status");
    this.announcer.setAttribute("aria-live", "polite");
    this.announcer.dataset.region = "tutorial-announce";
    root.append(this.announcer);
    root.addEventListener("click", this.onClick);
    root.addEventListener("keydown", this.onKeydown, true);
    this.panel.addEventListener("pointerdown", this.onPointerDown);
    this.panel.addEventListener("pointerup", this.onPointerUp);
    this.panel.addEventListener("pointercancel", this.cancelSwipe);
    this.boundary.addEventListener("change", this.onBoundary);
    this.resize = new ResizeObserver(this.positionSheet);
    const dock = root.querySelector('[data-region="terminal-dock"]');
    if (dock) this.resize.observe(dock);
    for (const name of ["files", "tasks", "chats", "assets"]) {
      const host = root.querySelector(`[data-region="${name}"]`);
      if (host) this.resize.observe(host);
    }
  }
  // The phone layout, including framed previews that do not resize the browser.
  isMobile(): boolean {
    return this.boundary.matches || this.root.querySelector('[data-viewport="narrow"]') !== null;
  }
  // Focus is on the tutorial's own controls, or on nothing in particular.
  focusIsFree(): boolean {
    const active = document.activeElement;
    return !(active instanceof Element) || active === document.body
      || this.panel.contains(active) || this.mobileBar.contains(active) || this.deskBar.contains(active);
  }
  private readonly onKeydown = (event: KeyboardEvent): void => {
    if (this.disposed || event.key !== "Escape" || !this.state?.stepShown) return;
    const active = document.activeElement;
    if (!(active instanceof Element) || !(this.panel.contains(active) || this.deskBar.contains(active))) return;
    event.preventDefault();
    event.stopPropagation();
    this.foldStep();
  };
  private foldStep(): void {
    this.model.hideStep();
    this.deskBar.querySelector<HTMLElement>("button")?.focus();
  }
  private readonly positionSheet = (): void => {
    const open = this.root.querySelector<HTMLElement>(
      '[data-region="files"][data-open="true"], [data-region="tasks"][data-open="true"], [data-region="chats"][data-open="true"], [data-region="assets"][data-open="true"]');
    if (open && this.content) this.content.style.setProperty("--tutorial-bar-w", `${open.offsetWidth}px`);
    const dock = this.root.querySelector<HTMLElement>('[data-region="terminal-dock"]');
    this.panel.style.setProperty("--tutorial-dock-bottom", `${dock?.getBoundingClientRect().height ?? 0}px`);
  };
  private readonly onBoundary = (): void => {
    if (this.state) this.update(this.state, this.openPanel);
  };
  private readonly cancelSwipe = (): void => { this.swipeStart = null; };
  private readonly onPointerDown = (event: PointerEvent): void => {
    if (event.pointerType === "mouse" || !(event.target instanceof Element) || !event.target.closest("[data-tutorial-handle]")) return;
    this.swipeStart = event.clientY;
    (event.target as HTMLElement).setPointerCapture(event.pointerId);
  };
  private readonly onPointerUp = (event: PointerEvent): void => {
    const start = this.swipeStart;
    this.swipeStart = null;
    if (start !== null && event.clientY - start > 45) this.model.collapse();
  };
  private readonly onClick = (event: MouseEvent): void => {
    if (this.disposed || !(event.target instanceof Element)) return;
    const button = event.target.closest<HTMLElement>("[data-tutorial-action]");
    if (!button || !this.root.contains(button)) return;
    switch (button.dataset.tutorialAction) {
      case "close": this.model.close(); this.callbacks.onClose?.(); break;
      case "collapse": this.model.collapse(); this.mobileBar.querySelector<HTMLElement>("button")?.focus(); break;
      case "show": this.callbacks.showStep(); break;
      case "toggle-step":
        if (this.state?.stepShown) { this.foldStep(); break; }
        this.focusStep = true;
        this.model.showStep();
        this.focusStep = false;
        break;
      case "hide-step": this.foldStep(); break;
      case "next": this.model.next(); if (this.model.state.dismissed) this.callbacks.onClose?.(); break;
      case "skip": this.model.skip(); break;
      case "back": this.model.back(); break;
      case "lessons":
        this.lessonsOpen = !this.lessonsOpen;
        if (this.lessonsOpen) this.callbacks.onLessonsOpen?.();
        this.model.leaveResumePage();
        this.onBoundary();
        break;
      case "resume": this.model.leaveResumePage(); break;
      case "start": this.model.start(); break;
      case "lesson": this.lessonsOpen = false; this.model.selectLesson(button.dataset.lesson ?? ""); break;
      case "toggle-prompt":
        this.promptOpenKey = this.promptOpenKey === this.renderedKey ? "" : this.renderedKey;
        this.onBoundary();
        break;
      case "copy": void this.copy(button.dataset.tutorialText ?? ""); break;
      default: this.callbacks.onAction?.(button.dataset.tutorialAction ?? "", button.dataset.tutorialText ?? "", button.dataset.tutorialArg ?? null);
    }
  };
  private async copy(text: string): Promise<void> {
    try {
      await navigator.clipboard.writeText(text);
      this.say("Copied. Paste it where you want it.");
    } catch {
      this.say("Could not copy. Select the text and copy it yourself.");
    }
  }
  // Shows the outcome of an action under the current step.
  say(message: string): void {
    if (this.disposed || !this.state) return;
    this.notice = { key: `${this.state.lesson}:${this.state.step}`, message };
    this.onBoundary();
  }
  update(state: TutorialState, openPanel: string | null): void {
    const otherPanelOpen = openPanel !== null;
    if (this.disposed) return;
    // The fold state belongs to the model; tell it what is docked before
    // reading the state this paint draws.
    this.model.setDocked(this.isMobile() ? null : openPanel);
    state = this.model.state;
    // A new resume page, or a closed tutorial, starts from the step view
    // rather than a catalog left open before.
    if ((state.resumePage && !this.wasResumePage) || state.dismissed) this.lessonsOpen = false;
    this.wasResumePage = state.resumePage;
    this.state = state;
    this.openPanel = openPanel;
    // Framed previews use the same phone layout without resizing the browser.
    const mobile = this.isMobile();
    this.root.dataset.tutorialMobile = String(mobile);
    // Failed reads open only by explicit request; suspended controllers stay hidden.
    const active = state.phase === "ready"
      && !state.dismissed
      && (!state.loadError || state.visible || state.collapsed);
    // On a phone an open Files, Tasks or Chats panel hides the tutorial and
    // closing it restores the sheet. On desktop the tutorial folds into a
    // bar above the panel, and its page unfolds over the panel on request.
    // Collapse affects only phone sheets.
    const folded = active && otherPanelOpen && !mobile;
    const over = folded && state.stepShown;
    const full = (active && !otherPanelOpen && (!state.collapsed || !mobile)) || over;
    const focused = this.panel.contains(document.activeElement)
      ? (document.activeElement as HTMLElement).dataset.tutorialAction
      : undefined;
    const focusedLesson = this.panel.contains(document.activeElement)
      ? (document.activeElement as HTMLElement).closest<HTMLElement>("nav [data-lesson]")?.dataset.lesson : undefined;
    const focusedBar = document.activeElement instanceof Element
      ? document.activeElement.closest<HTMLElement>('[data-region="tutorial-mobile-bar"], [data-region="tutorial-bar"]') : null;
    const pageFocused = this.panel.contains(document.activeElement);
    const stepFocused = pageFocused && document.activeElement?.matches(".tutorial-body h3") === true;
    this.panel.dataset.over = String(over);
    this.panel.dataset.open = String(full);
    this.panel.setAttribute("aria-hidden", String(!full));
    this.panel.inert = !full;
    const lesson = this.model.catalog.find((entry) => entry.id === state.lesson) ?? this.model.catalog[0]!;
    const step = lesson.steps[state.step] ?? lesson.steps[0]!;
    const shown = this.model.visibleSteps(lesson);
    const position = Math.max(shown.indexOf(state.step), 0);
    const count = `${position + 1} of ${shown.length}`;
    const stepTitle = substitute(step.title, { ...emptyStepContext, ...this.callbacks.context?.() }).text;
    const key = `${lesson.id}-${state.step}`;
    const context: StepContext = { ...emptyStepContext, ...this.callbacks.context?.(), promptExpanded: this.promptOpenKey === key };
    // When an observed event moves the lesson on, the outcome of the action
    // that caused it moves with it.
    const stepKey = `${state.lesson}:${state.step}`;
    if (state.advances !== this.seenAdvances && this.notice?.key === this.seenStepKey) this.notice = { ...this.notice, key: stepKey };
    this.seenAdvances = state.advances;
    this.seenStepKey = stepKey;
    const notice = this.notice !== null && this.notice.key === stepKey ? this.notice.message : "";
    const next = step.done === undefined ? undefined : this.model.catalog.find((entry) => entry.id === step.done!.next);
    // A repaint of the same step keeps the reading position; a new step starts at the top.
    const scrolled = this.renderedKey === key ? this.panel.querySelector<HTMLElement>(".tutorial-body")?.scrollTop ?? 0 : 0;
    this.renderedKey = key;
    const button = (action: string, content: string, classes: string, hint: string, extra = "") =>
      `<button type="button" class="${classes}" data-tutorial-action="${action}" data-hint="${escapeHtml(hint)}" ${extra}>${content}</button>`;
    const last = this.model.finishing;
    const welcome = state.welcome && !this.lessonsOpen;
    const resumePage = state.resumePage && !this.lessonsOpen && !state.welcome;
    const forward = last ? button("next", "Finish", PRIMARY_MD_BUTTON, FINISH_HINT)
      : step.done !== undefined ? button("next", "Next", PRIMARY_MD_BUTTON, NEXT_HINT)
        : stepControl(step.advanceOn) === "continue" ? button("next", CONTINUE, PRIMARY_MD_BUTTON, CONTINUE_HINT)
          : button("skip", SKIP_STEP, PRIMARY_MD_BUTTON, SKIP_STEP_HINT);
    this.panel.innerHTML = `
      <div data-tutorial-handle aria-hidden="true"><span></span></div>
      <header class="tutorial-header"><h2>Tutorial</h2>${button("lessons", ALL_LESSONS, GHOST_SM_BUTTON, ALL_LESSONS_HINT, `aria-expanded="${this.lessonsOpen}"`)}${over ? button("hide-step", HIDE_STEP, GHOST_SM_BUTTON, HIDE_STEP_HINT) : ""}${button("collapse", lucideIcon("chevron-down", 16), `tutorial-collapse ${ICON_BUTTON}`, COLLAPSE_TUTORIAL_HINT, 'aria-label="Collapse tutorial"')}${button("close", lucideIcon("x", 16), ICON_BUTTON, CLOSE_TUTORIAL_HINT, 'aria-label="Close tutorial"')}</header>
      <div class="tutorial-body">
        ${state.unsupportedVersion ? '<p role="status">These preferences were saved by a newer version of Deck. Changes here will not be saved.</p>' : ""}
        ${state.loadError ? '<p role="status">Tutorial preferences could not be loaded. Changes here will not be saved.</p>' : ""}
        ${state.saveError ? '<p role="status">Your latest tutorial changes could not be saved.</p>' : ""}
        ${state.scope === "installation" || this.callbacks.sharedPreferences ? '<p class="tutorial-scope">Tutorial preferences are shared by everyone using this installation.</p>' : ""}
        ${this.lessonsOpen ? `<nav aria-label="Tutorial lessons">${renderLessonList(this.model.catalog, lesson.id, state.done, { ...unknownRequirementFacts, ...this.callbacks.requirementFacts?.() })}</nav>` : welcome ? renderWelcome() : resumePage ? `
          <h3 tabindex="-1">Pick up where you left off</h3>
          <p>${escapeHtml(`${lesson.title} · step ${count} · ${stepTitle}`)}</p>
          <p class="tutorial-actions">${button("resume", RESUME, PRIMARY_MD_BUTTON, RESUME_HINT)}${button("lessons", ALL_LESSONS, SECONDARY_ACTION, ALL_LESSONS_HINT)}</p>` : `
          <div class="tutorial-lesson"><span>${escapeHtml(lesson.title)}</span><span>Step ${count}</span></div>
          <div class="tutorial-progress" aria-hidden="true">${shown.map((index) => `<span data-complete="${index <= state.step}"></span>`).join("")}</div>
          <h3${over ? ' tabindex="-1"' : ""}>${escapeHtml(substitute(step.title, context).text)}</h3>${renderStepContent(step, context, key)}${step.done === undefined ? "" : renderDoneAction(step.done.next, next?.title ?? null)}${notice === "" ? "" : `<p role="status" class="tutorial-status">${escapeHtml(notice)}</p>`}`}
      </div>
      ${this.lessonsOpen || welcome || resumePage ? "" : `<footer class="tutorial-footer">${button("back", "Back", SECONDARY_ACTION, BACK_HINT, this.model.starting ? "disabled" : "")}${forward}</footer>`}`;
    const body = this.panel.querySelector<HTMLElement>(".tutorial-body");
    if (body) body.scrollTop = scrolled;
    const row = this.lessonsOpen && focusedLesson !== undefined
      ? [...this.panel.querySelectorAll<HTMLElement>("nav [data-lesson]")].find((entry) => entry.dataset.lesson === focusedLesson) : undefined;
    if (full && row) row.focus();
    else if (full && focused) {
      const restored = this.panel.querySelector<HTMLButtonElement>(
        `[data-tutorial-action="${focused === "lesson" ? "next" : focused}"]`,
      );
      // Back becomes disabled at the first step, so keep keyboard navigation usable.
      const target = restored && !restored.disabled
        ? restored
        : this.panel.querySelector<HTMLButtonElement>('[data-tutorial-action="next"], [data-tutorial-action="skip"]');
      target?.focus();
    }
    const label = state.welcome ? `Tutorial, ${WELCOME_TITLE}` : `Tutorial, step ${position + 1} of ${shown.length}`;
    const barCount = state.welcome ? "" : `<span class="tutorial-bar-count">${position + 1}/${shown.length}</span>`;
    const barLabel = `<span class="tutorial-bar-label">${barCount}<span class="tutorial-bar-title">${escapeHtml(state.welcome ? WELCOME_TITLE : stepTitle)}</span></span>`;
    this.mobileBar.hidden = !(active && state.collapsed && !otherPanelOpen && mobile);
    this.mobileBar.setAttribute("role", "region");
    this.mobileBar.setAttribute("aria-label", label);
    this.mobileBar.innerHTML = barLabel + button("show", SHOW_STEP, SECONDARY_SM_BUTTON, SHOW_TUTORIAL_HINT);
    this.deskBar.hidden = !folded;
    this.deskBar.setAttribute("role", "region");
    this.deskBar.setAttribute("aria-label", label);
    this.deskBar.innerHTML = barLabel + button("toggle-step", SHOW_STEP, SECONDARY_SM_BUTTON, SHOW_STEP_HINT, `aria-expanded="${over}"`);
    this.content?.toggleAttribute("data-tutorial-folded", folded);
    if (focusedBar && !focusedBar.hidden) focusedBar.querySelector<HTMLElement>("button")?.focus();
    // Navigating from the unfolded page folds it; keep the keyboard on the control that reopens it.
    if (pageFocused && folded && !over) this.deskBar.querySelector<HTMLElement>("button")?.focus();
    if ((this.focusStep || stepFocused) && over) {
      this.panel.querySelector<HTMLElement>(".tutorial-body h3")?.focus();
    }
    this.focusStep = false;
    if (!active) this.announcedKey = "";
    else if (this.announcedKey !== (state.welcome ? "welcome" : key)) {
      this.announcedKey = state.welcome ? "welcome" : key;
      this.announcer.textContent = state.welcome ? WELCOME_TITLE : `Step ${count}: ${stepTitle}`;
    }
    this.positionSheet();
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.root.removeEventListener("click", this.onClick);
    this.root.removeEventListener("keydown", this.onKeydown, true);
    this.announcer.remove();
    this.panel.removeEventListener("pointerdown", this.onPointerDown);
    this.panel.removeEventListener("pointerup", this.onPointerUp);
    this.panel.removeEventListener("pointercancel", this.cancelSwipe);
    this.boundary.removeEventListener("change", this.onBoundary);
    this.resize.disconnect();
  }
}
