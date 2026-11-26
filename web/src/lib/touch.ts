// Touch input on phones and tablets.
//
// The windows were written for a mouse: a click selects, a double click opens
// and the right button opens a menu. On a touch screen a tap is the only
// gesture people expect to open something, and a long press is how they ask
// for its menu. Android's browsers turn a long press into a contextmenu event
// already; iOS Safari does not, so installTouchInput makes it do the same.

let lastPointer = "";
// Development only (phone.html): a mouse stands in for a finger.
let simulated = false;

// touchScreen says whether the main pointer is a finger, for wording such as
// "点按" in place of "双击" and for gestures that only suit a mouse.
export const touchScreen = () =>
  simulated || (typeof window !== "undefined" && window.matchMedia?.("(pointer: coarse)").matches === true);

// lastPointerWasTouch says whether the latest press came from a finger, so a
// click handler can open on a tap where it would select on a mouse click.
export const lastPointerWasTouch = () => simulated || lastPointer === "touch";

// iOS and iPadOS Safari are the browsers that fire no contextmenu after a
// long press; Android, Windows and ChromeOS fire their own. iPadOS calls
// itself a Mac with a touch screen.
const isIOS = (win: Window) =>
  /iP(hone|ad|od)/.test(win.navigator.userAgent) ||
  (/Macintosh/.test(win.navigator.userAgent) && win.navigator.maxTouchPoints > 1);

// The press must stay put this long, and within this distance, to count.
export const LONG_PRESS_MS = 500;
const SLOP = 10;

// installTouchInput records each press's pointer type and, on iOS, whose
// browser does not do it itself, dispatches contextmenu after a long
// press. The click that follows a long press that opened a menu is swallowed,
// so the menu does not close or the item open as the finger lifts.
//
// simulate (development only) treats every press as a finger's and a mouse
// button held still as a long press, so the phone simulator page can show
// the gestures on a computer.
export function installTouchInput(
  win: Window & typeof globalThis = window,
  options: { longPress?: boolean; simulate?: boolean } = {},
) {
  const doc = win.document;
  simulated = options.simulate === true;
  const listeners: Array<[string, EventListener, AddEventListenerOptions]> = [];
  const listen = <E extends Event>(type: string, listener: (event: E) => void, opts: AddEventListenerOptions) => {
    doc.addEventListener(type, listener as EventListener, opts);
    listeners.push([type, listener as EventListener, opts]);
  };

  // One press at a time: where it began, and whether it opened a menu.
  let timer: ReturnType<typeof setTimeout> | undefined;
  let start = { x: 0, y: 0 };
  let opened = false;
  const cancel = () => {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  };
  const uninstall = () => {
    cancel();
    simulated = false;
    for (const [type, listener, opts] of listeners) doc.removeEventListener(type, listener, opts);
  };
  const press = (target: EventTarget | null, x: number, y: number) => {
    opened = false;
    cancel();
    if (!(target instanceof win.Element)) return;
    start = { x, y };
    timer = setTimeout(() => {
      timer = undefined;
      const menu = new win.MouseEvent("contextmenu", {
        bubbles: true,
        cancelable: true,
        clientX: x,
        clientY: y,
        button: 2,
        buttons: 2,
      });
      // A handler that showed its own menu cancels the event.
      opened = !target.dispatchEvent(menu);
    }, LONG_PRESS_MS);
  };
  const moved = (x: number, y: number) => {
    if (Math.hypot(x - start.x, y - start.y) > SLOP) cancel();
  };

  listen<PointerEvent>(
    "pointerdown",
    (event) => {
      lastPointer = event.pointerType;
      if (simulated && event.pointerType === "mouse" && event.button === 0)
        press(event.target, event.clientX, event.clientY);
    },
    { capture: true },
  );

  // A key press is not a tap: Enter or Space on a focused item selects it.
  listen<KeyboardEvent>(
    "keydown",
    () => {
      lastPointer = "";
    },
    { capture: true },
  );

  const longPress = simulated || (options.longPress ?? isIOS(win));
  if (!longPress) return uninstall;

  if (simulated) {
    listen<PointerEvent>("pointermove", (event) => moved(event.clientX, event.clientY), { capture: true });
    listen<PointerEvent>("pointerup", cancel, { capture: true });
  }
  listen<TouchEvent>(
    "touchstart",
    (event) => {
      if (event.touches.length !== 1) {
        opened = false;
        cancel();
        return;
      }
      press(event.target, event.touches[0].clientX, event.touches[0].clientY);
    },
    { passive: true, capture: true },
  );
  listen<TouchEvent>(
    "touchmove",
    (event) => {
      const touch = event.touches[0];
      if (touch) moved(touch.clientX, touch.clientY);
    },
    { passive: true, capture: true },
  );
  listen<TouchEvent>(
    "touchend",
    (event) => {
      cancel();
      if (opened && event.cancelable) event.preventDefault();
    },
    { capture: true },
  );
  listen<TouchEvent>("touchcancel", cancel, { capture: true });
  listen<MouseEvent>(
    "click",
    (event) => {
      if (!opened) return;
      opened = false;
      event.preventDefault();
      event.stopPropagation();
    },
    { capture: true },
  );
  return uninstall;
}
