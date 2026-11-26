// A long press that fires contextmenu, for iOS Safari, which has none of its
// own (Android, Windows and ChromeOS browsers do), and for the phone
// simulator, where a mouse button held still stands in for one.
//
// The click that follows a long press that opened a menu is swallowed, so the
// menu does not close or the item open as the finger lifts.

// The press must stay put this long, and within this distance, to count.
export const LONG_PRESS_MS = 500;
const SLOP = 10;

export function installLongPress({ mouse = false }: { mouse?: boolean } = {}) {
  const stop = new AbortController();
  const listen = <E extends Event>(type: string, listener: (event: E) => void, passive = false) =>
    document.addEventListener(type, listener as EventListener, { capture: true, passive, signal: stop.signal });

  // One press at a time: where it began, and whether it opened a menu.
  let timer: ReturnType<typeof setTimeout> | undefined;
  let start = { x: 0, y: 0 };
  let opened = false;
  const cancel = () => {
    clearTimeout(timer);
    timer = undefined;
  };
  stop.signal.addEventListener("abort", cancel);
  const press = (target: EventTarget | null, x: number, y: number) => {
    opened = false;
    cancel();
    if (!(target instanceof Element)) return;
    start = { x, y };
    timer = setTimeout(() => {
      timer = undefined;
      const menu = new MouseEvent("contextmenu", {
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

  listen<TouchEvent>(
    "touchstart",
    (event) => {
      if (event.touches.length === 1) press(event.target, event.touches[0].clientX, event.touches[0].clientY);
      else {
        opened = false;
        cancel();
      }
    },
    true,
  );
  listen<TouchEvent>(
    "touchmove",
    (event) => {
      const touch = event.touches[0];
      if (touch) moved(touch.clientX, touch.clientY);
    },
    true,
  );
  listen<TouchEvent>("touchend", (event) => {
    cancel();
    if (opened && event.cancelable) event.preventDefault();
  });
  listen("touchcancel", cancel);
  if (mouse) {
    listen<PointerEvent>("pointerdown", (event) => {
      if (event.pointerType === "mouse" && event.button === 0) press(event.target, event.clientX, event.clientY);
    });
    listen<PointerEvent>("pointermove", (event) => moved(event.clientX, event.clientY));
    listen("pointerup", cancel);
  }
  listen<MouseEvent>("click", (event) => {
    if (!opened) return;
    opened = false;
    event.preventDefault();
    event.stopPropagation();
  });
  return () => stop.abort();
}
