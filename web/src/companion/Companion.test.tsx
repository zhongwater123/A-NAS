import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { reportAwaiting, reportTurn, resetAssistantActivity } from "../assistant/assistantActivity";
import { http, server } from "../test/server";
import { Companion } from "./Companion";

const origin = window.location.origin;
const served = (names: string[]) =>
  Object.fromEntries(names.map((name) => [name, { webm: `/companion/animations/${encodeURIComponent(name)}.webm` }]));

beforeEach(() => {
  // jsdom plays no video and makes no object URLs; the animation chain only needs them to exist.
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockReturnValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "load").mockReturnValue(undefined);
  URL.createObjectURL ??= () => "blob:animation";
  URL.revokeObjectURL ??= () => undefined;
  server.use(http.untyped.get(`${origin}/companion/animations/:name`, () => new HttpResponse(new Blob(["webm"]))));
});
afterEach(() => {
  resetAssistantActivity();
  localStorage.clear();
  // A test that plays Safari must not leave the next one in it.
  vi.restoreAllMocks();
});

describe("the companion on the desktop", () => {
  it("shows nothing while no animations are installed", async () => {
    let asked = false;
    server.use(
      http.untyped.get(`${origin}/companion/animations`, () => {
        asked = true;
        return HttpResponse.json({ animations: {} });
      }),
    );
    render(<Companion />);
    await waitFor(() => expect(asked).toBe(true));
    await act(() => Promise.resolve());
    expect(screen.queryByRole("button", { name: "大肥鱼" })).toBeNull();
  });

  it("plays 大肥鱼 with dsh-pet's animations, credits them and follows what VNAS is doing", async () => {
    server.use(
      http.untyped.get(`${origin}/companion/animations`, () =>
        HttpResponse.json({
          animations: served([
            "待机呼吸休闲",
            "东张西望",
            "工作状态-忙碌点按",
            "工作状态-原地踱步张望",
            "工作状态-雀跃庆祝",
          ]),
        }),
      ),
    );
    render(<Companion />);
    const pet = await screen.findByRole("button", { name: "大肥鱼" });
    expect(pet.title).toContain("github.com/PC2005-cloud/dsh-pet");
    const root = pet.closest("[data-anim]") as HTMLElement;
    // Only installed animations play: the first is always the idle one.
    expect(root.dataset.anim).toBe("待机呼吸休闲");

    act(() => reportTurn("live0001", "working"));
    await waitFor(() => expect(root.dataset.anim).toBe("工作状态-忙碌点按"));
    expect(root.dataset.work).toBe("working");
    act(() => reportAwaiting(1));
    await waitFor(() => expect(root.dataset.anim).toBe("工作状态-原地踱步张望"));
    act(() => reportAwaiting(0));
    act(() => reportTurn("live0001", "success"));
    await waitFor(() => expect(root.dataset.anim).toBe("工作状态-雀跃庆祝"));
    // A stage without its animation installed stays with an idle one.
    act(() => reportTurn("live0001", "error"));
    await waitFor(() => expect(root.dataset.work).toBe("error"));
    expect(root.dataset.anim).toBe("待机呼吸休闲");
  });

  it("is not shown in a browser that needs animations this NAS does not have", async () => {
    vi.spyOn(navigator, "userAgent", "get").mockReturnValue(
      "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 Version/26.0 Mobile/15E148 Safari/604.1",
    );
    let asked = false;
    server.use(
      http.untyped.get(`${origin}/companion/animations`, () => {
        asked = true;
        return HttpResponse.json({ animations: served(["待机呼吸休闲"]) });
      }),
    );
    render(<Companion />);
    await waitFor(() => expect(asked).toBe(true));
    await act(() => new Promise((resolve) => setTimeout(resolve, 50)));
    expect(screen.queryByRole("button", { name: "大肥鱼" })).toBeNull();
  });
});

describe("walking and throwing the companion", () => {
  // Frames run when the test says so, at a clock it moves, so a walk or a flight
  // can be stepped and a loop left running can be seen.
  const queue = new Map<number, FrameRequestCallback>();
  let now = 1000;
  let handle = 0;
  beforeEach(() => {
    queue.clear();
    now = 1000;
    vi.spyOn(performance, "now").mockImplementation(() => now);
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      queue.set(++handle, callback);
      return handle;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => queue.delete(id));
    // jsdom lays nothing out: the companion's box is where its style puts it.
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      const x = Number.parseFloat(this.style.left) || 0;
      const y = Number.parseFloat(this.style.top) || 0;
      return DOMRect.fromRect({ x, y, width: 240, height: 135 });
    });
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: false, media: query }));
    HTMLElement.prototype.setPointerCapture ??= () => undefined;
    HTMLElement.prototype.hasPointerCapture ??= () => false;
    HTMLElement.prototype.releasePointerCapture ??= () => undefined;
  });
  afterEach(() => vi.unstubAllGlobals());
  const frames = (count: number) => {
    for (let i = 0; i < count && queue.size > 0; i++) {
      now += 16;
      const due = [...queue.values()];
      queue.clear();
      act(() => {
        for (const callback of due) callback(now);
      });
    }
  };
  const serve = (names: string[]) =>
    server.use(
      http.untyped.get(`${origin}/companion/animations`, () => HttpResponse.json({ animations: served(names) })),
    );
  // The animation a switch asked for has decoded: it becomes the one shown.
  const ready = async (videos: HTMLVideoElement[]) => {
    await waitFor(() => expect(videos.some((video) => video.src && !video.classList.contains("is-front"))).toBe(true));
    for (const video of videos) if (!video.classList.contains("is-front")) fireEvent(video, new Event("loadeddata"));
  };
  const front = (videos: HTMLVideoElement[]) => videos.find((video) => video.classList.contains("is-front"))!;
  const hide = (hidden: boolean) => {
    vi.spyOn(document, "hidden", "get").mockReturnValue(hidden);
    act(() => document.dispatchEvent(new Event("visibilitychange")));
  };
  const saved = () => JSON.parse(localStorage.getItem("a-nas.companion.position") ?? "null") as { rx: number };
  const mount = async () => {
    render(<Companion />);
    // The companion asks for its animations a second after the desktop has drawn.
    const pet = await screen.findByRole("button", { name: "大肥鱼" }, { timeout: 4000 });
    const root = pet.closest("[data-anim]") as HTMLElement;
    const videos = [...root.querySelectorAll("video")];
    await ready(videos);
    return { pet, root, videos };
  };

  it("walks when it rolls a move and stops where it got to when it rests", async () => {
    // With every VNAS stage installed, so the idle animation is not standing in for one.
    serve([
      "待机呼吸休闲",
      "螃蟹走路",
      "工作状态-思考冒泡",
      "工作状态-忙碌点按",
      "工作状态-清点归档",
      "工作状态-原地踱步张望",
      "工作状态-雀跃庆祝",
      "工作状态-垂头叹气冒汗",
    ]);
    const { root, videos } = await mount();
    // Every roll lands on a move (dsh-pet weighs idle 10, turn 5, move 5 of 100).
    vi.spyOn(Math, "random").mockReturnValue(0.17);
    act(() => fireEvent(front(videos), new Event("ended")));
    await waitFor(() => expect(root.dataset.anim).toBe("螃蟹走路"));
    await ready(videos);
    // Halfway through the clip, between its lead-in and its tail.
    Object.defineProperty(front(videos), "currentTime", { configurable: true, value: 5 });
    const from = root.style.left;
    frames(3);
    expect(root.style.left).not.toBe(from);
    expect(queue.size).toBe(1);
    hide(true);
    expect(queue.size).toBe(0);
    expect(saved().rx * window.innerWidth - 120).toBeCloseTo(Number.parseFloat(root.style.left));
  });

  it("flies when thrown, holds the flight while it rests, and lands within the page", async () => {
    serve(["待机呼吸休闲"]);
    const { pet, root } = await mount();
    const at = (x: number, y: number, t: number) => {
      now = t;
      return { clientX: x, clientY: y, pointerId: 1, button: 0, buttons: 1, pointerType: "mouse" };
    };
    fireEvent.pointerDown(pet, at(880, 620, 2000));
    for (let i = 1; i <= 5; i++) fireEvent.pointerMove(pet, at(880 - i * 30, 620 - i * 40, 2000 + i * 16));
    fireEvent.pointerUp(pet, at(730, 420, 2096));
    frames(5);
    expect(queue.size).toBeGreaterThan(0);
    hide(true);
    expect(queue.size).toBe(0);
    const held = root.style.top;
    hide(false);
    expect(queue.size).toBeGreaterThan(0);
    frames(2000);
    expect(queue.size).toBe(0);
    expect(root.style.top).not.toBe(held);
    const top = Number.parseFloat(root.style.top);
    expect(top).toBeLessThanOrEqual(window.innerHeight);
    expect(saved().rx).toBeGreaterThan(0);
  });
});
