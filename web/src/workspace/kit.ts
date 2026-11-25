import type { useDialogs, useToasts } from "../ui/feedback";

// Kit is what the window's parts use to talk to the member: toasts at the
// bottom of the window and its dialogs.
export interface Kit {
  toast: ReturnType<typeof useToasts>["toast"];
  confirm: ReturnType<typeof useDialogs>["confirm"];
  prompt: ReturnType<typeof useDialogs>["prompt"];
}
