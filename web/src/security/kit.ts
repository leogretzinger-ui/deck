import { formatRecoveryKey, parseRecoveryKey } from "./recovery.ts";

export interface RecoveryKit { format: "deck-recovery-kit"; version: 1; instanceId: string; address: string; recoveryKey: string; instructions: string }
const instructions = "Keep this kit outside the workspace and separately from your primary unlock method. Sign in to the named workspace, choose Use recovery key, then paste the key or import this file. This kit is not a backup: restoration also needs the matching encrypted workspace and its security metadata. Never give the key to whoever runs the machine.";

export async function recoveryKit(instanceId: string, address: string, key: string): Promise<RecoveryKit> {
  const secret = await parseRecoveryKey(key);
  try { return { format: "deck-recovery-kit", version: 1, instanceId, address, recoveryKey: await formatRecoveryKey(secret), instructions }; }
  finally { secret.fill(0); }
}

export function serializeRecoveryKit(kit: RecoveryKit): string { return JSON.stringify(kit, null, 2) + "\n"; }

export async function importRecoveryKit(file: Pick<File, "size" | "text">, instanceId: string): Promise<string> {
  if (file.size > 16_384) throw new Error("This file is too large to be a Deck recovery kit.");
  let value: unknown;
  try { value = JSON.parse(await file.text()); } catch { throw new Error("This file is not a Deck recovery kit."); }
  if (!value || typeof value !== "object") throw new Error("This file is not a Deck recovery kit.");
  const kit = value as Partial<RecoveryKit>;
  if (kit.format !== "deck-recovery-kit" || kit.version !== 1 || kit.instanceId !== instanceId || typeof kit.recoveryKey !== "string") {
    throw new Error("This recovery kit does not belong to this workspace, or its format is unsupported.");
  }
  const secret = await parseRecoveryKey(kit.recoveryKey);
  try { return await formatRecoveryKey(secret); }
  finally { secret.fill(0); }
}

export function downloadRecoveryKit(kit: RecoveryKit): void {
  const url = URL.createObjectURL(new Blob([serializeRecoveryKit(kit)], { type: "application/json" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = `deck-recovery-${kit.instanceId}.json`;
  document.body.append(link);
  try { link.click(); } finally { link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000); }
}

export function printRecoveryKit(kit: RecoveryKit): () => void {
  const frame = document.createElement("iframe");
  frame.title = "Recovery kit print view";
  frame.hidden = true;
  document.body.append(frame);
  const view = frame.contentWindow;
  const documentToPrint = frame.contentDocument;
  if (!view || !documentToPrint) { frame.remove(); throw new Error("The print view could not open."); }
  const title = documentToPrint.createElement("h1");
  title.textContent = "Deck recovery kit";
  const contents = documentToPrint.createElement("pre");
  contents.style.whiteSpace = "pre-wrap";
  contents.style.overflowWrap = "anywhere";
  contents.textContent = serializeRecoveryKit(kit);
  documentToPrint.body.append(title, contents);
  view.addEventListener("afterprint", () => frame.remove(), { once: true });
  try { view.focus(); view.print(); } catch (error) { frame.remove(); throw error; }
  return () => frame.remove();
}
