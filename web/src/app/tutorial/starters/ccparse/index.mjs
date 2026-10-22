// Parses one conventional commit subject. See PRD.md for the form.
const FORM = /^([a-z]+)(?:\(([^)]+)\))?!?: (.+)$/;

export function parse(subject) {
  const text = String(subject ?? "").trim();
  const match = FORM.exec(text);
  if (!match) return { type: null, scope: null, breaking: false, description: text };
  return { type: match[1], scope: match[2] ?? null, breaking: false, description: match[3] };
}
