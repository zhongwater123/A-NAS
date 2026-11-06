import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse } from "msw";
import { type ReactElement, StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { loadTaskNotices } from "../assistant/taskNotices";
import { http, server } from "../test/server";
import { Companion } from "./Companion";
import { companionReady, companionState, reportCards, reportEvent, resetWorkStatus, startTurn } from "./workStatus";

const origin = window.location.origin;
const IDLE = "待机呼吸休闲";
const POSITION_KEY = "a-nas.companion.position";
const served = (names: string[]) =>
  Object.fromEntries(names.map((name) => [name, { webm: `/companion/thumb/main/${encodeURIComponent(name)}.webm` }]));
const serve = (names: string[]) =>
  server.use(
    http.untyped.get(`${origin}/companion/animations`, () => HttpResponse.json({ animations: served(names) })),
  );
// The page's state is the companion's host, as dsh-pet's is: a test writes to it as VNAS would.
const write = (leaf: "say" | "anim" | "position" | "throw" | "lock" | "sleep", data: unknown) =>
  act(() => companionState.writePet("main", leaf, data));
const settle = () => act(() => new Promise((resolve) => setTimeout(resolve, 50)));

/** The animations the card fetched, by name, in order: what it played or is about to. */
let fetched: string[] = [];

beforeEach(() => {
  fetched = [];
  // jsdom plays no video and makes no object URLs; the animation chain only needs them to exist.
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockReturnValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "load").mockReturnValue(undefined);
  URL.createObjectURL ??= () => "blob:animation";
  URL.revokeObjectURL ??= () => undefined;
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: false, media: query }));
  server.use(
    http.untyped.get(`${origin}/companion/thumb/main/:name`, ({ request }) => {
      fetched.push(decodeURIComponent(new URL(request.url).pathname).replace(/^.*\/|\.(webm|mov)$/g, ""));
      return new HttpResponse(new Blob(["webm"]));
    }),
  );
});
afterEach(() => {
  // The page's state outlives the companion, and so a test: back to awake, unlocked and idle.
  act(() => {
    resetWorkStatus();
    companionState.writePet("main", "sleep", { sleeping: false });
    companionState.writePet("main", "lock", { locked: false });
  });
  localStorage.clear();
  // A test that plays Safari must not leave the next one in it.
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const front = (videos: HTMLVideoElement[]) => videos.find((video) => video.classList.contains("is-front"))!;
/** The animation the chain switched to has decoded: it becomes the one shown. */
const ready = async (videos: HTMLVideoElement[]) => {
  await waitFor(() => expect(videos.some((video) => video.src && !video.classList.contains("is-front"))).toBe(true));
  for (const video of videos) if (!video.classList.contains("is-front")) fireEvent(video, new Event("loadeddata"));
};
const plays = (name: string) => waitFor(() => expect(fetched).toContain(name));
const hide = (hidden: boolean) => {
  vi.spyOn(document, "hidden", "get").mockReturnValue(hidden);
  act(() => document.dispatchEvent(new Event("visibilitychange")));
};
const mount = async (ui: ReactElement = <Companion />) => {
  const view = render(ui);
  // The companion asks for its animations a second after the desktop has drawn.
  const pet = await screen.findByLabelText("大肥鱼", {}, { timeout: 4000 });
  const root = pet.closest(".dsh-pet-root") as HTMLElement;
  const videos = [...root.querySelectorAll("video")];
  await ready(videos);
  return { view, pet, root, videos };
};
const left = (rx: number) => `${rx * window.innerWidth - 120}px`;

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
    await settle();
    expect(screen.queryByLabelText("大肥鱼")).toBeNull();
    expect(companionReady()).toBe(false);
  });

  it("plays 大肥鱼 with dsh-pet's card, credits its makers and follows what VNAS is doing", async () => {
    serve([IDLE, "东张西望", "工作状态-忙碌点按", "工作状态-原地踱步张望", "工作状态-雀跃庆祝"]);
    const { view, pet } = await mount();
    const credit = pet.closest("[title]") as HTMLElement;
    for (const part of ["github.com/PC2005-cloud/dsh-pet", "@上善无形", "@ZipZipPipe", "CC BY-NC-SA 4.0"])
      expect(credit.title).toContain(part);
    // Above the desktop's icons, below its windows.
    expect(credit.style.getPropertyValue("--dsh-pet-z")).toBe("60");
    // Only installed animations play: the first is always the idle one.
    expect(fetched[0]).toBe(IDLE);
    expect(companionReady()).toBe(true);

    // A turn the VNAS window on this page started.
    act(() => {
      startTurn("live0001");
      reportEvent("live0001", "tool.start", { name: "browser_snapshot" });
    });
    await plays("工作状态-忙碌点按");
    act(() => reportCards([{ requestId: "req-1", sessionId: "live0001", status: "awaiting" }]));
    await plays("工作状态-原地踱步张望");
    act(() => reportCards([]));
    act(() => reportEvent("live0001", "message.complete", { status: "complete" }));
    await plays("工作状态-雀跃庆祝");
    // A stage without its animation installed changes nothing: the companion carries on.
    fetched = [];
    act(() => {
      startTurn("live0001");
      reportEvent("live0001", "message.complete", { status: "error" });
    });
    await settle();
    expect(fetched).toEqual([]);

    view.unmount();
    expect(companionReady()).toBe(false);
  });

  it("is not shown in a browser that needs animations this NAS does not have", async () => {
    vi.spyOn(navigator, "userAgent", "get").mockReturnValue(
      "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 Version/26.0 Mobile/15E148 Safari/604.1",
    );
    let asked = false;
    server.use(
      http.untyped.get(`${origin}/companion/animations`, () => {
        asked = true;
        return HttpResponse.json({ animations: served([IDLE]) });
      }),
    );
    render(<Companion />);
    await waitFor(() => expect(asked).toBe(true));
    await settle();
    expect(screen.queryByLabelText("大肥鱼")).toBeNull();
  });

  it("asks for the animations of the pool the service names", async () => {
    server.use(
      http.untyped.get(`${origin}/companion/animations`, () =>
        HttpResponse.json({ pool: "efc3b4c6eebf8963", animations: served([IDLE]) }),
      ),
    );
    const asked: string[] = [];
    server.use(
      http.untyped.get(`${origin}/companion/thumb/main/:name`, ({ request }) => {
        asked.push(new URL(request.url).search);
        return new HttpResponse(new Blob(["webm"]));
      }),
    );
    await mount();
    expect(asked[0]).toBe("?v=efc3b4c6eebf8963");
  });

  it("plays under React's StrictMode and comes back where it was left on this device", async () => {
    localStorage.setItem(POSITION_KEY, JSON.stringify({ rx: 0.5, ry: 0.5 }));
    serve([IDLE]);
    const { root, videos } = await mount(
      <StrictMode>
        <Companion />
      </StrictMode>,
    );
    expect(front(videos).src).toBeTruthy();
    await waitFor(() => expect(root.style.left).toBe(left(0.5)));
  });

  it("waves at a new result of a scheduled task, not at the results it finds", async () => {
    const notice = (id: string) => ({
      id,
      kind: "assistant_task" as const,
      actorUsername: "frank",
      task: { taskId: "0123456789ab", name: "整理照片", status: "ok", ranAt: "2026-11-06T08:00:00Z" },
      createdAt: "2026-11-06T08:00:00Z",
    });
    let items = [notice("n-1")];
    server.use(http.get("/api/v1/notifications", ({ response }) => response(200).json({ items })));
    await act(() => loadTaskNotices());
    serve([IDLE, "点击回应-元气挥手"]);
    await mount();
    await settle();
    expect(fetched).not.toContain("点击回应-元气挥手");
    // The task ran again: its notice is replaced by one with a new id, so there is still one.
    items = [notice("n-2")];
    await act(() => loadTaskNotices());
    await plays("点击回应-元气挥手");
  });
});

describe("what VNAS asks of the companion", () => {
  it("plays a named animation and says what it is told", async () => {
    serve([IDLE, "东张西望", "碎碎念-发呆碎碎念"]);
    await mount();
    write("anim", { name: "东张西望" });
    await plays("东张西望");
    write("say", { text: "照片整理好了" });
    expect(await screen.findByText("照片整理好了")).toBeInTheDocument();
    await plays("碎碎念-发呆碎碎念");
  });

  it("says it without an animation when none is installed to say it with", async () => {
    serve([IDLE]);
    await mount();
    write("say", { text: "照片整理好了" });
    expect(await screen.findByText("照片整理好了")).toBeInTheDocument();
  });

  it("sleeps, and wakes when it is clicked", async () => {
    serve([IDLE, "睡觉-开始", "睡觉-循环", "睡觉-起来"]);
    const { pet, videos } = await mount();
    write("sleep", { sleeping: true, start: "睡觉-开始", loop: "睡觉-循环", end: "睡觉-起来" });
    await plays("睡觉-开始");
    await ready(videos);
    act(() => fireEvent(front(videos), new Event("ended")));
    await plays("睡觉-循环");
    await ready(videos);
    fireEvent.click(pet);
    await plays("睡觉-起来");
    expect((companionState.read().pets.main.sleep.data as { sleeping: boolean }).sleeping).toBe(false);
  });

  it("holds still under a lock, and goes on when the lock ends", async () => {
    serve([IDLE]);
    const { videos } = await mount();
    const pause = vi.mocked(HTMLMediaElement.prototype.pause);
    const play = vi.mocked(HTMLMediaElement.prototype.play);
    pause.mockClear();
    write("lock", { locked: true, expiresAt: Date.now() + 60_000 });
    expect(pause.mock.contexts).toContain(front(videos));
    // The random chain stays where it is.
    fetched = [];
    act(() => fireEvent(front(videos), new Event("ended")));
    await settle();
    expect(fetched).toEqual([]);
    play.mockClear();
    write("lock", { locked: false });
    expect(play.mock.contexts).toContain(front(videos));
    await plays(IDLE);
  });
});

describe("walking, throwing and resting", () => {
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
  });
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
  const saved = () => JSON.parse(localStorage.getItem(POSITION_KEY) ?? "null") as { rx: number; ry: number } | null;

  it("goes where it is put and is there again next time on this device", async () => {
    serve([IDLE]);
    const first = await mount();
    write("position", { x: 0.25, y: 0.5, mode: "instant" });
    await waitFor(() => expect(first.root.style.left).toBe(left(0.25)));
    expect(saved()).toEqual({ rx: 0.25, ry: 0.5 });
    first.view.unmount();
    const second = await mount();
    await waitFor(() => expect(second.root.style.left).toBe(left(0.25)));
  });

  it("walks when it rolls a move and stops where it got to when the page is hidden", async () => {
    // With every VNAS stage installed, so the idle animation is not standing in for one.
    serve([
      IDLE,
      "螃蟹走路",
      "工作状态-思考冒泡",
      "工作状态-忙碌点按",
      "工作状态-清点归档",
      "工作状态-原地踱步张望",
      "工作状态-雀跃庆祝",
      "工作状态-垂头叹气冒汗",
    ]);
    const { root, videos } = await mount();
    // Room to walk to the left.
    write("position", { x: 0.6, y: 0.8, mode: "instant" });
    await waitFor(() => expect(root.style.left).toBe(left(0.6)));
    // Every roll lands on a move (dsh-pet weighs idle 10, turn 5, move 5 of 100).
    vi.spyOn(Math, "random").mockReturnValue(0.17);
    act(() => fireEvent(front(videos), new Event("ended")));
    await plays("螃蟹走路");
    await ready(videos);
    // Halfway through the clip, between its lead-in and its tail.
    Object.defineProperty(front(videos), "currentTime", { configurable: true, value: 5 });
    frames(3);
    expect(root.style.left).not.toBe(left(0.6));
    expect(queue.size).toBe(1);
    hide(true);
    expect(queue.size).toBe(0);
    await waitFor(() => expect(saved()?.rx ?? 0).toBeLessThan(0.6));
    expect((saved()?.rx ?? 0) * window.innerWidth - 120).toBeCloseTo(Number.parseFloat(root.style.left));
  });

  it("flies when VNAS throws it, holds the flight while the page is hidden, and lands within the page", async () => {
    serve([IDLE]);
    const { root } = await mount();
    write("throw", { angle: 60, power: 1 });
    expect(queue.size).toBeGreaterThan(0);
    frames(5);
    hide(true);
    expect(queue.size).toBe(0);
    const held = root.style.top;
    hide(false);
    expect(queue.size).toBeGreaterThan(0);
    frames(2000);
    expect(queue.size).toBe(0);
    expect(root.style.top).not.toBe(held);
    expect(Number.parseFloat(root.style.top)).toBeLessThanOrEqual(window.innerHeight);
    expect(saved()).not.toBeNull();
  });

  it("freezes while the page is hidden, drops what it is asked meanwhile, and carries on when it is back", async () => {
    serve([IDLE, "东张西望"]);
    const { videos } = await mount();
    const pause = vi.mocked(HTMLMediaElement.prototype.pause);
    const play = vi.mocked(HTMLMediaElement.prototype.play);
    pause.mockClear();
    hide(true);
    expect(pause.mock.contexts).toContain(front(videos));
    fetched = [];
    write("anim", { name: "东张西望" });
    play.mockClear();
    hide(false);
    expect(play.mock.contexts).toContain(front(videos));
    await settle();
    expect(fetched).not.toContain("东张西望");
  });

  it("rests after a minute without input", async () => {
    serve([IDLE]);
    const { videos } = await mount();
    const pause = vi.mocked(HTMLMediaElement.prototype.pause);
    vi.useFakeTimers({ shouldClearNativeTimers: true });
    // Input starts the minute again, on the fake clock.
    fireEvent.keyDown(window, { key: "a" });
    pause.mockClear();
    act(() => vi.advanceTimersByTime(59_000));
    expect(pause.mock.contexts).not.toContain(front(videos));
    act(() => vi.advanceTimersByTime(1_000));
    expect(pause.mock.contexts).toContain(front(videos));
    vi.useRealTimers();
  });
});
