// The film page's shortcuts (页面级): bound while the page is there, and
// heard only while 剪辑中心 is the desktop's focused window, so a key meant
// for another window never edits the film. They listen on the document,
// not the page: after a click, focus may have left the page for the body,
// as when the button clicked was greyed while its edit was made. A key
// typed into a text field is the field's.
import { type RefObject, useEffect, useEffectEvent } from "react";

import { focusedWindow } from "../../desktop/focusedWindow";
import { WINDOW_ID } from "../../desktop/windowSnapshot";

const typing = (target: EventTarget | null) =>
  target instanceof HTMLInputElement ||
  target instanceof HTMLTextAreaElement ||
  (target instanceof HTMLElement && target.isContentEditable);

export function useShortcuts(page: RefObject<HTMLElement | null>, keys: { undo: () => void }) {
  const onKeyDown = useEffectEvent((event: KeyboardEvent) => {
    if (!((event.ctrlKey || event.metaKey) && !event.shiftKey && event.key.toLowerCase() === "z")) return;
    const frame = page.current?.closest(`[${WINDOW_ID}]`);
    // Inside a desktop window, only the focused one; a page on its own, as in a test, always.
    if (frame && focusedWindow()?.id !== frame.getAttribute(WINDOW_ID)) return;
    if (typing(event.target)) return;
    event.preventDefault();
    keys.undo();
  });
  useEffect(() => {
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);
}
