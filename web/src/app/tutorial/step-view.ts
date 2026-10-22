import { escapeHtml } from "../../lib/escape.ts";
import {
  ALL_LESSONS,
  ALL_LESSONS_HINT,
  COPY_TEXT_HINT,
  EXPLORE_ON_MY_OWN,
  EXPLORE_ON_MY_OWN_HINT,
  NEEDS_A_TASK,
  SHOW_FULL_PROMPT_HINT,
  SHOW_LESS_PROMPT_HINT,
  START_TUTORIAL,
  START_TUTORIAL_HINT,
  nextLessonHint,
  tutorialActionHint,
} from "../../lib/hints.ts";
import { INLINE_LINK_BUTTON, PRIMARY_MD_BUTTON, SECONDARY_MD_BUTTON } from "../../ui/controls.ts";
import { renderMarkdown } from "../../ui/markdown.ts";
import { DEFERRED_REASON, WIRED_ACTIONS, actionWired } from "./actions.ts";
import type { LessonCall } from "./lessons.ts";
import type { TutorialStep } from "./model.ts";

// What the panel knows when it draws a step. Anything unknown stays null
// and the text says so in words instead of showing a placeholder.
export interface StepContext {
  // The agent the user chose in this tutorial.
  agent: string | null;
  task: { title: string; description: string } | null;
  // Path of the relnotes practice project.
  relnotes: string | null;
  // Whether a lesson condition (agent_needs_login, agent_cannot_generate_tasks, ...) holds.
  // Conditions nothing can tell yet are false.
  holds(condition: LessonCall): boolean;
  // The step's first action has succeeded, so then_primary replaces primary.
  primaryDone: boolean;
  // A long prompt is clamped on a phone until the user asks for all of it.
  promptExpanded: boolean;
}

export const emptyStepContext: StepContext = {
  agent: null,
  task: null,
  relnotes: null,
  holds: () => false,
  primaryDone: false,
  promptExpanded: false,
};

const PLACEHOLDER = /\{(agent|task\.title|task\.description|starter\.relnotes)\}/g;

interface Substituted {
  text: string;
  // False when a task or starter value was unknown and a stand-in was used.
  complete: boolean;
}

function valueOf(key: string, context: StepContext): { value: string; known: boolean } {
  switch (key) {
    case "agent": return { value: context.agent ?? "your agent", known: true };
    case "task.title": return context.task === null ? { value: "your task", known: false } : { value: context.task.title, known: true };
    case "task.description": return context.task === null ? { value: "", known: false } : { value: context.task.description, known: true };
    default: return context.relnotes === null ? { value: "relnotes", known: false } : { value: context.relnotes, known: true };
  }
}

function capitalized(value: string): string {
  return value.charAt(0).toUpperCase() + value.slice(1);
}

// Plain-text substitution for labels, prompts and commands.
export function substitute(text: string, context: StepContext): Substituted {
  let complete = true;
  let dropped = false;
  const out = text.replace(PLACEHOLDER, (_match, key: string, offset: number) => {
    const { value, known } = valueOf(key, context);
    if (!known) complete = false;
    if (value === "") dropped = true;
    return offset === 0 && key === "agent" && context.agent === null ? capitalized(value) : value;
  });
  return { text: dropped ? out.replace(/ {2,}/g, " ").trim() : out, complete };
}

// Substitution over rendered HTML: values are escaped here, after the
// markdown pass, so a task title can never be read as markup or as a link.
function substituteHtml(html: string, context: StepContext): string {
  return html.replace(PLACEHOLDER, (_match, key: string, offset: number) => {
    const { value } = valueOf(key, context);
    return escapeHtml(offset === 0 && key === "agent" && context.agent === null ? capitalized(value) : value);
  });
}

function markdown(source: string, context: StepContext): string {
  return substituteHtml(renderMarkdown(source, undefined, WIRED_ACTIONS), context);
}

function small(text: string | undefined, context: StepContext, className: string): string {
  return text === undefined ? "" : `<p class="${className}">${escapeHtml(substitute(text, context).text)}</p>`;
}

function textBlock(text: string, context: StepContext, kind: "prompt" | "command"): string {
  const expanded = kind === "prompt" && context.promptExpanded ? ' data-expanded="true"' : "";
  const block = `<pre class="tutorial-${kind}"${expanded}><code>${escapeHtml(substitute(text, context).text)}</code></pre>`;
  if (kind !== "prompt") return block;
  const label = context.promptExpanded ? "Show less" : "Show full prompt";
  return `${block}<p class="tutorial-prompt-more"><button type="button" class="${INLINE_LINK_BUTTON}" data-tutorial-action="toggle-prompt" data-hint="${context.promptExpanded ? SHOW_LESS_PROMPT_HINT : SHOW_FULL_PROMPT_HINT}" aria-expanded="${context.promptExpanded}">${label}</button></p>`;
}

export const SECONDARY_ACTION = `${SECONDARY_MD_BUTTON} disabled:opacity-45`;

interface ButtonSpec {
  label: string;
  call: LessonCall;
  // The step the button sits on, which decides whether its action is wired
  // and carries the details some actions need.
  step: TutorialStep;
  text?: string;
  complete?: boolean;
  dimmed?: boolean;
  secondary?: boolean;
}

// The argument a button sends with its action: the one the lesson wrote on
// the call, the step's task count for generate-tasks, or the step's target
// project for insert-prompt and run-command.
function callArg(call: LessonCall, step: TutorialStep): string | null {
  if (call.arg !== null) return call.arg;
  if (call.name === "generate-tasks" && step.generateCount !== undefined) return `count=${step.generateCount}`;
  if ((call.name === "insert-prompt" || call.name === "run-command") && step.target !== undefined) return `target=${step.target}`;
  return null;
}

// A disabled button says why in text beside it, where everyone can read it.
function disabledButton(id: string, classes: string, label: string, dimmed: string, reason: string): string {
  return `<button type="button" class="${classes}" disabled aria-disabled="true" aria-describedby="${id}"${dimmed} data-hint="${escapeHtml(reason)}">${label}</button>`
    + `<span id="${id}" class="tutorial-reason">${reason}</span>`;
}

function button(id: string, spec: ButtonSpec, context: StepContext): string {
  const label = escapeHtml(spec.label);
  const classes = `${spec.secondary === true ? SECONDARY_ACTION : PRIMARY_MD_BUTTON}${spec.dimmed === true ? " opacity-45" : ""}`;
  const dimmed = spec.dimmed === true ? ' data-dimmed="true"' : "";
  if (!actionWired(spec.call)) return disabledButton(id, classes, label, dimmed, DEFERRED_REASON);
  if (spec.complete === false) return disabledButton(id, classes, label, dimmed, NEEDS_A_TASK);
  const text = spec.text === undefined ? "" : ` data-tutorial-text="${escapeHtml(spec.text)}"`;
  const arg = callArg(spec.call, spec.step);
  const argument = arg === null ? "" : ` data-tutorial-arg="${escapeHtml(arg)}"`;
  const hint = escapeHtml(tutorialActionHint(spec.call.name, context.agent, arg));
  return `<button type="button" class="${classes}" data-tutorial-action="${spec.call.name}"${text}${argument}${dimmed} data-hint="${hint}">${label}</button>`;
}

function copyButton(text: string): string {
  return `<button type="button" class="${SECONDARY_MD_BUTTON}" data-tutorial-action="copy" data-tutorial-text="${escapeHtml(text)}" data-hint="${COPY_TEXT_HINT}">Copy</button>`;
}

// The fallback is a plain sentence when its label carries its own link.
function fallbackHtml(step: TutorialStep, context: StepContext, id: string, secondary: boolean): string {
  if (step.fallback === undefined) return "";
  if (step.fallbackWhen !== undefined && !context.holds(step.fallbackWhen)) return "";
  if (/\]\((?:action|lesson):/.test(step.fallback)) return `<p class="tutorial-fallback">${markdown(step.fallback, context)}</p>`;
  if (step.fallbackAction === undefined) return "";
  return `<p class="tutorial-fallback">${button(id, { label: substitute(step.fallback, context).text, call: step.fallbackAction, step, secondary }, context)}</p>`;
}

// Everything under the step title: body, prompt or command, buttons and
// the small print, in that order.
export function renderStepContent(step: TutorialStep, context: StepContext, key: string): string {
  const parts: string[] = [];
  if (step.body !== "") parts.push(markdown(step.body, context));

  const promptText = step.prompt === undefined ? null : substitute(step.prompt, context);
  const commandText = step.command === undefined ? null : substitute(step.command, context);
  const promptHtml = promptText === null ? "" : textBlock(step.prompt!, context, "prompt");
  if (commandText !== null) parts.push(textBlock(step.command!, context, "command"));

  const fallbackLeads = step.fallbackPrimaryWhen !== undefined && context.holds(step.fallbackPrimaryWhen);
  const lead = context.primaryDone && step.thenPrimary !== undefined
    ? { label: step.thenPrimary, call: step.thenAction }
    : { label: step.primary, call: step.action };
  const buttons: string[] = [];
  if (lead.label !== undefined && lead.call !== undefined) {
    const carriesPrompt = lead.call.name === "insert-prompt" && promptText !== null;
    const carriesCommand = lead.call.name === "run-command" && commandText !== null;
    const text = carriesPrompt ? promptText!.text : carriesCommand ? commandText!.text : undefined;
    buttons.push(button(`${key}-primary`, {
      label: substitute(lead.label, context).text,
      call: lead.call,
      step,
      text,
      complete: carriesPrompt ? promptText!.complete : carriesCommand ? commandText!.complete : true,
      dimmed: fallbackLeads,
    }, context));
  }
  const shown = promptText ?? commandText;
  if (shown !== null) buttons.push(copyButton(shown.text));
  const actions = buttons.length > 0 ? `<p class="tutorial-actions">${buttons.join("")}</p>` : "";
  // The buttons share a box with the prompt so a phone can show them first.
  if (promptText !== null) parts.push(`<div class="tutorial-ask">${promptHtml}${actions}</div>`);
  else parts.push(actions);
  parts.push(small(step.primaryNote, context, "tutorial-note"));
  if (step.note !== undefined && (step.noteWhen === undefined || context.holds(step.noteWhen))) {
    parts.push(small(step.note, context, "tutorial-note"));
  }
  parts.push(small(step.commandNote, context, "tutorial-note"));
  parts.push(fallbackHtml(step, context, `${key}-fallback`, !fallbackLeads));
  parts.push(small(step.finePrint, context, "tutorial-note"));
  if (step.footer !== undefined) parts.push(`<div class="tutorial-footer-note">${markdown(step.footer, context)}</div>`);
  return parts.join("");
}

// The closing page: its line and a button to the lesson that follows.
export function renderDoneAction(next: string, nextTitle: string | null): string {
  if (next === "all-lessons") return `<p class="tutorial-actions"><button type="button" class="${PRIMARY_MD_BUTTON}" data-tutorial-action="lessons" data-hint="${ALL_LESSONS_HINT}">${ALL_LESSONS}</button></p>`;
  if (nextTitle === null) return "";
  return `<p class="tutorial-actions"><button type="button" class="${PRIMARY_MD_BUTTON}" data-tutorial-action="lesson" data-lesson="${escapeHtml(next)}" data-hint="${escapeHtml(nextLessonHint(nextTitle))}">${escapeHtml(nextTitle)}</button></p>`;
}

export const WELCOME_TITLE = "Welcome to Deck";

// The page in front of the first lesson: what Deck is and what the
// tutorial gets you.
export function renderWelcome(): string {
  return `<h3>${WELCOME_TITLE}</h3>
    <p>Deck is one place in your browser to run the coding agents you already pay for, each in its own real terminal, on your own machine.</p>
    <p>In the tutorial:</p>
    <ul class="tutorial-welcome-outcomes">
      <li>An agent fixes a real bug while you watch.</li>
      <li>You leave a session running and pick it up later.</li>
      <li>Two agents work on one problem together.</li>
      <li>You finish with your own repository set up.</li>
    </ul>
    <p>It takes about 20 minutes across three lessons. Your agents get a handful of small requests on your plan.</p>
    <p class="tutorial-welcome-actions">
      <button type="button" class="${PRIMARY_MD_BUTTON}" data-tutorial-action="start" data-hint="${START_TUTORIAL_HINT}">${START_TUTORIAL}</button>
      <button type="button" class="${SECONDARY_MD_BUTTON}" data-tutorial-action="close" data-hint="${EXPLORE_ON_MY_OWN_HINT}">${EXPLORE_ON_MY_OWN}</button>
    </p>`;
}

// A step with a `when:` condition is shown only while the condition holds.
export function stepHidden(step: TutorialStep, context: StepContext): boolean {
  return step.when !== undefined && !context.holds(step.when);
}
