// Touch input on phones and tablets.
//
// The windows were written for a mouse: a click selects, a double click opens
// and the right button opens a menu. On a touch screen a tap is the only
// gesture people expect to open something, and a long press is how they ask
// for its menu.
//
// installTouchInput marks a touch screen with data-touch on <html>, which the
// styles and touchScreen() both read, and records whether each press came from
// a finger. What only iOS needs (a long press that fires contextmenu) and what
// only the phone simulator needs (touchSimulator.ts) load on their own, so the
// first page carries neither.

// The query for a touch screen, for tests that stand in for one.
export const coarsePointer = "(pointer: coarse)";

let lastPointer = "";

// touchScreen says whether the main pointer is a finger, for wording such as
// "点按" in place of "双击" and for gestures that only suit a mouse.
export const touchScreen = () => typeof document !== "undefined" && document.documentElement.hasAttribute("data-touch");

// lastPointerWasTouch says whether the latest press came from a finger, so a
// click handler can open on a tap where it would select on a mouse click.
export const lastPointerWasTouch = () => lastPointer === "touch";

// recordPointer stands in for the latest press's pointer type; the simulator
// makes every mouse press a finger's with it.
export const recordPointer = (type: string) => {
  lastPointer = type;
};

// iOS and iPadOS Safari fire no contextmenu after a long press and zoom into
// small text fields; iPadOS calls itself a Mac with a touch screen.
const isIOS = () =>
  /iP(hone|ad|od)/.test(navigator.userAgent) || (/Macintosh/.test(navigator.userAgent) && navigator.maxTouchPoints > 1);

export function installTouchInput() {
  const stop = new AbortController();
  const { signal } = stop;
  const root = document.documentElement;
  const screens = window.matchMedia?.(coarsePointer);
  const mark = () => root.toggleAttribute("data-touch", screens?.matches === true);
  mark();
  screens?.addEventListener?.("change", mark, { signal });
  signal.addEventListener("abort", () => root.removeAttribute("data-touch"));

  document.addEventListener("pointerdown", (event) => recordPointer(event.pointerType), { capture: true, signal });
  // A key press is not a tap: Enter or Space on a focused item selects it.
  document.addEventListener("keydown", () => recordPointer(""), { capture: true, signal });

  if (isIOS()) {
    // Safari zooms the whole page into a field under 16px as it takes the
    // focus, and the desktop does not scroll, so the page stays cropped. A
    // maximum scale stops that zoom; people can still pinch to zoom on iOS.
    const viewport = document.querySelector<HTMLMetaElement>('meta[name="viewport"]');
    if (viewport && !viewport.content.includes("maximum-scale")) viewport.content += ", maximum-scale=1";
    void import("./longPress").then(({ installLongPress }) => {
      if (!signal.aborted) signal.addEventListener("abort", installLongPress());
    });
  }
  return () => stop.abort();
}
