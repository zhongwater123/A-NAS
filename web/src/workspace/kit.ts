import type { RefObject } from "react";

import type { useDialogs, useToasts } from "../ui/feedback";

// Kit is what the window's parts use to talk to the member: toasts at the
// bottom of the window, its dialogs, and the window itself, where dialogs open.
export interface Kit {
  toast: ReturnType<typeof useToasts>["toast"];
  confirm: ReturnType<typeof useDialogs>["confirm"];
  prompt: ReturnType<typeof useDialogs>["prompt"];
  container: RefObject<HTMLElement | null>;
}
