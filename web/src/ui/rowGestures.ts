import { lastPointerWasTouch, touchScreen } from "../lib/touch";

interface Modifiers {
  shiftKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
}

// rowGestures is how a list row answers a mouse and a finger alike: a click
// selects and a double click opens, but a tap opens, as people expect on a
// touch screen. A long press arrives as contextmenu, the row's own menu. A row
// that can be dragged with a mouse is not draggable on a touch screen, where
// dragging would take the long press and the scroll.
export function rowGestures<Event extends Modifiers>({
  open,
  select,
  draggable = false,
}: {
  open: () => void;
  select: (event: Event) => void;
  draggable?: boolean;
}) {
  return {
    draggable: draggable && !touchScreen(),
    onClick: (event: Event) => {
      if (lastPointerWasTouch() && !event.shiftKey && !event.ctrlKey && !event.metaKey) open();
      else select(event);
    },
    onDoubleClick: open,
  };
}
