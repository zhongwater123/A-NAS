import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fileOrderKey, FileOrder, OrderableFile, reorderFiles, sortFiles, useFileOrder } from "./useFileOrder";

const base: FileOrder = { sortBy: "name", direction: "ascending", pinned: [], manual: [] };
const items: OrderableFile[] = [
  { id: "a", name: "A", kind: "directory", modifiedAt: "2026-10-01", type: "文件夹", sizeBytes: 0 },
  { id: "b", name: "B", kind: "directory", modifiedAt: "2026-10-02", type: "文件夹", sizeBytes: 0 },
  { id: "c", name: "C.txt", kind: "file", modifiedAt: "2026-10-03", type: "文档", sizeBytes: 12 },
];
const ids = (order: FileOrder, files = items) => sortFiles(files, order).map((item) => item.id);
beforeEach(() => { localStorage.clear(); vi.restoreAllMocks(); });

describe("file organization", () => {
  it("keeps pinned folders first for every automatic sort and direction", () => {
    for (const sortBy of ["name", "modifiedAt", "type", "sizeBytes"] as const) {
      for (const direction of ["ascending", "descending"] as const) {
        expect(ids({ ...base, sortBy, direction, pinned: ["b", "c", "missing"] })).toEqual(["b", "a", "c"]);
      }
    }
  });

  it("allows files ahead of folders in manual mode, retaining the pinned boundary", () => {
    const manual: FileOrder = { ...base, sortBy: "manual", manual: ["a", "b", "c"] };
    expect(ids(reorderFiles(items, manual, "c", "a"))).toEqual(["c", "a", "b"]);
    const pinned = { ...manual, pinned: ["b"] };
    expect(reorderFiles(items, pinned, "a", "b")).toBe(pinned);
    expect(ids(reorderFiles(items, pinned, "c", "a"))).toEqual(["b", "c", "a"]);
    expect(reorderFiles(items, manual, "missing", "a")).toBe(manual);
  });

  it("keeps order across renames, appends new entries, ignores stale IDs, and restores it after automatic sorting", () => {
    const manual: FileOrder = { ...base, sortBy: "manual", manual: ["missing", "b", "a"] };
    expect(ids(manual, items.map((item) => item.id === "b" ? { ...item, name: "Z" } : item))).toEqual(["b", "a", "c"]);
    expect(ids({ ...manual, sortBy: "name" })).toEqual(["a", "b", "c"]);
    expect(ids(manual)).toEqual(["b", "a", "c"]);
    expect(ids(manual, items.filter((item) => item.id !== "b"))).toEqual(["a", "c"]);
  });

  it("persists per user, space and parent without leaking when scope changes", () => {
    const { result, rerender, unmount } = renderHook(({ user, space, parent }) => useFileOrder(user, space, parent), { initialProps: { user: "u1", space: "s1", parent: "" } });
    const saved: FileOrder = { ...base, sortBy: "manual", pinned: ["b"], manual: ["b", "c", "a"] };
    act(() => result.current.save(saved));
    for (const scope of [{ user: "u2", space: "s1", parent: "" }, { user: "u1", space: "s2", parent: "" }, { user: "u1", space: "s1", parent: "a" }]) {
      rerender(scope); expect(result.current.order).toEqual(base);
    }
    rerender({ user: "u1", space: "s1", parent: "" });
    expect(result.current.order).toEqual(saved);
    unmount();
    const reopened = renderHook(() => useFileOrder("u1", "s1", ""));
    expect(reopened.result.current.order).toEqual(saved);
  });

  it("survives corrupt or blocked storage and keeps session changes after a failed save", () => {
    localStorage.setItem(fileOrderKey("u", "s", ""), '{"sortBy":"manual","direction":"ascending","pinned":{},"manual":[]}');
    const { result } = renderHook(() => useFileOrder("u", "s", ""));
    expect(result.current.order).toEqual(base);
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("quota"); });
    act(() => result.current.save({ ...base, pinned: ["b"] }));
    expect(result.current.order.pinned).toEqual(["b"]);
    expect(result.current.storageError).toContain("无法保存");
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("blocked"); });
    expect(renderHook(() => useFileOrder("other", "s", "")).result.current.order).toEqual(base);
  });
});
