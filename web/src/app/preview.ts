// Fixture entry only. Embedded production builds exclude preview.html.
import "../styles/app.css";
import "@fonts";
import "../preview/preview.css";
import { currentTheme } from "./theme.ts";
import { parseSecurityPreview } from "../preview/security-states.ts";

import { parseOnboardingPreview } from "../preview/onboarding-states.ts";

document.documentElement.dataset.theme = currentTheme();
try {
  const onboarding = parseOnboardingPreview(new URLSearchParams(location.search));
  const security = parseSecurityPreview(new URLSearchParams(location.search));
  const params = new URLSearchParams(location.search);
  if (params.has("harness-login")) {
    const { parseHarnessLoginPreview, mountHarnessLoginPreview } = await import("../preview/harness-login-preview.ts");
    const harnessLogin = parseHarnessLoginPreview(params)!;
    mountHarnessLoginPreview(document.querySelector<HTMLDivElement>("#app")!, harnessLogin);
  } else if (onboarding !== null) {
    const { mountOnboardingPreview } = await import("../preview/onboarding-preview.ts");
    mountOnboardingPreview(document.querySelector<HTMLDivElement>("#app")!, onboarding);
  } else if (security !== null) {
    const { mountSecurityPreview } = await import("../preview/security-preview.ts");
    mountSecurityPreview(document.querySelector<HTMLDivElement>("#app")!, security);
  } else {
    await import("./workspace-preview.ts");
  }
} catch (error) {
  document.documentElement.dataset.previewError = error instanceof Error ? error.message : String(error);
  throw error;
}
