import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { installLongPress, LONG_PRESS_MS } from "./longPress";
import { coarsePointer, installTouchInput, lastPointerWasTouch, touchScreen } from "./touch";
import { simulateTouch } from "./touchSimulator";

// A touch event as iOS Safari sends it: jsdom has no Touch constructor.
const touch = (type: string, target: Element, at = { x: 20, y: 30 }, fingers = 1) => {
  const event = new Event(type, { bubbles: true, cancelable: true });
  const points = Array.from({ length: type === "touchend" ? 0 : fingers }, () => ({ clientX: at.x, clientY: at.y }));
  Object.defineProperty(event, "touches", { value: points });
  target.dispatchEvent(event);
  return event;
};
const press = (target: Element, pointerType: string) =>
  target.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, pointerType, button: 0 }));

let item: HTMLButtonElement;
let uninstall = () => {};
beforeEach(() => {
  vi.useFakeTimers();
  item = document.createElement("button");
  document.body.append(item);
});
afterEach(() => {
  uninstall();
  uninstall = () => {};
  item.remove();
  vi.unstubAllGlobals();
});

describe("touch input", () => {
  it("marks a touch screen and remembers whether the latest press was a finger", () => {
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: query === coarsePointer }));
    uninstall = installTouchInput();
    expect(touchScreen()).toBe(true);
    press(item, "touch");
    expect(lastPointerWasTouch()).toBe(true);
    press(item, "mouse");
    expect(lastPointerWasTouch()).toBe(false);
    // A key pressed after a touch is not a tap.
    press(item, "touch");
    item.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter" }));
    expect(lastPointerWasTouch()).toBe(false);
    // Uninstalling forgets the last press too, so the next test starts clean.
    press(item, "touch");
    uninstall();
    expect(touchScreen()).toBe(false);
    expect(lastPointerWasTouch()).toBe(false);
  });

  it("leaves a computer unmarked", () => {
    uninstall = installTouchInput();
    expect(touchScreen()).toBe(false);
  });
});

describe("long press", () => {
  it("opens an item's menu after a long press and swallows the click that follows", () => {
    uninstall = installLongPress();
    const menu = vi.fn((event: MouseEvent) => event.preventDefault());
    const click = vi.fn();
    item.addEventListener("contextmenu", menu);
    item.addEventListener("click", click);

    touch("touchstart", item);
    vi.advanceTimersByTime(LONG_PRESS_MS - 1);
    expect(menu).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(menu).toHaveBeenCalledTimes(1);
    expect(menu.mock.calls[0][0]).toMatchObject({ clientX: 20, clientY: 30 });

    expect(touch("touchend", item).defaultPrevented).toBe(true);
    item.click();
    expect(click).not.toHaveBeenCalled();
    // The next tap is a tap again.
    item.click();
    expect(click).toHaveBeenCalledTimes(1);
  });

  // Each press is left to run its full time after it ends, so only the end of
  // that press, not the next touchstart, can be what cancelled it.
  it("leaves a short tap alone", () => {
    uninstall = installLongPress();
    const menu = vi.fn();
    item.addEventListener("contextmenu", menu);
    touch("touchstart", item);
    vi.advanceTimersByTime(200);
    expect(touch("touchend", item).defaultPrevented).toBe(false);
    vi.advanceTimersByTime(LONG_PRESS_MS);
    expect(menu).not.toHaveBeenCalled();
  });

  it("leaves a scroll alone", () => {
    uninstall = installLongPress();
    const menu = vi.fn();
    item.addEventListener("contextmenu", menu);
    touch("touchstart", item);
    touch("touchmove", item, { x: 20, y: 60 });
    vi.advanceTimersByTime(LONG_PRESS_MS);
    expect(menu).not.toHaveBeenCalled();
  });

  it("leaves a second finger, as for a pinch, alone", () => {
    uninstall = installLongPress();
    const menu = vi.fn();
    item.addEventListener("contextmenu", menu);
    touch("touchstart", item);
    vi.advanceTimersByTime(100);
    touch("touchstart", item, undefined, 2);
    vi.advanceTimersByTime(LONG_PRESS_MS);
    expect(menu).not.toHaveBeenCalled();
  });

  it("lets a page that shows no menu of its own keep the click", () => {
    uninstall = installLongPress();
    const click = vi.fn();
    item.addEventListener("click", click);
    touch("touchstart", item);
    vi.advanceTimersByTime(LONG_PRESS_MS);
    touch("touchend", item);
    item.click();
    expect(click).toHaveBeenCalledTimes(1);
  });
});

describe("on iOS", () => {
  // jsdom's navigator has no maxTouchPoints; both are set on the object and
  // deleted after, which brings back what its prototype says.
  const agent = (userAgent: string, maxTouchPoints: number) => {
    Object.defineProperty(navigator, "userAgent", { value: userAgent, configurable: true });
    Object.defineProperty(navigator, "maxTouchPoints", { value: maxTouchPoints, configurable: true });
  };
  let viewport: HTMLMetaElement;
  beforeEach(() => {
    viewport = document.createElement("meta");
    viewport.name = "viewport";
    viewport.content = "width=device-width, initial-scale=1.0";
    document.head.append(viewport);
  });
  afterEach(() => {
    viewport.remove();
    Reflect.deleteProperty(navigator, "userAgent");
    Reflect.deleteProperty(navigator, "maxTouchPoints");
  });

  const longPressOpensMenu = async () => {
    await vi.dynamicImportSettled();
    const menu = vi.fn((event: MouseEvent) => event.preventDefault());
    item.addEventListener("contextmenu", menu);
    touch("touchstart", item);
    vi.advanceTimersByTime(LONG_PRESS_MS);
    touch("touchend", item);
    return menu.mock.calls.length > 0;
  };

  it.each([
    ["an iPhone", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148", 5],
    ["an iPad, which calls itself a Mac", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15", 5],
  ])("on %s, stops the focus zoom once and loads the long press", async (_, userAgent, points) => {
    agent(userAgent, points);
    installTouchInput()();
    uninstall = installTouchInput();
    expect(viewport.content).toBe("width=device-width, initial-scale=1.0, maximum-scale=1");
    expect(await longPressOpensMenu()).toBe(true);
    // Gone with the rest when uninstalled.
    uninstall();
    uninstall = () => {};
    expect(await longPressOpensMenu()).toBe(false);
  });

  it("leaves a Mac and other computers as they are", async () => {
    agent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15", 0);
    uninstall = installTouchInput();
    expect(viewport.content).toBe("width=device-width, initial-scale=1.0");
    expect(await longPressOpensMenu()).toBe(false);
  });
});

describe("the phone simulator", () => {
  it("lets a mouse stand in for a finger", () => {
    const input = installTouchInput();
    const simulator = simulateTouch();
    uninstall = () => {
      simulator();
      input();
    };
    expect(touchScreen()).toBe(true);
    const menu = vi.fn((event: MouseEvent) => event.preventDefault());
    const click = vi.fn();
    item.addEventListener("contextmenu", menu);
    item.addEventListener("click", click);

    // A click is a tap.
    press(item, "mouse");
    item.dispatchEvent(new PointerEvent("pointerup", { bubbles: true, pointerType: "mouse" }));
    expect(lastPointerWasTouch()).toBe(true);
    vi.advanceTimersByTime(LONG_PRESS_MS);
    expect(menu).not.toHaveBeenCalled();

    // The button held still is a long press; the click as it is let go is swallowed.
    press(item, "mouse");
    vi.advanceTimersByTime(LONG_PRESS_MS);
    expect(menu).toHaveBeenCalledTimes(1);
    item.dispatchEvent(new PointerEvent("pointerup", { bubbles: true, pointerType: "mouse" }));
    item.click();
    expect(click).not.toHaveBeenCalled();

    uninstall();
    uninstall = () => {};
    expect(touchScreen()).toBe(false);
  });
});
