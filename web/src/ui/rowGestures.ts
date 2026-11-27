import { lastPointerWasTouch, touchScreen } from "../lib/touch";

interface Click {
  shiftKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  currentTarget: EventTarget | null;
}

// A double tap is two taps and a dblclick. The first tap opens; what follows
// on the same row this soon belongs to the same gesture and opens nothing
// more, or a file that cannot be previewed would download two or three times.
const DOUBLE_TAP_MS = 500;
const tappedAt = new WeakMap<EventTarget, number>();

// rowGestures is how a list row answers a mouse and a finger alike: a click
// selects and a double click opens, but a tap opens, as people expect on a
// touch screen. A long press arrives as contextmenu, the row's own menu. A row
// that can be dragged with a mouse is not draggable on a touch screen, where
// dragging would take the long press and the scroll.
export function rowGestures<C extends Click>({
  open,
  select,
  draggable = false,
}: {
  open: () => void;
  select: (click: C) => void;
  draggable?: boolean;
}) {
  return {
    draggable: draggable && !touchScreen(),
    onClick: (click: C) => {
      if (!lastPointerWasTouch() || click.shiftKey || click.ctrlKey || click.metaKey) {
        select(click);
        return;
      }
      const row = click.currentTarget;
      const now = performance.now();
      const again = row !== null && now - (tappedAt.get(row) ?? Number.NEGATIVE_INFINITY) < DOUBLE_TAP_MS;
      if (row) tappedAt.set(row, now);
      if (!again) open();
    },
    onDoubleClick: () => {
      if (!lastPointerWasTouch()) open();
    },
  };
}
