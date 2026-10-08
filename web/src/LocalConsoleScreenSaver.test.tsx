import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  LocalConsoleScreenSaver,
  isLocalConsole,
  loadScreenSaverVideos,
  localConsoleScreenSaverIdleMs,
  shuffleScreenSaverVideos,
} from "./LocalConsoleScreenSaver";

const videos = [
  "/local-console/screensavers/alpha.mp4",
  "/local-console/screensavers/beta.mp4",
  "/local-console/screensavers/gamma.mp4",
];

describe("local console screen saver", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("is enabled only by the local console URL marker", () => {
    expect(isLocalConsole("?local-console=1")).toBe(true);
    expect(isLocalConsole("?local-console=0")).toBe(false);
    expect(isLocalConsole("")).toBe(false);
  });

  it("shuffles without repeating the last video at a pool boundary", () => {
    expect(shuffleScreenSaverVideos(videos, () => 0.99, videos[0])).toEqual([videos[1], videos[0], videos[2]]);
  });

  it("accepts only unique anonymous same-origin video URLs from the manifest", async () => {
    const valid = `/local-console/screensavers/${"a".repeat(64)}.mp4`;
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ videos: [valid, valid, "https://example.com/video.mp4", "/api/v1/users"] }),
    }));
    await expect(loadScreenSaverVideos()).resolves.toEqual([valid]);
    expect(fetch).toHaveBeenCalledWith("/local-console/screensavers", { cache: "no-store" });
  });

  it("starts after three idle minutes, plays every video without repeats, and consumes the first activity", async () => {
    render(<LocalConsoleScreenSaver enabled loadVideos={async () => videos} random={() => 0.99} />);
    await act(async () => undefined);

    act(() => vi.advanceTimersByTime(localConsoleScreenSaverIdleMs - 1));
    expect(screen.queryByRole("dialog", { name: "屏幕保护程序" })).toBeNull();
    act(() => vi.advanceTimersByTime(1));

    const saver = screen.getByRole("dialog", { name: "屏幕保护程序" });
    const video = saver.querySelector("video")!;
    expect(video.getAttribute("src")).toBe(videos[0]);
    expect(video.autoplay).toBe(true);
    expect(video.loop).toBe(false);
    expect(video.muted).toBe(true);

    fireEvent.ended(video);
    expect(video.getAttribute("src")).toBe(videos[1]);
    fireEvent.ended(video);
    expect(video.getAttribute("src")).toBe(videos[2]);
    fireEvent.ended(video);
    expect(video.getAttribute("src")).toBe(videos[0]);

    expect(fireEvent.keyDown(window, { key: "Enter" })).toBe(false);
    expect(screen.queryByRole("dialog", { name: "屏幕保护程序" })).toBeNull();
  });

  it("resets on activity and skips failed videos until the pool is exhausted", async () => {
    render(<LocalConsoleScreenSaver enabled loadVideos={async () => videos.slice(0, 2)} random={() => 0.99} />);
    await act(async () => undefined);

    act(() => vi.advanceTimersByTime(120_000));
    fireEvent.pointerMove(window);
    act(() => vi.advanceTimersByTime(localConsoleScreenSaverIdleMs - 1));
    expect(screen.queryByRole("dialog", { name: "屏幕保护程序" })).toBeNull();
    act(() => vi.advanceTimersByTime(1));

    const video = screen.getByRole("dialog", { name: "屏幕保护程序" }).querySelector("video")!;
    expect(video.getAttribute("src")).toBe(videos[0]);
    fireEvent.error(video);
    expect(video.getAttribute("src")).toBe(videos[1]);
    expect(video.loop).toBe(true);
    fireEvent.error(video);
    expect(screen.queryByRole("dialog", { name: "屏幕保护程序" })).toBeNull();
    act(() => vi.advanceTimersByTime(localConsoleScreenSaverIdleMs));
    expect(screen.queryByRole("dialog", { name: "屏幕保护程序" })).toBeNull();
  });

  it("does not load the pool in a remote browser", async () => {
    const loadVideos = vi.fn().mockResolvedValue(videos);
    render(<LocalConsoleScreenSaver enabled={false} loadVideos={loadVideos} />);
    await act(async () => undefined);
    act(() => vi.advanceTimersByTime(localConsoleScreenSaverIdleMs));
    expect(loadVideos).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog", { name: "屏幕保护程序" })).toBeNull();
  });
});
