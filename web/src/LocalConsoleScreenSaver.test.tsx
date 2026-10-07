import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  LocalConsoleScreenSaver,
  isLocalConsole,
  localConsoleScreenSaverIdleMs,
  localConsoleScreenSaverVideo,
} from "./LocalConsoleScreenSaver";

describe("local console screen saver", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("is enabled only by the local console URL marker", () => {
    expect(isLocalConsole("?local-console=1")).toBe(true);
    expect(isLocalConsole("?local-console=0")).toBe(false);
    expect(isLocalConsole("")).toBe(false);
  });

  it("starts after three idle minutes and consumes the first activity", () => {
    render(<LocalConsoleScreenSaver enabled />);

    act(() => vi.advanceTimersByTime(localConsoleScreenSaverIdleMs - 1));
    expect(screen.queryByRole("dialog", { name: "屏幕保护程序" })).toBeNull();
    act(() => vi.advanceTimersByTime(1));

    const saver = screen.getByRole("dialog", { name: "屏幕保护程序" });
    const video = saver.querySelector("video")!;
    expect(video.getAttribute("src")).toBe(localConsoleScreenSaverVideo);
    expect(video.autoplay).toBe(true);
    expect(video.loop).toBe(true);
    expect(video.muted).toBe(true);

    expect(fireEvent.keyDown(window, { key: "Enter" })).toBe(false);
    expect(screen.queryByRole("dialog", { name: "屏幕保护程序" })).toBeNull();
  });

  it("resets on activity and keeps the wallpaper when video playback fails", () => {
    render(<LocalConsoleScreenSaver enabled />);

    act(() => vi.advanceTimersByTime(120_000));
    fireEvent.pointerMove(window);
    act(() => vi.advanceTimersByTime(localConsoleScreenSaverIdleMs - 1));
    expect(screen.queryByRole("dialog", { name: "屏幕保护程序" })).toBeNull();
    act(() => vi.advanceTimersByTime(1));

    const video = screen.getByRole("dialog", { name: "屏幕保护程序" }).querySelector("video")!;
    fireEvent.error(video);
    expect(screen.queryByRole("dialog", { name: "屏幕保护程序" })).toBeNull();
    act(() => vi.advanceTimersByTime(localConsoleScreenSaverIdleMs));
    expect(screen.queryByRole("dialog", { name: "屏幕保护程序" })).toBeNull();
  });
});
