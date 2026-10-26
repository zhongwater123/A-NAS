import { act, renderHook, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as api from "../api";
import { useFileListing } from "./useFileListing";

const entry = (id: string): api.FileEntry => ({
  id,
  spaceId: "space:a",
  name: id,
  kind: "file",
  sizeBytes: 1,
  modifiedAt: "2026-10-26T00:00:00Z",
});

function pendingList() {
  let resolve!: (items: api.FileEntry[]) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<api.FileEntry[]>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  vi.mocked(api.listEntries).mockReturnValueOnce(promise);
  return { resolve, reject };
}

beforeEach(() => {
  vi.spyOn(api, "listSpaces").mockResolvedValue([
    { id: "space:a", kind: "shared", name: "A", createdAt: "2026-10-26T00:00:00Z" },
    { id: "space:b", kind: "shared", name: "B", createdAt: "2026-10-26T00:00:00Z" },
  ]);
  vi.spyOn(api, "listVolumes").mockResolvedValue([]);
  vi.spyOn(api, "listEntries").mockResolvedValue([]);
});

afterEach(() => vi.restoreAllMocks());

describe("file listing request lifetime", () => {
  it.each(["folder", "space"])("ignores a late success after changing %s", async (destination) => {
    const old = pendingList();
    const current = pendingList();
    const { result } = renderHook(() => useFileListing());
    await waitFor(() => expect(api.listEntries).toHaveBeenCalledTimes(1));
    act(() => {
      if (destination === "folder") result.current.setTrail([{ id: "folder:b", name: "B" }]);
      else result.current.setSpaceID("space:b");
    });
    await act(async () => current.resolve([entry("current")]));
    await act(async () => old.resolve([entry("obsolete")]));
    expect(result.current.entries.map((item) => item.id)).toEqual(["current"]);
    expect(result.current.loading).toBe(false);
    expect(result.current.error).toBe("");
  });

  it.each(["success", "failure"])("keeps the current request loading when an old %s arrives", async (outcome) => {
    const old = pendingList();
    const current = pendingList();
    const { result } = renderHook(() => useFileListing());
    await waitFor(() => expect(api.listEntries).toHaveBeenCalledTimes(1));
    act(() => result.current.setTrail([{ id: "folder:b", name: "B" }]));
    await act(async () => {
      if (outcome === "success") old.resolve([entry("obsolete")]);
      else old.reject(new Error("obsolete failure"));
    });
    expect(result.current.entries).toEqual([]);
    expect(result.current.error).toBe("");
    expect(result.current.loading).toBe(true);
    await act(async () => current.reject(new Error("current failure")));
    expect(result.current.error).toBe("current failure");
    expect(result.current.loading).toBe(false);
  });

  it("clears the previous folder while loading and when no space remains", async () => {
    vi.mocked(api.listEntries).mockResolvedValueOnce([entry("old-folder-file")]);
    const { result } = renderHook(() => useFileListing());
    await waitFor(() => expect(result.current.entries).toHaveLength(1));
    const pending = pendingList();
    act(() => result.current.setTrail([{ id: "folder:b", name: "B" }]));
    expect(result.current.entries).toEqual([]);
    expect(result.current.loading).toBe(true);
    act(() => result.current.setSpaceID(""));
    await act(async () => pending.resolve([entry("obsolete")]));
    expect(result.current.entries).toEqual([]);
    expect(result.current.loading).toBe(false);
  });

  it("ignores an earlier visit after navigating A to B to A", async () => {
    const firstA = pendingList();
    const b = pendingList();
    const secondA = pendingList();
    const { result } = renderHook(() => useFileListing());
    await waitFor(() => expect(api.listEntries).toHaveBeenCalledTimes(1));
    act(() => result.current.setSpaceID("space:b"));
    act(() => result.current.setSpaceID("space:a"));
    await act(async () => secondA.resolve([entry("new-a")]));
    await act(async () => b.reject(new Error("old-b")));
    await act(async () => firstA.resolve([entry("old-a")]));
    expect(result.current.entries.map((item) => item.id)).toEqual(["new-a"]);
    expect(result.current.error).toBe("");
  });

  it("refreshes the open folder even through a callback retained by an earlier operation", async () => {
    const { result } = renderHook(() => useFileListing());
    await waitFor(() => expect(api.listEntries).toHaveBeenCalledTimes(1));
    const afterOperation = result.current.reload;
    act(() => result.current.setTrail([{ id: "folder:b", name: "B" }]));
    await waitFor(() => expect(api.listEntries).toHaveBeenCalledTimes(2));
    act(() => {
      void afterOperation();
    });
    await waitFor(() => expect(api.listEntries).toHaveBeenCalledTimes(3));
    expect(api.listEntries).toHaveBeenLastCalledWith("space:a", "folder:b");
  });

  it("keeps the newest refresh of the same folder", async () => {
    const old = pendingList();
    const current = pendingList();
    const { result } = renderHook(() => useFileListing(), { wrapper: StrictMode });
    await waitFor(() => expect(api.listEntries).toHaveBeenCalledTimes(1));
    act(() => {
      void result.current.reload();
    });
    await act(async () => current.resolve([entry("refreshed")]));
    await act(async () => old.resolve([entry("obsolete")]));
    expect(result.current.entries.map((item) => item.id)).toEqual(["refreshed"]);
  });
});
