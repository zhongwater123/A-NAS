import { act, fireEvent, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { BootIdentHooks } from "./bootIdent";
import { BootSplash, bootIdentStorageKey, shouldPlayBootIdent } from "./BootSplash";

const ident = vi.hoisted(() => ({
  hooks: undefined as BootIdentHooks | undefined,
  supported: true,
  skip: vi.fn(),
  stop: vi.fn(),
}));

vi.mock("./bootIdent", () => ({
  startBootIdent: (_canvas: HTMLCanvasElement, hooks: BootIdentHooks) => {
    ident.hooks = hooks;
    return ident.supported ? { skip: ident.skip, stop: ident.stop } : null;
  },
}));

beforeEach(() => {
  sessionStorage.clear();
  ident.hooks = undefined;
  ident.supported = true;
  ident.skip.mockClear();
  ident.stop.mockClear();
});

afterEach(() => vi.unstubAllGlobals());

describe("boot splash", () => {
  it("plays once per tab and hands over to the desktop when the ident ends", () => {
    const { container, unmount } = render(<BootSplash />);
    expect(container.querySelector(".boot-splash canvas")).not.toBeNull();
    expect(sessionStorage.getItem(bootIdentStorageKey)).toBe("1");

    act(() => ident.hooks!.onFade(.4));
    expect(container.querySelector<HTMLElement>(".boot-splash")!.style.opacity).toBe("0.4");

    act(() => ident.hooks!.onDone());
    expect(container.querySelector(".boot-splash")).toBeNull();
    expect(ident.stop).toHaveBeenCalled();
    unmount();

    expect(shouldPlayBootIdent()).toBe(false);
    expect(render(<BootSplash />).container.querySelector(".boot-splash")).toBeNull();
  });

  it("skips on a pointer or any key without letting keys reach the page", () => {
    const { container } = render(<BootSplash />);
    const pageKeys = vi.fn();
    document.body.addEventListener("keydown", pageKeys);

    fireEvent.pointerDown(container.querySelector(".boot-splash")!);
    const key = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    document.body.dispatchEvent(key);

    expect(ident.skip).toHaveBeenCalledTimes(2);
    expect(key.defaultPrevented).toBe(true);
    expect(pageKeys).not.toHaveBeenCalled();
    document.body.removeEventListener("keydown", pageKeys);
  });

  it("goes straight to the desktop when the ident cannot start", () => {
    ident.supported = false;
    expect(render(<BootSplash />).container.querySelector(".boot-splash")).toBeNull();
  });

  it("never plays for users who prefer reduced motion", () => {
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("reduce") }));
    expect(shouldPlayBootIdent()).toBe(false);
    expect(render(<BootSplash />).container.querySelector(".boot-splash")).toBeNull();
    expect(sessionStorage.getItem(bootIdentStorageKey)).toBeNull();
  });
});
