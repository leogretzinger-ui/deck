// Inert markup only: it owns no state and wires no events, so Escape
// remains with the key strip. The compose row is a fixed-height label
// wrapper with a leading prompt prefix and a circular send target; the
// textarea fills the wrapper and carries no placeholder so the cursor alone
// marks the insertion point.
import { lucideIcon } from "../ui/icons.ts";
import * as hints from "../lib/hints.ts";

export function mobileComposeMarkup(): string {
  return `<form data-mobile-compose-form>
  <label data-mobile-compose-input-wrap>
    <span data-mobile-compose-prompt aria-hidden="true">›</span>
    <textarea data-mobile-compose-input rows="1"
      aria-label="Terminal input"
      placeholder=""
      class="w-full bg-transparent font-mono resize-none outline-none"></textarea>
  </label>
  <button type="submit" data-mobile-compose-send aria-label="Send" data-hint="${hints.SEND_RETURN_KEY}"
    class="grid h-11 w-11 flex-none place-items-center rounded-full bg-accent text-text-on-accent">
    ${lucideIcon("arrow-up", 18)}
  </button>
</form>`;
}
