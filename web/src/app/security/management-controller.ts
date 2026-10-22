import { SecurityClient, SecurityError, mayOpenWorkspace, type SecurityAction, type SecurityMethod, type SecurityState } from "../../security/client.ts";
import { clearCredential, IncompatiblePasskey, prepareProof, registerPasskey, saveCandidate, type PreparedProof } from "../../security/ceremony.ts";
import { validateNewEncryptionPassword, type MethodKind, type OpenedMethod, type UnlockCredential } from "../../security/envelope.ts";
import { formatRecoveryKey, generateRecoverySecret, parseRecoveryKey } from "../../security/recovery.ts";
import { downloadRecoveryKit, importRecoveryKit, printRecoveryKit, recoveryKit } from "../../security/kit.ts";
import { bindManagementActions, type ManagementAction } from "./management-actions.ts";
import { managementHtml, type ManagedMethod, type ManagementScreen, type ManagementView } from "./management.ts";

export interface MethodManagementContext {
  api: SecurityClient;
  state: SecurityState;
  recoveryMethod?: { id: string; revision: number };
  onClose(): Promise<void>;
  authorizationLost(): void;
}
interface CandidateFlow { kind: MethodKind; replacement?: SecurityMethod; candidate?: SecurityMethod; tested: boolean }
type ProofPurpose = "source" | "activate" | "remove" | "test";
const ref = (method: SecurityMethod) => ({ id: method.id, revision: method.revision });

export class MethodManagement {
  private readonly context: MethodManagementContext;
  private readonly render: (view: ManagementView) => void;
  private readonly address: string;
  private methods: SecurityMethod[] = [];
  private screen: ManagementScreen = { kind: "loading" };
  private flow?: CandidateFlow;
  private password?: string;
  private key?: string;
  private printDisposer?: () => void;
  private opened?: OpenedMethod;
  private credentialInUse?: UnlockCredential;
  private volumeCopy?: Uint8Array;
  private selected?: SecurityMethod;
  private removal?: SecurityMethod;
  private pending?: { id: string; purpose: "candidate" | "source" | "mutation" | "test" };
  private ceremony = new AbortController();
  private disposed = false;
  private busy = false;
  private error?: string;
  private notice?: string;

  constructor(context: MethodManagementContext, render: (view: ManagementView) => void, address = location.host) {
    this.context = context; this.render = render; this.address = address;
  }

  private present(method: SecurityMethod): ManagedMethod {
    const date = method.verifiedAt ? new Date(method.verifiedAt) : undefined;
    return { ...ref(method), kind: method.kind, label: method.label, status: method.status,
      ...(date && Number.isFinite(date.getTime()) ? { checked: date.toLocaleString() } : {}) };
  }
  private paint(): void {
    if (!this.disposed) this.render({ name: this.context.state.instanceId, methods: this.methods.map(method => this.present(method)), screen: this.screen, busy: this.busy, error: this.error, notice: this.notice });
  }
  private show(screen: ManagementScreen): void { this.screen = screen; this.error = undefined; this.notice = undefined; this.paint(); }
  private check(signal: AbortSignal): void { if (this.disposed || signal.aborted) throw new DOMException("Cancelled", "AbortError"); }
  private active(id: string): SecurityMethod {
    const method = this.methods.find(method => method.id === id && method.status === "active" && method.envelope);
    if (!method) throw new Error("This unlock method is no longer active.");
    return method;
  }
  private clear(): void {
    this.ceremony.abort(); this.ceremony = new AbortController();
    this.printDisposer?.(); this.printDisposer = undefined;
    this.opened?.dispose(); this.opened = undefined;
    clearCredential(this.credentialInUse); this.credentialInUse = undefined;
    this.volumeCopy?.fill(0); this.volumeCopy = undefined;
    this.password = undefined; this.key = undefined; this.flow = undefined; this.selected = undefined; this.removal = undefined;
  }
  dispose(): void { this.disposed = true; this.clear(); this.ceremony.abort(); }
  report(message: string): void { this.error = message; this.paint(); }

  async start(): Promise<void> {
    this.paint();
    await this.work(async signal => {
      if (!mayOpenWorkspace(this.context.state)) { this.lost(); return; }
      await this.reload(signal);
      this.show(this.home());
    });
  }
  private home(): ManagementScreen { return this.context.recoveryMethod ? { kind: "recovery-checklist", usedRecovery: this.context.recoveryMethod } : { kind: "list" }; }
  private lost(): void { this.dispose(); this.context.authorizationLost(); }
  private async reload(signal: AbortSignal): Promise<void> {
    const state = await this.context.api.state(signal);
    this.check(signal);
    if (!mayOpenWorkspace(state)) { this.lost(); return; }
    this.context.state = state;
    const methods = await this.context.api.methods(true);
    this.check(signal);
    this.methods = methods;
  }
  private async work(operation: (signal: AbortSignal) => Promise<void>): Promise<void> {
    if (this.busy || this.disposed) return;
    const signal = this.ceremony.signal;
    this.busy = true; this.error = undefined; this.notice = undefined; this.paint();
    try { await operation(signal); }
    catch (error) {
      if (!signal.aborted && !this.disposed) {
        if (error instanceof SecurityError && (error.status === 401 || error.status === 403)) this.lost();
        else if (this.pending) this.show({ kind: "unknown" });
        else if (error instanceof IncompatiblePasskey) { this.password = undefined; this.show({ kind: "incompatible" }); }
        else this.error = "That check did not complete. Check the saved method and try again. Existing methods have not been removed unless the workspace confirms it.";
      }
    } finally { if (!signal.aborted && !this.disposed) { this.busy = false; this.paint(); } }
  }

  async run(action: ManagementAction): Promise<void> {
    if (this.disposed) return;
    if (action.kind === "cancel" || (action.kind === "close" && this.context.recoveryMethod)) {
      this.clear(); this.busy = false;
      this.show(this.pending ? { kind: "unknown" } : this.home());
      return;
    }
    if (action.kind === "close" || action.kind === "open-workspace") { this.dispose(); await this.context.onClose(); return; }
    if (action.kind === "review-methods") { this.show({ kind: "list" }); return; }
    if (this.busy) return;
    switch (action.kind) {
      case "detail": this.show({ kind: "detail", method: this.present(this.active(action.id)) }); return;
      case "add": this.show({ kind: "choose", replacement: this.flow?.replacement ? this.present(this.flow.replacement) : undefined }); return;
      case "replace": {
        const method = this.active(action.id);
        this.flow = { kind: method.kind, replacement: method, tested: false };
        if (method.kind === "password") this.show({ kind: "password", replacement: this.present(method) });
        else if (method.kind === "recovery") this.authorize("source");
        else this.show({ kind: "choose", replacement: this.present(method) });
        return;
      }
      case "add-passkey": case "add-password": case "add-recovery": {
        const kind = action.kind === "add-passkey" ? "passkey" : action.kind === "add-password" ? "password" : "recovery";
        this.flow = { kind, replacement: this.flow?.replacement, tested: false };
        if (kind === "password") this.show({ kind: "password", replacement: this.flow.replacement ? this.present(this.flow.replacement) : undefined });
        else this.authorize("source");
        return;
      }
      case "password":
        validateNewEncryptionPassword(action.password);
        if (!this.flow || this.flow.kind !== "password") return;
        this.password = action.password;
        if (this.flow.replacement?.kind === "password" && action.current !== undefined) {
          this.selected = this.flow.replacement;
          this.authorize("source", this.selected);
          await this.work(signal => this.use(this.selected!, "source", { kind: "password", password: action.current! }, signal));
        } else this.authorize("source");
        return;
      case "remove": {
        const method = this.active(action.id);
        if (method.kind === "recovery") { await this.run({ kind: "replace", id: method.id }); return; }
        if (this.methods.filter(item => item.status === "active" && item.kind !== "recovery").length <= 1) { this.show({ kind: "last-method", method: this.present(method) }); return; }
        this.removal = method; this.show({ kind: "remove", method: this.present(method) }); return;
      }
      case "confirm-remove": if (this.removal) this.authorize("remove"); return;
      case "test": this.authorize("test", this.active(action.id)); return;
      case "use-method": {
        if (this.screen.kind !== "authorize") return;
        const purpose = this.screen.purpose;
        const method = this.active(action.id);
        this.authorize(purpose, method);
        if (method.kind === "passkey") await this.work(signal => this.use(method, purpose, undefined, signal));
        return;
      }
      case "credential": {
        if (this.screen.kind !== "authorize" || !this.selected) return;
        const method = this.selected, purpose = this.screen.purpose;
        await this.work(async signal => {
          const credential = await this.credential(method.kind, action.value);
          try { await this.use(method, purpose, credential, signal); } finally { clearCredential(credential); }
        }); return;
      }
      case "check-passkey": case "candidate":
        await this.work(async signal => {
          const candidate = this.flow?.candidate;
          if (!candidate || this.screen.kind !== "check") return;
          const credential = action.kind === "candidate" ? await this.credential(candidate.kind, action.value) : undefined;
          let proof: PreparedProof | undefined;
          try {
            proof = await this.proof(candidate, { operation: "verify-method", method: ref(candidate) }, credential, signal);
            await this.submit(proof, "candidate", signal);
            this.candidateChecked();
          } finally { this.release(proof, credential); }
        }); return;
      case "saved-key":
        if (this.flow?.candidate && this.key) { this.printDisposer?.(); this.printDisposer = undefined; this.key = undefined; this.show({ kind: "check", candidate: this.present(this.flow.candidate) }); }
        return;
      case "name":
        await this.work(async signal => {
          if (!this.flow?.candidate || !this.flow.tested) return;
          await this.context.api.label(this.flow.candidate, action.value); this.check(signal);
          this.flow.candidate = { ...this.flow.candidate, label: action.value };
          this.authorize("activate");
        }); return;
      case "activate": if (this.flow?.tested) this.authorize("activate"); return;
      case "copy-key": {
        const signal = this.ceremony.signal;
        if (this.key) { await navigator.clipboard.writeText(this.key); if (!this.disposed && !signal.aborted) { this.notice = "Key copied."; this.paint(); } }
        return;
      }
      case "download-kit": case "print-kit":
        if (this.key) {
          const signal = this.ceremony.signal;
          const kit = await recoveryKit(this.context.state.instanceId, this.address, this.key);
          if (this.disposed || signal.aborted) return;
          if (action.kind === "download-kit") { downloadRecoveryKit(kit); this.notice = "Download requested."; }
          else { this.printDisposer?.(); this.printDisposer = printRecoveryKit(kit); }
          this.paint();
        } return;
      case "import-kit": {
        const signal = this.ceremony.signal;
        const key = await importRecoveryKit(action.file, this.context.state.instanceId);
        if (this.disposed || signal.aborted) return;
        await this.run({ kind: this.screen.kind === "check" ? "candidate" : "credential", value: key }); return;
      }
      case "observe": await this.work(signal => this.observe(signal)); return;
    }
  }

  private authorize(purpose: ProofPurpose, method?: SecurityMethod): void {
    this.selected = method;
    this.show({ kind: "authorize", purpose, method: method ? this.present(method) : undefined });
  }
  private async credential(kind: MethodKind, value: string): Promise<UnlockCredential> {
    if (kind === "password") return { kind, password: value };
    if (kind === "recovery") return { kind, recoverySecret: await parseRecoveryKey(value) };
    throw new Error("Use the browser passkey prompt.");
  }
  private async proof(method: SecurityMethod, action: SecurityAction, credential: UnlockCredential | undefined, signal: AbortSignal): Promise<PreparedProof> {
    this.check(signal);
    if (!this.context.state.commitment) throw new Error("Workspace key metadata is unavailable.");
    this.credentialInUse = credential;
    const proof = await prepareProof(this.context.api, this.context.state.instanceId, this.context.state.commitment, method, action, credential, signal);
    if (this.disposed || signal.aborted) { proof.opened.dispose(); this.check(signal); }
    this.opened = proof.opened;
    return proof;
  }
  private release(proof: PreparedProof | undefined, credential: UnlockCredential | undefined): void {
    proof?.opened.dispose();
    if (this.opened === proof?.opened) this.opened = undefined;
    clearCredential(credential);
    if (this.credentialInUse === credential) this.credentialInUse = undefined;
  }
  private async submit(proof: PreparedProof, purpose: NonNullable<MethodManagement["pending"]>["purpose"], signal: AbortSignal): Promise<void> {
    this.check(signal);
    this.pending = { id: proof.challenge.operationId, purpose };
    const receipt = await this.context.api.operate({ challengeId: proof.challenge.id, signature: proof.signature, ...(proof.assertion ? { assertion: proof.assertion } : {}) }, signal);
    this.check(signal);
    if (receipt.id !== this.pending.id) throw new Error("The response belongs to a different operation.");
    const result = receipt.status === "complete" ? receipt : await this.context.api.observe(receipt.id, signal);
    this.check(signal);
    if (result.id !== this.pending.id || result.status !== "complete") throw new Error("The operation has not confirmed completion.");
    this.pending = undefined;
  }
  private async use(method: SecurityMethod, purpose: ProofPurpose, credential: UnlockCredential | undefined, signal: AbortSignal): Promise<void> {
    const candidate = this.flow?.candidate;
    const replacement = this.flow?.replacement;
    const action: SecurityAction = purpose === "activate" && candidate && this.flow?.tested
      ? { operation: replacement ? "replace-method" : "add-method", method: ref(method), target: ref(candidate), ...(replacement ? { replacement: ref(replacement) } : {}) }
      : purpose === "remove" && this.removal ? { operation: "remove-method", method: ref(method), target: ref(this.removal) }
      : { operation: "verify-method", method: ref(method) };
    let proof: PreparedProof | undefined;
    try {
      proof = await this.proof(method, action, credential, signal);
      await this.submit(proof, purpose === "activate" || purpose === "remove" ? "mutation" : purpose, signal);
      this.check(signal);
      if (purpose === "source") {
        const secret = proof.opened.copyUnlockSecret(); this.volumeCopy = secret;
        try { await this.save(secret, signal); } finally { secret.fill(0); if (this.volumeCopy === secret) this.volumeCopy = undefined; }
      } else await this.finished(signal, purpose === "test" ? "The saved method passed its unlock check." : "Unlock methods updated.");
    } finally { this.release(proof, credential); }
  }
  private async save(secret: Uint8Array, signal: AbortSignal): Promise<void> {
    this.check(signal);
    const flow = this.flow;
    if (!flow) throw new Error("Choose a new method first.");
    const api = this.context.api;
    const label = flow.kind === "passkey" ? "New passkey" : flow.kind === "password" ? "Encryption password" : "Recovery key";
    const prepared = await api.prepare(flow.kind, label, signal); this.check(signal);
    let credential: UnlockCredential | undefined;
    let registration: Awaited<ReturnType<typeof registerPasskey>>["registration"] | undefined;
    const clearCandidate = (): void => clearCredential(credential);
    signal.addEventListener("abort", clearCandidate, { once: true });
    try {
      if (flow.kind === "passkey") ({ credential, registration } = await registerPasskey(prepared, signal));
      else if (flow.kind === "password") {
        if (!this.password) throw new Error("Enter the new password again.");
        credential = { kind: "password", password: this.password };
      } else {
        credential = { kind: "recovery", recoverySecret: generateRecoverySecret() };
        const key = await formatRecoveryKey(credential.recoverySecret);
        this.check(signal); this.key = key;
      }
      this.check(signal);
      if (!credential) throw new Error("The new unlock method is unavailable.");
      const candidate = await saveCandidate(api, this.context.state.instanceId, prepared, secret, this.context.state.commitment!, credential, registration, signal);
      this.check(signal); flow.candidate = candidate;
      this.show(flow.kind === "recovery" ? { kind: "recovery-save", key: this.key! } : { kind: "check", candidate: this.present(candidate) });
    } finally {
      signal.removeEventListener("abort", clearCandidate);
      if (this.flow === flow) this.password = undefined;
      clearCandidate();
    }
  }
  private candidateChecked(): void {
    if (!this.flow?.candidate) return;
    this.flow.tested = true; this.key = undefined;
    this.show({ kind: "ready", candidate: this.present(this.flow.candidate), ...(this.flow.replacement ? { replacement: this.present(this.flow.replacement) } : {}) });
  }
  private async finished(signal: AbortSignal, notice: string): Promise<void> {
    await this.reload(signal); this.check(signal);
    this.flow = undefined; this.password = undefined; this.key = undefined; this.removal = undefined;
    this.screen = this.home(); this.notice = notice; this.error = undefined;
  }
  private async observe(signal: AbortSignal): Promise<void> {
    if (!this.pending) return;
    const pending = this.pending;
    const result = await this.context.api.observe(pending.id, signal); this.check(signal);
    if (result.id !== pending.id || result.status !== "complete") { this.error = "The workspace has not confirmed completion. Check again when it is reachable."; return; }
    this.pending = undefined;
    if (pending.purpose === "candidate" && this.flow?.candidate) this.candidateChecked();
    else await this.finished(signal, pending.purpose === "mutation" ? "Unlock methods updated." : "The saved method passed its unlock check. Start the change again when you are ready.");
  }
}

export async function mountMethodManagement(root: HTMLElement, context: MethodManagementContext): Promise<() => void> {
  let unbind: (() => void) | undefined;
  let previousScreen: ManagementScreen["kind"] | undefined;
  const controller = new MethodManagement(context, view => {
    const focused = document.activeElement?.getAttribute("name");
    unbind?.(); root.innerHTML = managementHtml(view);
    unbind = bindManagementActions(root, action => controller.run(action), message => controller.report(message));
    if (!view.busy && previousScreen !== view.screen.kind) root.querySelector<HTMLElement>("#method-title")?.focus();
    else if (!view.busy && focused) root.querySelector<HTMLInputElement>(`input[name="${CSS.escape(focused)}"],textarea[name="${CSS.escape(focused)}"]`)?.focus();
    previousScreen = view.screen.kind;
  });
  void controller.start();
  return () => { unbind?.(); controller.dispose(); };
}
