// A modal screen re-renders from markup that does not carry what you typed,
// so an owner snapshots its edited fields and focus before the rewrite and
// puts them back on the fields the new markup draws again.

export type FormField = HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement;

export interface ModalFocus {
  selector: string;
  selection: [number | null, number | null] | null;
}

// focusedModalField names the field inside host that has focus and its
// selection.
export function focusedModalField(host: HTMLElement | null): ModalFocus | null {
  const focused = document.activeElement;
  if (focused == null || host?.contains(focused) !== true) return null;
  if (!(focused instanceof HTMLInputElement || focused instanceof HTMLTextAreaElement || focused instanceof HTMLSelectElement)) return null;
  const selector = fieldSelector(focused);
  if (selector === null) return null;
  return { selector, selection: focused instanceof HTMLSelectElement ? null : [focused.selectionStart, focused.selectionEnd] };
}

// editedModalFields lists the fields inside host whose value differs from
// the one their markup drew.
export function editedModalFields(host: HTMLElement | null): Array<readonly [string, string]> {
  return [...host?.querySelectorAll<FormField>("input, textarea, select") ?? []]
    .filter(fieldEdited)
    .flatMap((field) => {
      const selector = fieldSelector(field);
      return selector === null ? [] : [[selector, field.value] as const];
    });
}

export function restoreModalEdits(host: HTMLElement | null, edited: ReadonlyArray<readonly [string, string]>): void {
  for (const [selector, value] of edited) {
    const field = host?.querySelector<FormField>(selector);
    if (field != null && field.value !== value) field.value = value;
  }
}

// restoreModalFocus reports whether the field took focus again, so an owner
// can skip the screen's initial focus.
export function restoreModalFocus(host: HTMLElement | null, focus: ModalFocus | null): boolean {
  if (focus === null) return false;
  const restored = host?.querySelector<HTMLElement>(focus.selector) ?? null;
  restored?.focus({ preventScroll: true });
  const selection = focus.selection;
  // An email field reports no selection and refuses one.
  if ((restored instanceof HTMLInputElement || restored instanceof HTMLTextAreaElement) && selection?.[0] != null) restored.setSelectionRange(selection[0], selection[1] ?? selection[0]);
  return restored !== null && document.activeElement === restored;
}

// fieldSelector names a modal field by its first data attribute, which a
// re-render of the same screen draws again.
function fieldSelector(field: FormField): string | null {
  const attribute = [...field.attributes].find((attr) => attr.name.startsWith("data-") && attr.name !== "data-hint");
  return attribute === undefined ? null : `${field.localName}[${attribute.name}="${CSS.escape(attribute.value)}"]`;
}

function fieldEdited(field: FormField): boolean {
  if (field instanceof HTMLSelectElement) return [...field.options].some((option) => option.selected !== option.defaultSelected);
  // A file input accepts only an empty value from script.
  if (field instanceof HTMLInputElement && (field.type === "checkbox" || field.type === "radio" || field.type === "file")) return false;
  return field.value !== field.defaultValue;
}
