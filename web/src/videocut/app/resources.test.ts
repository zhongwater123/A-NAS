import { renderHook } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { pool, useObjectURL } from "./resources";

afterEach(() => vi.restoreAllMocks());

describe("pool", () => {
  it("never runs more than its limit, even as a new task comes when one ends", async () => {
    const run = pool(2);
    let running = 0;
    let most = 0;
    const task = async () => {
      running += 1;
      most = Math.max(most, running);
      await new Promise((resolve) => setTimeout(resolve, 5));
      running -= 1;
    };
    const first = [run(task), run(task), run(task)];
    await first[0];
    // A task asked for just as one ended must wait for the one waiting before it.
    await Promise.all([...first, run(task), run(task)]);
    expect(most).toBe(2);
  });
});

describe("useObjectURL", () => {
  it("keeps the URL alive through StrictMode's remount, and revokes it once left", async () => {
    const created = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:one");
    const revoked = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
    const blob = new Blob(["x"]);
    const { result, unmount } = renderHook(() => useObjectURL(blob), { wrapper: StrictMode });
    await new Promise((resolve) => setTimeout(resolve));
    expect(result.current).toBe("blob:one");
    expect(revoked).not.toHaveBeenCalled();
    unmount();
    await new Promise((resolve) => setTimeout(resolve));
    expect(revoked).toHaveBeenCalledExactlyOnceWith("blob:one");
    expect(created).toHaveBeenCalledOnce();
  });
});
