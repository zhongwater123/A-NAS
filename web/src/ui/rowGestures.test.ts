import { afterEach, describe, expect, it, vi } from "vitest";

import { recordPointer } from "../lib/touch";
import { rowGestures } from "./rowGestures";

const plain = { shiftKey: false, ctrlKey: false, metaKey: false };

describe("row gestures", () => {
  afterEach(() => {
    recordPointer("");
    document.documentElement.removeAttribute("data-touch");
  });

  it("selects on a click and opens on a double click with a mouse", () => {
    const open = vi.fn();
    const select = vi.fn();
    const row = rowGestures({ open, select, draggable: true });
    recordPointer("mouse");
    row.onClick(plain);
    expect(select).toHaveBeenCalledWith(plain);
    expect(open).not.toHaveBeenCalled();
    row.onDoubleClick();
    expect(open).toHaveBeenCalledTimes(1);
    expect(row.draggable).toBe(true);
  });

  it("opens on a tap, selects a range with Shift and does not drag on a touch screen", () => {
    document.documentElement.setAttribute("data-touch", "");
    const open = vi.fn();
    const select = vi.fn();
    const row = rowGestures({ open, select, draggable: true });
    recordPointer("touch");
    row.onClick(plain);
    expect(open).toHaveBeenCalledTimes(1);
    row.onClick({ ...plain, shiftKey: true });
    expect(select).toHaveBeenCalledTimes(1);
    expect(row.draggable).toBe(false);
  });
});
