// copyText puts text on the clipboard. The Clipboard API needs a secure
// context and the desktop is plain HTTP on the LAN, so this copies a
// selected, unseen text field instead, then gives focus back.
export function copyText(text: string): boolean {
  const focused = document.activeElement as HTMLElement | null;
  const field = document.createElement("textarea");
  field.value = text;
  field.readOnly = true;
  field.style.position = "fixed";
  field.style.opacity = "0";
  document.body.append(field);
  field.select();
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    field.remove();
    focused?.focus();
  }
}
