import { fireEvent, render, screen } from "@testing-library/react";
import { Suspense } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { lazyCode } from "../ui/lazyCode";
import { AppWindow } from "./AppWindow";
import { apps } from "./apps";
import { closedWindow, type WindowModel } from "./windows";

const trash: WindowModel = { ...closedWindow("trash", apps.trash.bounds), open: true, z: 1 };
const setPointerCapture = Object.getOwnPropertyDescriptor(Element.prototype, "setPointerCapture");

describe("AppWindow", () => {
  beforeEach(() => {
    // A failing window logs what failed; jsdom captures no pointer.
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    Object.defineProperty(Element.prototype, "setPointerCapture", { configurable: true, value: vi.fn() });
  });
  afterEach(() => {
    vi.restoreAllMocks();
    if (setPointerCapture) Object.defineProperty(Element.prototype, "setPointerCapture", setPointerCapture);
    else delete (Element.prototype as Partial<Element>).setPointerCapture;
  });

  it("asks for a reload when the window's code did not download", async () => {
    const Missing = lazyCode<object>(() =>
      Promise.reject(new TypeError("Failed to fetch dynamically imported module")),
    );
    render(
      <AppWindow model={trash} dispatch={vi.fn()}>
        <Suspense fallback={null}>
          <Missing />
        </Suspense>
      </AppWindow>,
    );
    expect(await screen.findByRole("heading", { name: "这个窗口没有加载出来" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "重新载入页面" })).toBeTruthy();
  });

  it("offers to reopen a window whose content failed", () => {
    function Broken(): never {
      throw new Error("broken");
    }
    render(
      <AppWindow model={trash} dispatch={vi.fn()}>
        <Broken />
      </AppWindow>,
    );
    expect(screen.getByRole("heading", { name: "这个窗口暂时无法显示" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "重新打开" })).toBeTruthy();
  });

  it("follows the pointer and tells the desktop where the window ended once released", () => {
    const dispatch = vi.fn();
    render(
      <AppWindow model={trash} dispatch={dispatch}>
        <p>内容</p>
      </AppWindow>,
    );
    const frame = screen.getByRole("dialog", { name: "回收站" });
    const titleBar = screen.getByText("回收站").parentElement!;

    fireEvent.pointerDown(titleBar, { button: 0, pointerId: 1, clientX: 400, clientY: 100 });
    expect(titleBar.setPointerCapture).toHaveBeenCalledWith(1);
    fireEvent.pointerMove(titleBar, { pointerId: 1, clientX: 440, clientY: 130 });
    expect(frame.style.left).toBe(`${trash.x + 40}px`);
    expect(frame.style.top).toBe(`${trash.y + 30}px`);
    expect(dispatch).not.toHaveBeenCalledWith(expect.objectContaining({ type: "move" }));

    fireEvent.pointerUp(titleBar, { pointerId: 1, clientX: 440, clientY: 130 });
    fireEvent(titleBar, new Event("lostpointercapture"));
    expect(dispatch).toHaveBeenCalledWith({ type: "move", id: "trash", x: trash.x + 40, y: trash.y + 30 });
  });

  it("puts the window back when the browser cancels the press", () => {
    const dispatch = vi.fn();
    render(
      <AppWindow model={trash} dispatch={dispatch}>
        <p>内容</p>
      </AppWindow>,
    );
    const frame = screen.getByRole("dialog", { name: "回收站" });
    const titleBar = screen.getByText("回收站").parentElement!;

    fireEvent.pointerDown(titleBar, { button: 0, pointerId: 1, clientX: 400, clientY: 100 });
    fireEvent.pointerMove(titleBar, { pointerId: 1, clientX: 440, clientY: 130 });
    fireEvent.pointerCancel(titleBar, { pointerId: 1 });
    fireEvent(titleBar, new Event("lostpointercapture"));

    expect(frame.style.left).toBe(`${trash.x}px`);
    expect(dispatch).not.toHaveBeenCalledWith(expect.objectContaining({ type: "move" }));
    // Later moves no longer reach the window.
    fireEvent.pointerMove(titleBar, { pointerId: 1, clientX: 500, clientY: 200 });
    expect(frame.style.left).toBe(`${trash.x}px`);
  });
});
