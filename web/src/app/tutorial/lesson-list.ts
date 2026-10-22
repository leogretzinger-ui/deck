import { escapeHtml } from "../../lib/escape.ts";
import { lessonRowHint } from "../../lib/hints.ts";
import { lucideIcon } from "../../ui/icons.ts";
import { requirementNotes, type RequirementFacts, type RequirementNote } from "./requirements.ts";
import type { TutorialLesson } from "./model.ts";

export interface LessonRow {
  caption: string;
  dimmed: boolean;
  enable: NonNullable<RequirementNote["enable"]> | null;
  done: boolean;
  current: boolean;
}

export function lessonRow(lesson: TutorialLesson, current: string, done: readonly string[], facts: RequirementFacts): LessonRow {
  const notes = requirementNotes(lesson.requires ?? [], facts);
  return {
    caption: [lesson.duration, ...notes.map((note) => note.text)].filter((part) => part !== undefined && part !== "").join(" · "),
    dimmed: notes.some((note) => note.unmet),
    enable: notes.find((note) => note.enable !== undefined)?.enable ?? null,
    done: done.includes(lesson.id),
    current: lesson.id === current,
  };
}

function renderRow(lesson: TutorialLesson, row: LessonRow): string {
  const mark = row.done ? `<span class="tutorial-lesson-mark" role="img" aria-label="Done">${lucideIcon("circle-check", 14)}</span>` : '<span class="tutorial-lesson-mark"></span>';
  const caption = row.caption === "" ? "" : `<span class="tutorial-lesson-caption">${escapeHtml(row.caption)}</span>`;
  const enable = row.enable === null ? "" : `<p class="tutorial-lesson-enable">${escapeHtml(row.enable.sentence)} <a href="${escapeHtml(row.enable.url)}" target="_blank" rel="noopener noreferrer" data-hint="${escapeHtml(row.enable.hint)}">${escapeHtml(row.enable.label)}</a></p>`;
  return `<button type="button" class="transition-colors hover:bg-wash-hover" data-tutorial-action="lesson" data-lesson="${escapeHtml(lesson.id)}" data-dimmed="${row.dimmed}"${row.current ? ' aria-current="step"' : ""} data-hint="${escapeHtml(lessonRowHint(lesson.title, lesson.duration))}">${mark}<span class="tutorial-lesson-text"><span class="tutorial-lesson-title">${escapeHtml(lesson.title)}</span>${caption}</span></button>${enable}`;
}

// The guided course sits under Get started and the optional chapters under
// Explore Deck; a catalog without optional lessons keeps the flat list.
// Every lesson is listed whether or not the installation can run it.
export function renderLessonList(
  catalog: readonly TutorialLesson[],
  current: string,
  done: readonly string[],
  facts: RequirementFacts,
): string {
  const button = (lesson: TutorialLesson) => renderRow(lesson, lessonRow(lesson, current, done, facts));
  const started = catalog.filter((entry) => !entry.optional);
  const explore = catalog.filter((entry) => entry.optional);
  if (explore.length === 0) return started.map(button).join("");
  return [
    started.length > 0 ? `<p class="tutorial-lesson-group">Get started</p>${started.map(button).join("")}` : "",
    `<p class="tutorial-lesson-group">Explore Deck</p>${explore.map(button).join("")}`,
  ].filter(Boolean).join("");
}
