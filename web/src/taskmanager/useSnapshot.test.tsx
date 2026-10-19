import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { useSnapshot } from "./useSnapshot";

afterEach(() => { vi.useRealTimers(); Object.defineProperty(document, "hidden", { configurable: true, value: false }); });
const hide = (hidden: boolean) => { Object.defineProperty(document, "hidden", { configurable: true, value: hidden }); document.dispatchEvent(new Event("visibilitychange")); };
it("polls without overlap, aborts on hide/unmount, resumes immediately and retains only 60 distinct samples", async () => {
  vi.useFakeTimers();
  let finish: (value: { observedAt: string }) => void = () => {};
  const read = vi.fn((signal: AbortSignal) => new Promise<{ observedAt: string }>((resolve, reject) => { finish = resolve; signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError"))); }));
  const hook = renderHook(() => useSnapshot(read));
  await act(async () => { await vi.advanceTimersByTimeAsync(6000); });
  expect(read).toHaveBeenCalledTimes(1);
  await act(async () => hide(true));
  expect(read.mock.calls[0][0].aborted).toBe(true);
  await act(async () => { await vi.advanceTimersByTimeAsync(6000); });
  expect(read).toHaveBeenCalledTimes(1);
  await act(async () => hide(false));
  expect(read).toHaveBeenCalledTimes(2);
  await act(async () => finish({ observedAt: "first" }));
  expect(hook.result.current.data?.observedAt).toBe("first");
  for (let index = 0; index < 65; index++) {
    await act(async () => { await vi.advanceTimersByTimeAsync(2000); finish({ observedAt: String(index) }); });
  }
  expect(hook.result.current.history).toHaveLength(60);
  await act(async () => { await vi.advanceTimersByTimeAsync(2000); finish({ observedAt: "64" }); });
  expect(hook.result.current.history).toHaveLength(60);
  expect(hook.result.current.history[0].observedAt).toBe("5");
  await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
  hook.unmount(); expect(read.mock.lastCall?.[0].aborted).toBe(true);
});
it("preserves the last data during failure and recovers without adding synthetic points", async () => {
  vi.useFakeTimers();
  const read = vi.fn().mockResolvedValueOnce({ observedAt: "first" }).mockRejectedValueOnce(new Error("offline")).mockResolvedValue({ observedAt: "next" });
  const hook = renderHook(() => useSnapshot(read));
  await act(async () => {});
  await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
  expect(hook.result.current.data?.observedAt).toBe("first"); expect(hook.result.current.error).toBeTruthy(); expect(hook.result.current.history).toHaveLength(1);
  await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
  expect(hook.result.current.data?.observedAt).toBe("next"); expect(hook.result.current.error).toBe(""); expect(hook.result.current.history).toHaveLength(2);
});

it("does not request while initially hidden and ignores a late response after cancellation", async () => {
  let resolve: (value: { observedAt: string }) => void = () => {};
  hide(true);
  const read = vi.fn(() => new Promise<{ observedAt: string }>(done => { resolve = done; }));
  const hook = renderHook(() => useSnapshot(read));
  expect(read).not.toHaveBeenCalled();
  await act(async () => hide(false));
  expect(read).toHaveBeenCalledTimes(1);
  await act(async () => hide(true));
  await act(async () => resolve({ observedAt: "cancelled" }));
  expect(hook.result.current.data).toBeUndefined();
  await act(async () => hide(false));
  await act(async () => resolve({ observedAt: "current" }));
  expect(hook.result.current.data?.observedAt).toBe("current");
});
it("retains only the requested history limit for large task snapshots", async () => {
  vi.useFakeTimers(); let observation = 0;
  const read = async () => ({ observedAt: String(observation++) });
  const hook = renderHook(() => useSnapshot(read, 1));
  await act(async () => {});
  await act(async () => { await vi.advanceTimersByTimeAsync(6000); });
  expect(hook.result.current.history).toHaveLength(1);
  expect(hook.result.current.history[0].observedAt).toBe("3");
});