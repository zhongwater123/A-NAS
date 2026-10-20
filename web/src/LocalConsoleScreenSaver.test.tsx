import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  LocalConsoleScreenSaver,
  defaultSleepAfterMinutes,
  isLocalConsole,
  loadScreenSaverVideos,
  localConsoleScreenSaverIdleMs,
  selectScreenSaverVideo,
} from "./LocalConsoleScreenSaver";

const videos = [
  "/local-console/screensavers/alpha.mp4",
  "/local-console/screensavers/beta.mp4",
  "/local-console/screensavers/gamma.mp4",
];
const minute = 60_000;
const neverSleep = async () => 0;
const saver = () => screen.queryByRole("dialog", { name: "屏幕保护程序" });
const asleep = () => screen.queryByRole("dialog", { name: "息屏" });

// idle lets the screen saver start and the sleep setting it then reads arrive.
async function idle(ms = localConsoleScreenSaverIdleMs) {
  await act(async () => { vi.advanceTimersByTime(ms); });
}

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

  it("selects one video uniformly from the pool", () => {
    expect(selectScreenSaverVideo(videos, () => 0)).toBe(videos[0]);
    expect(selectScreenSaverVideo(videos, () => 0.5)).toBe(videos[1]);
    expect(selectScreenSaverVideo(videos, () => 0.99)).toBe(videos[2]);
    expect(selectScreenSaverVideo([], () => 0)).toBeUndefined();
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

  it("loops one random video per idle activation and samples again after activity", async () => {
    const random = vi.fn().mockReturnValueOnce(0).mockReturnValueOnce(0.99);
    render(<LocalConsoleScreenSaver enabled loadVideos={async () => videos} loadSleepAfter={neverSleep} random={random} />);
    await act(async () => undefined);

    act(() => vi.advanceTimersByTime(localConsoleScreenSaverIdleMs - 1));
    expect(screen.queryByRole("dialog", { name: "屏幕保护程序" })).toBeNull();
    act(() => vi.advanceTimersByTime(1));

    const saver = screen.getByRole("dialog", { name: "屏幕保护程序" });
    const video = saver.querySelector("video")!;
    expect(video.getAttribute("src")).toBe(videos[0]);
    expect(video.autoplay).toBe(true);
    expect(video.loop).toBe(true);
    expect(video.muted).toBe(true);

    fireEvent.ended(video);
    expect(video.getAttribute("src")).toBe(videos[0]);

    expect(fireEvent.keyDown(window, { key: "Enter" })).toBe(false);
    expect(screen.queryByRole("dialog", { name: "屏幕保护程序" })).toBeNull();
    act(() => vi.advanceTimersByTime(localConsoleScreenSaverIdleMs));
    expect(screen.getByRole("dialog", { name: "屏幕保护程序" }).querySelector("video")?.getAttribute("src")).toBe(videos[2]);
    expect(random).toHaveBeenCalledTimes(2);
  });

  it("resets on activity and replaces failed videos until the pool is exhausted", async () => {
    render(<LocalConsoleScreenSaver enabled loadVideos={async () => videos.slice(0, 2)} loadSleepAfter={neverSleep} random={() => 0} />);
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

  it("does not load the pool or go black in a remote browser", async () => {
    const loadVideos = vi.fn().mockResolvedValue(videos);
    const loadSleepAfter = vi.fn().mockResolvedValue(10);
    render(<LocalConsoleScreenSaver enabled={false} loadVideos={loadVideos} loadSleepAfter={loadSleepAfter} />);
    await act(async () => undefined);
    await idle(localConsoleScreenSaverIdleMs + 60 * minute);
    expect(loadVideos).not.toHaveBeenCalled();
    expect(loadSleepAfter).not.toHaveBeenCalled();
    expect(saver()).toBeNull();
    expect(asleep()).toBeNull();
  });

  it("turns the screen black once the screen saver has played the set minutes", async () => {
    render(<LocalConsoleScreenSaver enabled loadVideos={async () => videos} loadSleepAfter={async () => 10} random={() => 0} />);
    await act(async () => undefined);
    await idle();
    expect(saver()?.querySelector("video")?.getAttribute("src")).toBe(videos[0]);

    act(() => vi.advanceTimersByTime(10 * minute - 1));
    expect(asleep()).toBeNull();
    act(() => vi.advanceTimersByTime(1));
    expect(asleep()).toBeTruthy();
    // Nothing is decoded while the screen is black.
    expect(saver()).toBeNull();
    expect(document.querySelector("video")).toBeNull();

    // The first input only wakes the page, back to the desktop rather than the video.
    expect(fireEvent.pointerDown(window)).toBe(false);
    expect(asleep()).toBeNull();
    expect(saver()).toBeNull();
    await idle(localConsoleScreenSaverIdleMs - 1);
    expect(saver()).toBeNull();
    await idle(1);
    expect(saver()).toBeTruthy();
  });

  it("goes black without a screen saver when the pool is empty", async () => {
    render(<LocalConsoleScreenSaver enabled loadVideos={async () => []} loadSleepAfter={async () => 30} />);
    await act(async () => undefined);
    await idle();
    expect(saver()).toBeNull();
    act(() => vi.advanceTimersByTime(30 * minute));
    expect(asleep()).toBeTruthy();
  });

  it("turns the sign-in page black after the screen saver delay plus the set minutes", async () => {
    const loadVideos = vi.fn().mockResolvedValue(videos);
    render(<LocalConsoleScreenSaver enabled videos={false} loadVideos={loadVideos} loadSleepAfter={async () => 30} />);
    await act(async () => undefined);
    await idle();
    act(() => vi.advanceTimersByTime(30 * minute - 1));
    expect(asleep()).toBeNull();
    act(() => vi.advanceTimersByTime(1));
    expect(asleep()).toBeTruthy();
    expect(loadVideos).not.toHaveBeenCalled();
    // A key that wakes the page is not typed into the form underneath.
    expect(fireEvent.keyDown(window, { key: "a" })).toBe(false);
    expect(asleep()).toBeNull();
  });

  it("keeps the screen saver playing when set to never", async () => {
    render(<LocalConsoleScreenSaver enabled loadVideos={async () => videos} loadSleepAfter={neverSleep} random={() => 0} />);
    await act(async () => undefined);
    await idle();
    act(() => vi.advanceTimersByTime(12 * 60 * minute));
    expect(saver()).toBeTruthy();
    expect(asleep()).toBeNull();
  });

  it("falls back to thirty minutes when the setting cannot be read", async () => {
    render(<LocalConsoleScreenSaver enabled loadVideos={async () => videos} loadSleepAfter={() => Promise.reject(new Error("offline"))} random={() => 0} />);
    await act(async () => undefined);
    await idle();
    act(() => vi.advanceTimersByTime(defaultSleepAfterMinutes * minute - 1));
    expect(asleep()).toBeNull();
    act(() => vi.advanceTimersByTime(1));
    expect(asleep()).toBeTruthy();
  });

  it("ignores a setting that arrives after the idle period ended", async () => {
    let answer: ((minutes: number) => void) | undefined;
    const loadSleepAfter = () => new Promise<number>((resolve) => { answer = resolve; });
    render(<LocalConsoleScreenSaver enabled loadVideos={async () => videos} loadSleepAfter={loadSleepAfter} random={() => 0} />);
    await act(async () => undefined);
    await idle();
    fireEvent.pointerMove(window);
    await act(async () => answer?.(1));
    act(() => vi.advanceTimersByTime(2 * minute));
    expect(asleep()).toBeNull();
    expect(saver()).toBeNull();
  });
});
