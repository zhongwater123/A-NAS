import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { PhotoAsset } from "./photosApi";
import { Slideshow } from "./Slideshow";

const photo = (id: string): PhotoAsset => ({
  id, libraryId: "library:mine", name: `${id}.jpg`, mediaType: "image/jpeg", sizeBytes: 1, uploadedBy: "user:alice",
  importedAt: "2026-10-08T09:00:00Z", takenAt: "2026-05-01T08:00:00Z", width: 4, height: 3, thumbnail: "ready",
});
const shown = () => document.querySelector<HTMLImageElement>(".ph-slide:not(.leaving)")?.alt;
// One photo's turn: its fade, then the time it stays.
const turn = 4900;
const elapse = async (ms: number) => { await act(async () => { await vi.advanceTimersByTimeAsync(ms); }); };

// jsdom loads no images; here every photo decodes at once.
const decode = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, "decode");
beforeEach(() => {
  vi.useFakeTimers();
  Object.defineProperty(HTMLImageElement.prototype, "decode", { configurable: true, value: () => Promise.resolve() });
});
afterEach(() => {
  vi.useRealTimers();
  if (decode) Object.defineProperty(HTMLImageElement.prototype, "decode", decode);
  else delete (HTMLImageElement.prototype as { decode?: unknown }).decode;
});

describe("Slideshow", () => {
  it("keeps its pace whatever the view below does, and ends after the last photo", async () => {
    const photos = [photo("a"), photo("b"), photo("c")];
    const onReveal = vi.fn();
    const { rerender } = render(<Slideshow assets={photos} startId="a" more={false} onReveal={onReveal} onExit={() => undefined} />);
    expect(shown()).toBe("a.jpg");
    expect(screen.getByText(/1 \/ 3/)).toBeTruthy();
    // The view re-renders with new callbacks midway; the clock goes on.
    await elapse(3000);
    rerender(<Slideshow assets={[...photos]} startId="a" more={false} onReveal={vi.fn()} onExit={() => undefined} />);
    await elapse(turn - 3000);
    expect(shown()).toBe("b.jpg");
    await elapse(turn);
    expect(shown()).toBe("c.jpg");
    await elapse(turn);
    expect(screen.getByText("放完了")).toBeTruthy();
    expect(onReveal).toHaveBeenCalledWith("a");

    // From the top again.
    fireEvent.click(screen.getByRole("button", { name: /从头再放/ }));
    await elapse(0);
    expect(shown()).toBe("a.jpg");
    expect(screen.queryByText("放完了")).toBeNull();
  });

  it("pauses, steps by hand and leaves at the photo shown", async () => {
    const onExit = vi.fn();
    render(<Slideshow assets={[photo("a"), photo("b"), photo("c")]} startId="b" total={3} more={false} onReveal={() => undefined} onExit={onExit} />);
    const show = screen.getByRole("dialog", { name: "幻灯片" });
    fireEvent.keyDown(show, { key: " " });
    await elapse(turn * 3);
    expect(shown()).toBe("b.jpg");
    fireEvent.keyDown(show, { key: "ArrowRight" });
    await elapse(0);
    expect(shown()).toBe("c.jpg");
    fireEvent.keyDown(show, { key: "ArrowLeft" });
    await elapse(0);
    fireEvent.keyDown(show, { key: "ArrowLeft" });
    await elapse(0);
    expect(shown()).toBe("a.jpg");
    expect(screen.getByRole("button", { name: "上一张" })).toHaveProperty("disabled", true);
    fireEvent.keyDown(show, { key: "Escape" });
    expect(onExit).toHaveBeenCalledWith("a");
  });

  it("runs on into the photos the view loads after the last one", async () => {
    const onReveal = vi.fn();
    const first = [photo("a"), photo("b")];
    const { rerender } = render(<Slideshow assets={first} startId="b" total={3} more onReveal={onReveal} onExit={() => undefined} />);
    await elapse(turn);
    // At the end of what is loaded it asks the view for more and waits.
    expect(shown()).toBe("b.jpg");
    expect(onReveal).toHaveBeenLastCalledWith("b");
    expect(screen.getByText("正在载入后面的照片")).toBeTruthy();
    rerender(<Slideshow assets={[...first, photo("c")]} startId="b" total={3} more={false} onReveal={onReveal} onExit={() => undefined} />);
    await elapse(0);
    expect(shown()).toBe("c.jpg");
    expect(screen.queryByText("放完了")).toBeNull();
  });
});
