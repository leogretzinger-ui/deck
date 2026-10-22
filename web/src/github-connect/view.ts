import { escapeHtml } from "../lib/escape.ts";
import * as hints from "../lib/hints.ts";
import { PRIMARY_MD_BUTTON, SECONDARY_MD_BUTTON } from "../ui/controls.ts";
import type { GitHubConnectScreen } from "./model.ts";

export const GITHUB_DEVICE_URL = "https://github.com/login/device";
const INSTALL_GUIDE = "https://github.com/cli/cli#installation";

function message(text: string): string {
  return `<p data-github-message role="status" class="text-body text-text-primary">${text}</p>`;
}

function installHint(platform: string): string {
  const command = (label: string, line: string) =>
    `<p data-github-install class="text-body-sm text-text-secondary">${label}, run <code class="font-mono select-all text-text-primary">${line}</code>.</p>`;
  if (platform === "darwin") return command("On macOS", "brew install gh");
  if (platform === "windows") return command("On Windows", "winget install --id GitHub.cli");
  const guide = `<a href="${INSTALL_GUIDE}" target="_blank" rel="noopener noreferrer" data-hint="${hints.OPEN_GITHUB_CLI_INSTALL_GUIDE}" class="text-accent underline">GitHub CLI install guide</a>`;
  return `<p data-github-install class="text-body-sm text-text-secondary">${platform === "linux" ? "On Linux, follow the" : "Follow the"} ${guide}.</p>`;
}

export function githubScreenHtml(screen: GitHubConnectScreen): string {
  switch (screen.kind) {
    case "checking":
      return message("Checking your GitHub connection…");
    case "starting":
      return message("Starting GitHub sign-in…");
    case "code":
      return `<div class="flex flex-col items-center gap-4 py-2 text-center">
        <output data-github-code aria-label="One-time code" class="select-all font-mono text-title-2 font-semibold text-text-primary">${escapeHtml(screen.code)}</output>
        <button type="button" data-github-copy data-hint="${hints.COPY_CODE_AND_OPEN_GITHUB}" class="${PRIMARY_MD_BUTTON}">Copy code and open GitHub</button>
        <p class="text-body-sm text-text-secondary">Deck connects as soon as you approve it on GitHub.</p>
      </div>`;
    case "connected":
      return message(screen.login === "" ? "Connected to GitHub"
        : `Connected as <span data-github-login class="font-medium">${escapeHtml(screen.login)}</span>`) +
        `<p class="text-body-sm text-text-secondary">To use a different account, sign out with the GitHub CLI and connect again.</p>`;
    case "missing-cli":
      return message("Deck can't find the GitHub CLI (gh). Install it, then try again.") + installHint(screen.platform);
    case "ended":
      return message("Sign-in ended before GitHub approved it.");
    case "notice":
    case "failed":
      return message(escapeHtml(screen.message));
  }
}

export function githubFootHtml(screen: GitHubConnectScreen): string {
  const retry = `<button type="button" data-github-retry data-hint="${hints.RETRY_GITHUB_CONNECT}" class="${PRIMARY_MD_BUTTON}">Try again</button>`;
  const cancel = `<button type="button" data-github-cancel data-hint="${hints.CANCEL_GITHUB_SIGN_IN}" class="${SECONDARY_MD_BUTTON}">Cancel</button>`;
  switch (screen.kind) {
    case "checking":
      return "";
    case "starting":
      return screen.running ? cancel : "";
    case "code":
      return cancel;
    case "connected":
      return `<button type="button" data-github-done data-hint="${hints.FINISH_GITHUB_CONNECT}" class="${PRIMARY_MD_BUTTON}">Done</button>`;
    case "missing-cli":
    case "ended":
    case "failed":
      return retry;
    case "notice":
      return `<button type="button" data-github-done data-hint="${hints.CLOSE_THIS_DIALOG}" class="${SECONDARY_MD_BUTTON}">Close</button>`;
  }
}
