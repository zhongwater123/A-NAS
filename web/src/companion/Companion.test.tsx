import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse } from "msw";
import { type ReactElement, StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { loadTaskNotices } from "../assistant/taskNotices";
import { publishFocusedWindow } from "../desktop/focusedWindow";
import { http, server } from "../test/server";
import { Companion } from "./Companion";
import { resetCompanionCommands, runCompanionCommand, stopTurnCommands } from "./commands";
import { companionLock, companionPlayback, companionSleep, resetCompanionHost } from "./host";
import {
  companionReady,
  companionState,
  reportCards,
  reportEvent,
  resetWorkStatus,
  startTurn,
  stopTurn,
} from "./workStatus";

// Whether the local console's screen saver or black screen covers the page; the rest of the module as it is.
// A test that takes the cover away tells who listens, as the screen saver does.
const cover = vi.hoisted(() => ({ up: false, local: false, dismissed: new Set<() => void>() }));
vi.mock("../desktop/LocalConsoleScreenSaver", async (original) => ({
  ...(await original<typeof import("../desktop/LocalConsoleScreenSaver")>()),
  localConsoleCovered: () => cover.up,
  isLocalConsole: () => cover.local,
  onLocalConsoleDismissed: (listener: () => void) => {
    cover.dismissed.add(listener);
    return () => cover.dismissed.delete(listener);
  },
}));

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
/** Animations whose next fetches fail, by name: how many more, and whether the network or the NAS fails them. */
const failing = new Map<string, { times: number; how: "network" | "http" }>();

beforeEach(() => {
  fetched = [];
  failing.clear();
  // jsdom plays no video and makes no object URLs; the animation chain only needs them to exist.
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockReturnValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "load").mockReturnValue(undefined);
  URL.createObjectURL ??= () => "blob:animation";
  URL.revokeObjectURL ??= () => undefined;
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: false, media: query }));
  server.use(
    http.untyped.get(`${origin}/companion/thumb/main/:name`, ({ request }) => {
      const name = decodeURIComponent(new URL(request.url).pathname).replace(/^.*\/|\.(webm|mov)$/g, "");
      fetched.push(name);
      const failure = failing.get(name);
      if (failure && failure.times > 0) {
        failure.times -= 1;
        return failure.how === "http" ? new HttpResponse(null, { status: 503 }) : HttpResponse.error();
      }
      return new HttpResponse(new Blob(["webm"]));
    }),
  );
});
afterEach(() => {
  cover.up = false;
  cover.local = false;
  // The page's state outlives the companion, and so a test: back to awake, unlocked and idle.
  act(() => {
    resetWorkStatus();
    resetCompanionHost();
    resetCompanionCommands();
    companionState.writePet("main", "sleep", { sleeping: false });
    companionState.writePet("main", "lock", { locked: false });
  });
  publishFocusedWindow(null);
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
  it("plays every clip half again as fast as it was recorded", async () => {
    serve([IDLE]);
    const { videos } = await mount();
    for (const video of videos) {
      fireEvent(video, new Event("loadstart"));
      expect(video.defaultPlaybackRate).toBe(1.5);
      video.playbackRate = 1;
      fireEvent(video, new Event("play"));
      expect(video.playbackRate).toBe(1.5);
    }
  });

  it("shows nothing while no animations are installed", async () => {
    let asked = false;
    server.use(
      http.untyped.get(`${origin}/companion/animations`, () => {
        asked = true;
        return HttpResponse.json({ animations: {} });
      }),
    );
    render(<Companion />);
    await waitFor(() => expect(asked).toBe(true), { timeout: 2000 });
    // An empty list is an answer: it is not asked for again.
    asked = false;
    await act(() => new Promise((resolve) => setTimeout(resolve, 2500)));
    expect(asked).toBe(false);
    expect(screen.queryByLabelText("大肥鱼")).toBeNull();
    expect(companionReady()).toBe(false);
  });

  it("asks for the animations again, later each time, while their list cannot be read", async () => {
    const asked: number[] = [];
    server.use(
      http.untyped.get(`${origin}/companion/animations`, () => {
        asked.push(Date.now());
        if (asked.length === 1) return HttpResponse.error();
        if (asked.length === 2) return new HttpResponse("busy", { status: 503 });
        return HttpResponse.json({ animations: served([IDLE]) });
      }),
    );
    render(<Companion />);
    expect(await screen.findByLabelText("大肥鱼", {}, { timeout: 10_000 })).toBeInTheDocument();
    expect(asked).toHaveLength(3);
    expect(asked[1] - asked[0]).toBeGreaterThanOrEqual(2000);
    expect(asked[2] - asked[1]).toBeGreaterThanOrEqual(4000);
  });

  it("plays 大肥鱼 with dsh-pet's card, without a hover credit, and follows what VNAS is doing", async () => {
    serve([IDLE, "东张西望", "工作状态-忙碌点按", "工作状态-原地踱步张望", "工作状态-雀跃庆祝"]);
    const { view, pet } = await mount();
    // The credit is in Settings → 概览: hovering the pet shows only dsh-pet's tooltip with its name.
    expect(pet.closest("[title]")).toBeNull();
    // Out of the desktop VNAS reads (AssistantPanel's exclude): VNAS has its own tools for the companion.
    const layer = pet.closest("[data-vnas-overlay]") as HTMLElement;
    expect(layer).not.toBeNull();
    // Above the desktop's windows and status bar, below its rail, notices and menus.
    expect(layer.style.getPropertyValue("--dsh-pet-z")).toBe("76");
    // Only installed animations play: the first is always the idle one.
    expect(fetched[0]).toBe(IDLE);
    expect(companionReady()).toBe(true);

    // A turn the VNAS window on this page started: looking at the desktop plays its own clip.
    act(() => {
      startTurn("live0001");
      reportEvent("live0001", "tool.start", { name: "browser_snapshot" });
    });
    await plays("东张西望");
    // A step whose clip is not installed plays the stage's.
    act(() => reportEvent("live0001", "tool.start", { name: "some_new_tool" }));
    await plays("工作状态-忙碌点按");
    act(() => reportCards([{ requestId: "req-1", sessionId: "live0001", status: "awaiting" }]));
    await plays("工作状态-原地踱步张望");
    act(() => reportCards([]));
    // Without its fireworks installed, the success plays the stage's own clip.
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

  it("plays a step's clip for as long as the step lasts, with a line about it", async () => {
    serve([IDLE, "写代码", "放烟花"]);
    const { videos } = await mount();
    act(() => {
      startTurn("live0001");
      reportEvent("live0001", "tool.start", { name: "terminal", tool_id: "c1", args: { command: "du -sh 照片" } });
    });
    await plays("写代码");
    expect(await screen.findByText(/跑一下 du -sh 照片|噼里啪啦敲命令中/)).toBeInTheDocument();
    await ready(videos);
    // The clip ends while the command still runs: it plays again rather than a random one.
    fetched = [];
    act(() => fireEvent(front(videos), new Event("ended")));
    await settle();
    expect(fetched.filter((name) => name !== "写代码")).toEqual([]);
    // The turn ends: fireworks once, then idle.
    act(() => reportEvent("live0001", "message.complete", { status: "complete" }));
    await plays("放烟花");
    await ready(videos);
    act(() => fireEvent(front(videos), new Event("ended")));
    await plays(IDLE);
  });

  it("shows beside the pet that VNAS is at work, and a check for a while after the turn", async () => {
    serve([IDLE, "睡觉-开始", "睡觉-循环", "睡觉-起来"]);
    const { root } = await mount();
    const layer = root.closest("[data-vnas-overlay]") as HTMLElement;
    const sign = () => layer.dataset.companionWork;
    expect(sign()).toBeUndefined();
    act(() => {
      startTurn("live0001");
      reportEvent("live0001", "message.start");
    });
    await waitFor(() => expect(sign()).toBe("thinking"));
    act(() => reportEvent("live0001", "tool.start", { name: "terminal", tool_id: "c1" }));
    await waitFor(() => expect(sign()).toBe("working"));
    // Not while the pet sleeps or is locked: the card does not follow the stage then either.
    act(() => void companionSleep.enter("main", { start: "睡觉-开始", loop: "睡觉-循环", end: "睡觉-起来" }));
    await waitFor(() => expect(sign()).toBeUndefined());
    act(() => void companionSleep.wake("main"));
    await waitFor(() => expect(sign()).toBe("working"));
    write("lock", { locked: true, expiresAt: Date.now() + 60_000 });
    expect(sign()).toBeUndefined();
    write("lock", { locked: false });
    expect(sign()).toBe("working");
    // Resting, the ellipsis holds still.
    hide(true);
    expect(layer.dataset.companionRest).toBe("");
    hide(false);
    expect(layer.dataset.companionRest).toBeUndefined();
    vi.useFakeTimers({ shouldClearNativeTimers: true });
    act(() => reportEvent("live0001", "message.complete", { status: "complete" }));
    act(() => vi.advanceTimersByTime(0));
    expect(sign()).toBe("success");
    act(() => vi.advanceTimersByTime(9_999));
    expect(sign()).toBe("success");
    act(() => vi.advanceTimersByTime(1));
    expect(sign()).toBeUndefined();
    vi.useRealTimers();
  });

  it("curtsies once when the member stops a turn here, then idles", async () => {
    serve([IDLE, "写代码", "女仆屈膝礼仪"]);
    const { root, videos } = await mount();
    act(() => {
      startTurn("live0001");
      reportEvent("live0001", "tool.start", { name: "terminal", tool_id: "c1" });
    });
    await plays("写代码");
    act(() => stopTurn("live0001"));
    await plays("女仆屈膝礼仪");
    expect(await screen.findByText(/好的，停下啦|听你的，不做了~/)).toBeInTheDocument();
    // No stage, so no sign of one.
    expect(root.closest("[data-companion-work]")).toBeNull();
    await ready(videos);
    fetched = [];
    act(() => fireEvent(front(videos), new Event("ended")));
    await plays(IDLE);
  });

  it("stops a step's clip when the turn ends, though the step came while the pet was paused", async () => {
    serve([IDLE, "写代码", "照镜子"]);
    const { videos } = await mount();
    act(() => {
      startTurn("live0001");
      reportEvent("live0001", "tool.start", { name: "terminal", tool_id: "c1" });
    });
    await plays("写代码");
    await ready(videos);
    // Paused, the pet does not follow the next step; back, it plays that step's clip on and on.
    hide(true);
    act(() => reportEvent("live0001", "tool.start", { name: "search_files", tool_id: "c2" }));
    hide(false);
    await plays("照镜子");
    await ready(videos);
    expect(front(videos).loop).toBe(true);
    // The turn ends without a success or an error: the clip plays to its end, then idle.
    act(() => reportEvent("live0001", "message.complete", { status: "interrupted" }));
    await waitFor(() => expect(front(videos).loop).toBe(false));
    fetched = [];
    act(() => fireEvent(front(videos), new Event("ended")));
    await plays(IDLE);
  });

  it("shows the curtsy though the step's clip ends while the curtsy loads", async () => {
    serve([IDLE, "写代码", "女仆屈膝礼仪"]);
    const { videos } = await mount();
    act(() => {
      startTurn("live0001");
      reportEvent("live0001", "tool.start", { name: "terminal", tool_id: "c1" });
    });
    await plays("写代码");
    await ready(videos);
    const step = front(videos);
    act(() => stopTurn("live0001"));
    await plays("女仆屈膝礼仪");
    // The step's clip, still in front, ends before the curtsy has loaded: nothing else is asked for.
    fetched = [];
    act(() => fireEvent(step, new Event("ended")));
    await settle();
    expect(fetched).toEqual([]);
    await ready(videos);
    expect(front(videos)).not.toBe(step);
    act(() => fireEvent(front(videos), new Event("ended")));
    await plays(IDLE);
  });

  it("curtsies when the member stops a turn in which VNAS's animation still plays", async () => {
    serve([IDLE, "写代码", "女仆屈膝礼仪", "点击回应-元气挥手"]);
    const { videos } = await mount();
    act(() => {
      startTurn("live0001");
      reportEvent("live0001", "tool.start", { name: "terminal", tool_id: "c1" });
    });
    await plays("写代码");
    await ready(videos);
    void runCompanionCommand("live0001", { action: "anim", args: { name: "点击回应-元气挥手" } });
    await plays("点击回应-元气挥手");
    await ready(videos);
    fetched = [];
    // As the reply's panel stops a turn (AssistantPanel.tsx): the command's animation first, then the turn,
    // whose curtsy the command's idle must not replace.
    act(() => {
      stopTurnCommands("live0001");
      stopTurn("live0001");
    });
    await plays("女仆屈膝礼仪");
    await ready(videos);
    await settle();
    expect(fetched.at(-1)).toBe("女仆屈膝礼仪");
    fetched = [];
    act(() => fireEvent(front(videos), new Event("ended")));
    await plays(IDLE);
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
    serve([IDLE, "点击回应-元气挥手"]);
    await mount();
    // The notices are listed for the first time after the companion came: what it finds there is old.
    await act(() => loadTaskNotices());
    await settle();
    expect(fetched).not.toContain("点击回应-元气挥手");
    // The task ran again: its notice is replaced by one with a new id, so there is still one.
    items = [notice("n-2")];
    await act(() => loadTaskNotices());
    await plays("点击回应-元气挥手");
  });

  describe("when the member comes back", () => {
    const notice = (id: string) => ({
      id,
      kind: "assistant_task" as const,
      actorUsername: "frank",
      task: { taskId: "0123456789ab", name: "整理照片", status: "ok", ranAt: "2026-11-06T08:00:00Z" },
      createdAt: "2026-11-06T08:00:00Z",
    });
    let items: ReturnType<typeof notice>[] = [];
    const WAVE = "点击回应-元气挥手";
    const SIGH = "工作状态-垂头叹气冒汗";
    /** The companion on the page, with the notices it finds there listed once. */
    const arrive = async (names: string[]) => {
      items = [notice("n-1")];
      server.use(http.get("/api/v1/notifications", ({ response }) => response(200).json({ items })));
      serve(names);
      const mounted = await mount();
      await act(() => loadTaskNotices());
      await settle();
      // The store of notices outlives a test: what the last one left may have looked new here.
      fetched = [];
      const layer = mounted.root.closest("[data-vnas-overlay]") as HTMLElement;
      return { ...mounted, sign: () => layer.dataset.companionWork };
    };
    /** A turn of this window that ends while the page is hidden. */
    const endAway = (status: "complete" | "error") => {
      act(() => {
        startTurn("live0001");
        reportEvent("live0001", "message.start");
      });
      hide(true);
      act(() => reportEvent("live0001", "message.complete", { status }));
    };

    it("waves once at a result that came while the member was away, saying so, when they come back", async () => {
      await arrive([IDLE, WAVE]);
      hide(true);
      items = [notice("n-2")];
      await act(() => loadTaskNotices());
      await settle();
      expect(fetched).not.toContain(WAVE);
      hide(false);
      await plays(WAVE);
      expect(await screen.findByText("整理照片 有新结果")).toBeInTheDocument();
      // Once: away and back again, nothing more to tell.
      fetched = [];
      hide(true);
      hide(false);
      await settle();
      expect(fetched).not.toContain(WAVE);
    });

    it("does not wave at a result read elsewhere meanwhile", async () => {
      await arrive([IDLE, WAVE]);
      hide(true);
      items = [notice("n-2")];
      await act(() => loadTaskNotices());
      items = [];
      await act(() => loadTaskNotices());
      hide(false);
      await settle();
      expect(fetched).not.toContain(WAVE);
    });

    it("names no task on the screen the family shares", async () => {
      cover.local = true;
      await arrive([IDLE, WAVE]);
      hide(true);
      items = [notice("n-2")];
      await act(() => loadTaskNotices());
      hide(false);
      expect(await screen.findByText("有新结果")).toBeInTheDocument();
      expect(screen.queryByText(/整理照片/)).toBeNull();
    });

    it("shows a turn's check for 10 s once back, without its fireworks", async () => {
      const { sign } = await arrive([IDLE, "工作状态-思考冒泡", "放烟花"]);
      endAway("complete");
      await settle();
      expect(sign()).toBeUndefined();
      fetched = [];
      vi.useFakeTimers({ shouldClearNativeTimers: true });
      try {
        hide(false);
        act(() => vi.advanceTimersByTime(0));
        expect(sign()).toBe("success");
        act(() => vi.advanceTimersByTime(9_999));
        expect(sign()).toBe("success");
        act(() => vi.advanceTimersByTime(1));
        expect(sign()).toBeUndefined();
      } finally {
        vi.useRealTimers();
      }
      await settle();
      expect(fetched).not.toContain("放烟花");
    });

    it("tells only the error when a turn failed and a result came too", async () => {
      const { sign } = await arrive([IDLE, "工作状态-思考冒泡", SIGH, WAVE]);
      endAway("error");
      items = [notice("n-2")];
      await act(() => loadTaskNotices());
      await settle();
      fetched = [];
      hide(false);
      await plays(SIGH);
      expect(sign()).toBe("error");
      await settle();
      expect(fetched).not.toContain(WAVE);
      expect(screen.queryByText(/有新结果/)).toBeNull();
    });

    it("tells nothing that waited more than 12 hours", async () => {
      const { sign } = await arrive([IDLE, "工作状态-思考冒泡", "放烟花"]);
      endAway("complete");
      await settle();
      const later = Date.now() + 12 * 60 * 60 * 1000 + 1;
      vi.spyOn(Date, "now").mockReturnValue(later);
      hide(false);
      await settle();
      expect(sign()).toBeUndefined();
    });

    it("forgets what waited when the next turn starts", async () => {
      const { sign } = await arrive([IDLE, "工作状态-思考冒泡", "放烟花"]);
      endAway("complete");
      // The next turn starts while the member is still away, and is stopped.
      act(() => {
        startTurn("live0002");
        reportEvent("live0002", "message.start");
      });
      act(() => stopTurn("live0002"));
      hide(false);
      await settle();
      expect(sign()).toBeUndefined();
    });
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

  it("keeps what VNAS said last in a turn over the line the turn's end shows", async () => {
    serve([IDLE, "碎碎念-发呆碎碎念", "工作状态-雀跃庆祝"]);
    await mount();
    act(() => {
      startTurn("live0001");
      reportEvent("live0001", "message.start");
    });
    write("say", { text: "照片整理好了" });
    expect(await screen.findByText("照片整理好了")).toBeInTheDocument();
    act(() => reportEvent("live0001", "message.complete", { status: "complete" }));
    await plays("工作状态-雀跃庆祝");
    await settle();
    expect(screen.getByText("照片整理好了")).toBeInTheDocument();
  });

  it("shows no picture and plays no address it is given", async () => {
    serve([IDLE, "东张西望"]);
    const { root } = await mount();
    write("say", { text: "看这张", image: "doge", url: `${origin}/elsewhere.png` });
    expect(await screen.findByText("看这张")).toBeInTheDocument();
    expect(root.querySelector("img")).toBeNull();
    fetched = [];
    write("anim", { url: `${origin}/companion/thumb/main/${encodeURIComponent("东张西望")}.webm` });
    await settle();
    expect(fetched).toEqual([]);
  });

  it("says it without an animation when none is installed to say it with", async () => {
    serve([IDLE]);
    await mount();
    write("say", { text: "照片整理好了" });
    expect(await screen.findByText("照片整理好了")).toBeInTheDocument();
  });

  it("is a button the keyboard presses", async () => {
    serve([IDLE, "点击回应-挠痒咯咯笑"]);
    await mount();
    const pet = screen.getByRole("button", { name: "大肥鱼" });
    expect(pet.tabIndex).toBe(0);
    fireEvent.keyDown(pet, { key: "Enter" });
    await plays("点击回应-挠痒咯咯笑");
  });

  it("sleeps, and wakes when it is clicked", async () => {
    serve([IDLE, "睡觉-开始", "睡觉-循环", "睡觉-起来"]);
    const { pet, videos } = await mount();
    // As VNAS puts it to sleep: through the page's sleep store, which a click wakes it from.
    act(() => void companionSleep.enter("main", { start: "睡觉-开始", loop: "睡觉-循环", end: "睡觉-起来" }));
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

describe("an animation that does not come", () => {
  const SLEEP = { start: "睡觉-开始", loop: "睡觉-循环", end: "睡觉-起来" };
  // A clip that does not come is fetched again a second later, and the card goes on two seconds after that.
  const later = { timeout: 5000 };

  it.each([
    ["the clip it falls asleep with", SLEEP.start, "network"],
    ["its sleeping loop", SLEEP.loop, "network"],
    ["the clip it wakes with", SLEEP.end, "http"],
  ] as const)("does not keep it asleep or awake when it is %s", async (_, clip, how) => {
    serve([IDLE, SLEEP.start, SLEEP.loop, SLEEP.end]);
    const { videos } = await mount();
    failing.set(clip, { times: 2, how });
    act(() => void companionSleep.enter("main", SLEEP));
    if (clip !== SLEEP.start) {
      await plays(SLEEP.start);
      await ready(videos);
      act(() => fireEvent(front(videos), new Event("ended")));
    }
    // Its loop plays though the clip before it did not come, and comes again after it did not.
    await waitFor(
      () => expect(fetched.filter((name) => name === SLEEP.loop)).toHaveLength(clip === SLEEP.loop ? 3 : 1),
      later,
    );
    await settle();
    await ready(videos);
    fetched = [];
    act(() => void companionSleep.wake("main"));
    if (clip === SLEEP.end) {
      // It wakes without it: VNAS was told it is awake, and the card goes on.
      await waitFor(() => expect(fetched).toEqual([SLEEP.end, SLEEP.end, IDLE]), later);
      return;
    }
    await plays(SLEEP.end);
    await ready(videos);
    act(() => fireEvent(front(videos), new Event("ended")));
    await plays(IDLE);
  });

  it("goes on when the clip after the one that ended does not come", async () => {
    serve([IDLE]);
    const { videos } = await mount();
    failing.set(IDLE, { times: 2, how: "http" });
    fetched = [];
    act(() => fireEvent(front(videos), new Event("ended")));
    await waitFor(() => expect(fetched).toEqual([IDLE, IDLE, IDLE]), later);
  });
});

describe("VNAS's commands, carried out by the card", () => {
  const SESSION = "live0001";

  it("answers an animation once the card has played it to its end", async () => {
    serve([IDLE, "点击回应-元气挥手"]);
    const { videos } = await mount();
    startTurn(SESSION);
    let outcome: Awaited<ReturnType<typeof runCompanionCommand>> | undefined;
    void runCompanionCommand(SESSION, { action: "anim", args: { name: "点击回应-元气挥手" } }).then((answer) => {
      outcome = answer;
    });
    await plays("点击回应-元气挥手");
    await ready(videos);
    expect(outcome).toBeUndefined();
    act(() => fireEvent(front(videos), new Event("ended")));
    await waitFor(() =>
      expect(outcome).toEqual({ ok: true, result: { status: "ended", playbackId: expect.stringMatching(/^pb-/) } }),
    );
  });

  it("shows an animation asked for while the one it speaks with still loads, and answers at its own end", async () => {
    serve([IDLE, "碎碎念-发呆碎碎念", "三球抛接"]);
    let made = 0;
    vi.spyOn(URL, "createObjectURL").mockImplementation(() => `blob:animation-${++made}`);
    let release = () => {};
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    server.use(
      http.untyped.get(`${origin}/companion/thumb/main/:name`, async ({ request }) => {
        const name = decodeURIComponent(new URL(request.url).pathname).replace(/^.*\/|\.(webm|mov)$/g, "");
        fetched.push(name);
        if (name === "三球抛接") await held;
        return new HttpResponse(new Blob(["webm"]));
      }),
    );
    const { videos } = await mount();
    startTurn(SESSION);
    const shown = front(videos);
    const back = videos.find((video) => video !== shown) as HTMLVideoElement;
    write("say", { text: "看我的！" });
    await plays("碎碎念-发呆碎碎念");
    await waitFor(() => expect(back.src).toMatch(/^blob:animation-/));
    const speaking = back.src;
    let outcome: Awaited<ReturnType<typeof runCompanionCommand>> | undefined;
    void runCompanionCommand(SESSION, { action: "anim", args: { name: "三球抛接" } }).then((answer) => {
      outcome = answer;
    });
    await plays("三球抛接");
    // The clip it speaks with finishes loading before the animation's arrives: it is not the animation.
    fireEvent(back, new Event("loadeddata"));
    expect(front(videos)).toBe(shown);
    release();
    await waitFor(() => expect(back.src).not.toBe(speaking));
    fireEvent(back, new Event("loadeddata"));
    expect(front(videos)).toBe(back);
    expect(outcome).toBeUndefined();
    act(() => fireEvent(back, new Event("ended")));
    await waitFor(() =>
      expect(outcome).toEqual({ ok: true, result: { status: "ended", playbackId: expect.stringMatching(/^pb-/) } }),
    );
  });

  it("does not show an animation that comes after VNAS was answered it did not play", async () => {
    serve([IDLE, "点击回应-元气挥手"]);
    const { videos } = await mount();
    startTurn(SESSION);
    const shown = front(videos);
    let outcome: Awaited<ReturnType<typeof runCompanionCommand>> | undefined;
    void runCompanionCommand(SESSION, { action: "anim", args: { name: "点击回应-元气挥手" } }).then((answer) => {
      outcome = answer;
    });
    await plays("点击回应-元气挥手");
    const { playbackId } = companionState.read().pets.main!.anim.data as { playbackId: string };
    // What the window does when its wait for the clip to start runs out (commands.test.ts).
    act(() => void companionPlayback.report(playbackId, "failed", "timeout"));
    await waitFor(() =>
      expect(outcome).toEqual({ ok: true, result: { status: "failed", playbackId, reason: "timeout" } }),
    );
    await settle();
    for (const video of videos) if (video !== shown) fireEvent(video, new Event("loadeddata"));
    await settle();
    expect(front(videos)).toBe(shown);
  });

  it("plays an animation to its end though the turn takes a step meanwhile, then shows the step", async () => {
    serve([IDLE, "点击回应-元气挥手", "东张西望"]);
    const { videos } = await mount();
    startTurn(SESSION);
    let outcome: Awaited<ReturnType<typeof runCompanionCommand>> | undefined;
    void runCompanionCommand(SESSION, { action: "anim", args: { name: "点击回应-元气挥手" } }).then((answer) => {
      outcome = answer;
    });
    await plays("点击回应-元气挥手");
    await ready(videos);
    fetched = [];
    act(() => reportEvent(SESSION, "tool.start", { name: "browser_snapshot" }));
    await settle();
    expect(fetched).toEqual([]);
    expect(outcome).toBeUndefined();
    act(() => fireEvent(front(videos), new Event("ended")));
    await waitFor(() =>
      expect(outcome).toEqual({ ok: true, result: { status: "ended", playbackId: expect.stringMatching(/^pb-/) } }),
    );
    await plays("东张西望");
  });

  it("shows the end of a turn after an animation that was still loading when the turn ended", async () => {
    serve([IDLE, "点击回应-元气挥手", "工作状态-雀跃庆祝"]);
    const { videos } = await mount();
    startTurn(SESSION);
    let outcome: Awaited<ReturnType<typeof runCompanionCommand>> | undefined;
    void runCompanionCommand(SESSION, { action: "anim", args: { name: "点击回应-元气挥手" } }).then((answer) => {
      outcome = answer;
    });
    await plays("点击回应-元气挥手");
    act(() => reportEvent(SESSION, "message.complete", { status: "complete" }));
    await settle();
    expect(fetched).not.toContain("工作状态-雀跃庆祝");
    expect(outcome).toBeUndefined();
    await ready(videos);
    act(() => fireEvent(front(videos), new Event("ended")));
    await waitFor(() =>
      expect(outcome).toEqual({ ok: true, result: { status: "ended", playbackId: expect.stringMatching(/^pb-/) } }),
    );
    await plays("工作状态-雀跃庆祝");
  });

  it.each([
    ["while the page was hidden", "hidden"],
    ["while it slept", "asleep"],
  ])("does not show a step it held back for an animation once the turn ended %s", async (_, away) => {
    const SLEEP = { start: "睡觉-开始", loop: "睡觉-循环", end: "睡觉-起来" };
    serve([IDLE, "点击回应-元气挥手", "东张西望", SLEEP.start, SLEEP.loop, SLEEP.end]);
    const { videos } = await mount();
    startTurn(SESSION);
    let outcome: Awaited<ReturnType<typeof runCompanionCommand>> | undefined;
    void runCompanionCommand(SESSION, { action: "anim", args: { name: "点击回应-元气挥手" } }).then((answer) => {
      outcome = answer;
    });
    await plays("点击回应-元气挥手");
    await ready(videos);
    // The turn looks at the desktop while the animation plays: its clip waits for the animation's end.
    fetched = [];
    act(() => reportEvent(SESSION, "tool.start", { name: "browser_snapshot" }));
    await settle();
    expect(fetched).toEqual([]);
    if (away === "hidden") hide(true);
    else {
      act(() => void companionSleep.enter("main", SLEEP));
      await plays(SLEEP.start);
      await ready(videos);
    }
    await waitFor(() => expect(outcome).toMatchObject({ ok: true, result: { status: "cancelled" } }));
    // The turn ends while the card does not follow its stages.
    act(() => reportEvent(SESSION, "message.complete", { status: "interrupted" }));
    await settle();
    if (away === "hidden") {
      fetched = [];
      hide(false);
    } else {
      act(() => fireEvent(front(videos), new Event("ended")));
      await plays(SLEEP.loop);
      await ready(videos);
      act(() => void companionSleep.wake("main"));
      await plays(SLEEP.end);
      await ready(videos);
      fetched = [];
      act(() => fireEvent(front(videos), new Event("ended")));
    }
    await plays(IDLE);
    await ready(videos);
    fetched = [];
    act(() => fireEvent(front(videos), new Event("ended")));
    await waitFor(() => expect(fetched).not.toEqual([]));
    expect(fetched).not.toContain("东张西望");
  });

  it("stops the animation of a turn the member stops, and goes back to its idle one", async () => {
    serve([IDLE, "点击回应-元气挥手"]);
    const { videos } = await mount();
    startTurn(SESSION);
    let outcome: Awaited<ReturnType<typeof runCompanionCommand>> | undefined;
    void runCompanionCommand(SESSION, { action: "anim", args: { name: "点击回应-元气挥手" } }).then((answer) => {
      outcome = answer;
    });
    await plays("点击回应-元气挥手");
    await ready(videos);
    fetched = [];
    // Another conversation's stop leaves it playing.
    act(() => stopTurnCommands("live0002"));
    await settle();
    expect(fetched).toEqual([]);
    act(() => stopTurnCommands(SESSION));
    await plays(IDLE);
    await ready(videos);
    await waitFor(() =>
      expect(outcome).toEqual({
        ok: true,
        result: { status: "cancelled", playbackId: expect.stringMatching(/^pb-/), reason: "superseded" },
      }),
    );
  });

  it("hears at once that the card left the page in the middle of an animation", async () => {
    serve([IDLE, "点击回应-元气挥手"]);
    const { view } = await mount();
    startTurn(SESSION);
    let outcome: Awaited<ReturnType<typeof runCompanionCommand>> | undefined;
    void runCompanionCommand(SESSION, { action: "anim", args: { name: "点击回应-元气挥手" } }).then((answer) => {
      outcome = answer;
    });
    await plays("点击回应-元气挥手");
    view.unmount();
    await waitFor(() =>
      expect(outcome).toEqual({
        ok: true,
        result: { status: "cancelled", playbackId: expect.stringMatching(/^pb-/), reason: "frozen" },
      }),
    );
  });

  it("hears that the page is hidden, and that a hidden card cut an animation short", async () => {
    serve([IDLE, "点击回应-元气挥手"]);
    await mount();
    startTurn(SESSION);
    hide(true);
    expect(await runCompanionCommand(SESSION, { action: "anim", args: { name: "点击回应-元气挥手" } })).toEqual({
      ok: false,
      error: { reason: "paused", message: expect.any(String) },
    });
    // A command written past the check (as a race would) is reported cancelled by the card.
    const record = companionPlayback.create("main", "点击回应-元气挥手");
    write("anim", { name: "点击回应-元气挥手", playbackId: record.playbackId });
    await waitFor(() => expect(companionPlayback.get(record.playbackId)?.status).toBe("cancelled"));
    expect(companionPlayback.get(record.playbackId)?.reason).toBe("frozen");
  });

  it("lets the member take it over from a lock VNAS holds", async () => {
    serve([IDLE]);
    const { pet } = await mount();
    startTurn(SESSION);
    const taken = await act(() => runCompanionCommand(SESSION, { action: "lock", args: {} }));
    expect(taken).toMatchObject({ ok: true, result: { status: "ok", lockId: expect.any(String) } });
    expect(companionLock.locked("main")).toBe(true);
    // jsdom has no pointer capture, which the card's grab takes.
    const element = Element.prototype as unknown as Record<string, unknown>;
    const capture = { hasPointerCapture: () => false, setPointerCapture: () => {}, releasePointerCapture: () => {} };
    const added = Object.keys(capture).filter((name) => !(name in element));
    for (const name of added) element[name] = capture[name as keyof typeof capture];
    try {
      fireEvent.pointerDown(pet, { button: 0, pointerId: 1 });
      fireEvent.pointerUp(pet, { button: 0, pointerId: 1 });
    } finally {
      for (const name of added) delete element[name];
    }
    expect(companionLock.locked("main")).toBe(false);
    expect(companionState.read().pets.main.lock.data).toEqual({ locked: false });
    const lockId = taken.ok ? taken.result.lockId : undefined;
    expect(
      await runCompanionCommand(SESSION, {
        action: "position",
        args: { x: 0.5, y: 0.5, mode: "instant", lock: lockId },
      }),
    ).toEqual({
      ok: false,
      error: { reason: "lock-lost", cause: "preempted", message: expect.any(String) },
    });
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

  it("goes on to where VNAS put it when the turn that asked for it ends on the way", async () => {
    serve([IDLE, "工作状态-雀跃庆祝"]);
    const { root } = await mount();
    act(() => startTurn("live0001"));
    write("position", { x: 0.25, y: 0.5, mode: "line", duration: 1000 });
    frames(10);
    act(() => reportEvent("live0001", "message.complete", { status: "complete" }));
    await plays("工作状态-雀跃庆祝");
    frames(80);
    expect(root.style.left).toBe(left(0.25));
    expect(saved()).toEqual({ rx: 0.25, ry: 0.5 });
  });

  /** Presses the pet with the mouse and drags it along the moves, a frame between each, then lets it go. */
  const drag = (pet: HTMLElement, moves: [number, number][], still = 0) => {
    // jsdom has no pointer capture, which the card's grab takes.
    const element = Element.prototype as unknown as Record<string, unknown>;
    const capture = { hasPointerCapture: () => false, setPointerCapture: () => {}, releasePointerCapture: () => {} };
    const added = Object.keys(capture).filter((name) => !(name in element));
    for (const name of added) element[name] = capture[name as keyof typeof capture];
    try {
      const pointer = { button: 0, pointerId: 1, pointerType: "mouse" };
      const [x, y] = moves[0];
      fireEvent.pointerDown(pet, { ...pointer, clientX: x, clientY: y });
      for (const [mx, my] of moves.slice(1)) {
        fireEvent.pointerMove(pet, { ...pointer, clientX: mx, clientY: my });
        frames(1);
      }
      frames(still);
      const [ux, uy] = moves.at(-1) ?? [x, y];
      fireEvent.pointerUp(pet, { ...pointer, clientX: ux, clientY: uy });
      fireEvent.click(pet, { clientX: ux, clientY: uy });
    } finally {
      for (const name of added) delete element[name];
    }
  };

  it("answers a click with a click response, then idles", async () => {
    serve([IDLE, "点击回应-元气挥手"]);
    const { pet, videos } = await mount();
    fetched = [];
    fireEvent.click(pet);
    await plays("点击回应-元气挥手");
    await ready(videos);
    fetched = [];
    act(() => fireEvent(front(videos), new Event("ended")));
    await plays(IDLE);
    expect(fetched).toEqual([IDLE]);
  });

  it("answers a click during VNAS's turn, then goes back to the turn's stage", async () => {
    serve([IDLE, "点击回应-元气挥手", "东张西望"]);
    const { pet, videos } = await mount();
    act(() => {
      startTurn("live0001");
      reportEvent("live0001", "tool.start", { name: "browser_snapshot" });
    });
    await plays("东张西望");
    await ready(videos);
    fetched = [];
    fireEvent.click(pet);
    await plays("点击回应-元气挥手");
    await ready(videos);
    fetched = [];
    act(() => fireEvent(front(videos), new Event("ended")));
    await plays("东张西望");
    expect(fetched).toEqual(["东张西望"]);
  });

  it("is dragged with its drag clip, put down where it is let go, and remembers the place on this device", async () => {
    serve([IDLE, "被鼠标拖拽悬空反馈", "点击回应-元气挥手"]);
    const { pet, root, videos } = await mount();
    write("position", { x: 0.25, y: 0.5, mode: "instant" });
    await waitFor(() => expect(root.style.left).toBe(left(0.25)));
    const box = root.getBoundingClientRect();
    const [x, y] = [box.x + 120, box.y + 60];
    fetched = [];
    // Slowly: held still before it is let go, so it is put down rather than thrown.
    drag(
      pet,
      [
        [x, y],
        [x + 40, y],
        [x + 80, y],
        [x + 120, y],
      ],
      60,
    );
    await plays("被鼠标拖拽悬空反馈");
    // The press that ends a drag is not a click.
    await settle();
    expect(fetched).not.toContain("点击回应-元气挥手");
    expect(queue.size).toBe(0);
    expect(saved()?.rx ?? 0).toBeGreaterThan(0.25);
    expect(Number.parseFloat(root.style.left)).toBeGreaterThan(Number.parseFloat(left(0.25)));
    expect(saved()?.ry).toBeCloseTo(0.5, 1);
    // Let go, it idles.
    await waitFor(() => expect(fetched.at(-1)).toBe(IDLE));
    await ready(videos);
  });

  /** Where the companion's hit area is (dsh-pet's HIT_BOX on its 240-wide box), from its box's style. */
  const hitArea = (root: HTMLElement) => {
    const x = Number.parseFloat(root.style.left);
    const y = Number.parseFloat(root.style.top);
    return { left: x + 75, top: y + 30, right: x + 165, bottom: y + 137 };
  };
  const clear = (hit: ReturnType<typeof hitArea>, window: { x: number; y: number; width: number; height: number }) =>
    hit.right <= window.x ||
    hit.left >= window.x + window.width ||
    hit.bottom <= window.y ||
    hit.top >= window.y + window.height;
  /** A window on top, laid out where it is (jsdom lays nothing out), as the desktop publishes it. */
  const openWindow = (window: { x: number; y: number; width: number; height: number }) => {
    document.querySelector(`[data-window-id="assistant"]`)?.remove();
    const element = document.createElement("section");
    element.dataset.windowId = "assistant";
    for (const [name, value] of Object.entries({
      offsetLeft: window.x,
      offsetTop: window.y,
      offsetWidth: window.width,
      offsetHeight: window.height,
    }))
      Object.defineProperty(element, name, { configurable: true, value });
    document.body.append(element);
    act(() => publishFocusedWindow({ id: "assistant", ...window, maximized: false }));
  };
  const wait = (ms: number) => act(() => new Promise((resolve) => setTimeout(resolve, ms)));
  afterEach(() => document.querySelector(`[data-window-id="assistant"]`)?.remove());

  it("steps aside from a window that opens over it, springing from its corner, without a clip of its own", async () => {
    serve([IDLE]);
    const { root } = await mount();
    await settle();
    fetched = [];
    // On a 1024×768 page its corner is at the bottom right: a window from 200 to 1000 covers it.
    const window = { x: 200, y: 40, width: 800, height: 700 };
    openWindow(window);
    // It springs from where it is, not in one jump.
    frames(2);
    expect(queue.size).toBeGreaterThan(0);
    expect(hitArea(root).left).toBeGreaterThan(700);
    frames(400);
    expect(queue.size).toBe(0);
    // Out to the window's left, at the same height.
    expect(hitArea(root).right).toBeCloseTo(200 - 12, 0);
    expect(hitArea(root).top).toBeCloseTo(768 - 88 - 135 + 30, 0);
    expect(clear(hitArea(root), window)).toBe(true);
    expect(saved()?.rx).toBeCloseTo((200 - 12 - 90 - 75 + 120) / 1024, 3);
    expect(fetched).toEqual([]);
  });

  it("goes there at once when motion is reduced", async () => {
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("reduce"), media: query }));
    serve([IDLE]);
    const { root } = await mount();
    openWindow({ x: 200, y: 40, width: 800, height: 700 });
    expect(queue.size).toBe(0);
    expect(hitArea(root).right).toBeCloseTo(200 - 12, 0);
  });

  it("steps aside from the window it is dropped on, once let go", async () => {
    serve([IDLE, "被鼠标拖拽悬空反馈"]);
    const { pet, root } = await mount();
    const window = { x: 500, y: 200, width: 520, height: 400 };
    openWindow(window);
    write("position", { x: 0.25, y: 0.5, mode: "instant" });
    await waitFor(() => expect(root.style.left).toBe(left(0.25)));
    // Clear of the window where VNAS put it: it stays.
    expect(clear(hitArea(root), window)).toBe(true);
    const box = root.getBoundingClientRect();
    const [x, y] = [box.x + 120, box.y + 60];
    drag(
      pet,
      [
        [x, y],
        [x + 100, y],
        [x + 200, y],
        [x + 300, y],
      ],
      60,
    );
    frames(400);
    await wait(200);
    frames(400);
    expect(queue.size).toBe(0);
    expect(hitArea(root).right).toBeCloseTo(500 - 12, 0);
    expect(clear(hitArea(root), window)).toBe(true);
  });

  it("goes on stepping aside after a click cuts it short", async () => {
    serve([IDLE, "点击回应-元气挥手"]);
    const { pet, root } = await mount();
    write("position", { x: 0.25, y: 0.5, mode: "instant" });
    await waitFor(() => expect(root.style.left).toBe(left(0.25)));
    const window = { x: 150, y: 100, width: 700, height: 500 };
    openWindow(window);
    frames(3);
    // The press stops the move halfway, over the window, and a click leaves no place behind it.
    const box = root.getBoundingClientRect();
    drag(pet, [[box.x + 120, box.y + 60]]);
    expect(queue.size).toBe(0);
    expect(clear(hitArea(root), window)).toBe(false);
    await wait(200);
    frames(400);
    expect(clear(hitArea(root), window)).toBe(true);
  });

  it("goes on stepping aside after a lock that starts cuts it short", async () => {
    serve([IDLE]);
    const { root } = await mount();
    write("position", { x: 0.25, y: 0.5, mode: "instant" });
    await waitFor(() => expect(root.style.left).toBe(left(0.25)));
    const window = { x: 150, y: 100, width: 700, height: 500 };
    openWindow(window);
    frames(3);
    write("lock", { locked: true, expiresAt: Date.now() + 60_000 });
    expect(clear(hitArea(root), window)).toBe(false);
    // Once it holds still: a flight would be left to land.
    await wait(200);
    frames(400);
    expect(clear(hitArea(root), window)).toBe(true);
  });

  it("goes on stepping aside after a lock that starts once an earlier one ran out", async () => {
    serve([IDLE]);
    const { root } = await mount();
    write("position", { x: 0.25, y: 0.5, mode: "instant" });
    await waitFor(() => expect(root.style.left).toBe(left(0.25)));
    write("lock", { locked: true, expiresAt: Date.now() + 50 });
    await wait(100);
    const window = { x: 150, y: 100, width: 700, height: 500 };
    openWindow(window);
    frames(3);
    write("lock", { locked: true, expiresAt: Date.now() + 60_000 });
    await wait(200);
    frames(400);
    expect(clear(hitArea(root), window)).toBe(true);
  });

  it("lets VNAS's move go on when it starts soon after a step aside", async () => {
    serve([IDLE]);
    const { root } = await mount();
    // From its corner it steps to the right of a window from 500 to 900.
    openWindow({ x: 500, y: 40, width: 400, height: 700 });
    frames(400);
    expect(hitArea(root).left).toBeCloseTo(900 + 12, 0);
    // VNAS sends it across the window to the left, within the step aside's 1.5 s.
    write("position", { x: 0.2, y: 0.5, mode: "line", duration: 3000 });
    frames(10);
    await wait(1600);
    frames(300);
    expect(root.style.left).toBe(left(0.2));
  });

  it("checks the window again when an animation cuts VNAS's move short over it", { timeout: 10_000 }, async () => {
    serve([IDLE, "点击回应-元气挥手"]);
    const { root } = await mount();
    const window = { x: 300, y: 100, width: 400, height: 400 };
    openWindow(window);
    write("position", { x: 0.12, y: 0.4, mode: "instant" });
    await waitFor(() => expect(root.style.left).toBe(left(0.12)));
    write("position", { x: 0.85, y: 0.4, mode: "line", duration: 1000 });
    frames(25);
    write("anim", { name: "点击回应-元气挥手" });
    frames(2);
    expect(clear(hitArea(root), window)).toBe(false);
    // Its hold runs out (the move's 1 s and 1.5 s more): then it steps aside.
    await wait(2700);
    frames(400);
    await wait(50);
    frames(400);
    expect(clear(hitArea(root), window)).toBe(true);
  });

  it("goes on to VNAS's target though the page is resized on the way", async () => {
    serve([IDLE]);
    const { root } = await mount();
    write("position", { x: 0.25, y: 0.5, mode: "instant" });
    await waitFor(() => expect(root.style.left).toBe(left(0.25)));
    write("position", { x: 0.75, y: 0.3, mode: "line", duration: 3000 });
    frames(10);
    openWindow({ x: 150, y: 330, width: 400, height: 300 });
    const width = Object.getOwnPropertyDescriptor(globalThis, "innerWidth");
    Object.defineProperty(globalThis, "innerWidth", { configurable: true, value: 1000 });
    try {
      act(() => globalThis.dispatchEvent(new Event("resize")));
      await wait(250);
      frames(300);
      expect(root.style.left).toBe(`${0.75 * 1000 - 120}px`);
    } finally {
      if (width) Object.defineProperty(globalThis, "innerWidth", width);
    }
  });

  it("stops a walk under way when a window comes up within its reach", async () => {
    serve([IDLE, "螃蟹走路"]);
    const { root, videos } = await mount();
    write("position", { x: 0.6, y: 0.8, mode: "instant" });
    await waitFor(() => expect(root.style.left).toBe(left(0.6)));
    vi.spyOn(Math, "random").mockReturnValue(0.975);
    act(() => fireEvent(front(videos), new Event("ended")));
    await plays("螃蟹走路");
    await ready(videos);
    Object.defineProperty(front(videos), "currentTime", { configurable: true, value: 5 });
    frames(3);
    expect(queue.size).toBe(1);
    const walked = root.style.left;
    expect(walked).not.toBe(left(0.6));
    // Clear of it, within a walk's reach to its left.
    openWindow({ x: 250, y: 300, width: 240, height: 400 });
    expect(queue.size).toBe(0);
    expect(Number.parseFloat(root.style.left)).toBeCloseTo(Number.parseFloat(walked), 3);
    expect((saved()?.rx ?? 0) * 1024 - 120).toBeCloseTo(Number.parseFloat(walked), 0);
  });

  it("lets VNAS's move land before it checks the window that came up on the way", async () => {
    serve([IDLE]);
    const { root } = await mount();
    write("position", { x: 0.25, y: 0.5, mode: "instant" });
    await waitFor(() => expect(root.style.left).toBe(left(0.25)));
    write("position", { x: 0.75, y: 0.3, mode: "line", duration: 3000 });
    frames(10);
    // Over where it set out from, clear of where VNAS sends it.
    openWindow({ x: 150, y: 330, width: 400, height: 300 });
    frames(300);
    expect(root.style.left).toBe(left(0.75));
    expect(saved()).toEqual({ rx: 0.75, ry: 0.3 });
  });

  it("steps aside when the page is resized under a window", async () => {
    serve([IDLE]);
    const { root } = await mount();
    // Clear of its corner on a 1024-wide page; over it on a 700-wide one.
    const window = { x: 100, y: 40, width: 500, height: 700 };
    openWindow(window);
    await settle();
    expect(root.style.left).toBe("");
    const width = Object.getOwnPropertyDescriptor(globalThis, "innerWidth");
    Object.defineProperty(globalThis, "innerWidth", { configurable: true, value: 700 });
    try {
      act(() => globalThis.dispatchEvent(new Event("resize")));
      await wait(250);
      frames(400);
      await wait(50);
      frames(400);
      expect(clear(hitArea(root), window)).toBe(true);
      expect(hitArea(root).right).toBeLessThanOrEqual(700 - 8);
    } finally {
      if (width) Object.defineProperty(globalThis, "innerWidth", width);
    }
  });

  it("does not walk where a walk could end on the window on top", async () => {
    serve([IDLE, "螃蟹走路"]);
    const { root, videos } = await mount();
    write("position", { x: 0.6, y: 0.8, mode: "instant" });
    await waitFor(() => expect(root.style.left).toBe(left(0.6)));
    // 29 px to its left, within a walk's reach (166 px at its size).
    openWindow({ x: 300, y: 300, width: 240, height: 400 });
    await settle();
    fetched = [];
    // Every roll would land on a move (animations.ts weighs idle 97, turn 0, move 1 of 100).
    vi.spyOn(Math, "random").mockReturnValue(0.975);
    act(() => fireEvent(front(videos), new Event("ended")));
    await settle();
    expect(fetched).not.toContain("螃蟹走路");
  });

  it("comes back wholly on the page from a place remembered at its very corner", async () => {
    localStorage.setItem(POSITION_KEY, JSON.stringify({ rx: 1, ry: 1 }));
    serve([IDLE]);
    const { root } = await mount();
    await waitFor(() => expect(root.style.left).not.toBe(""));
    // Its hit area within the page less 8 px, left of the system rail's 88 px.
    const hit = hitArea(root);
    expect(hit.left).toBeGreaterThanOrEqual(88);
    expect(hit.top).toBeGreaterThanOrEqual(8);
    expect(hit.right).toBeCloseTo(1024 - 8, 0);
    expect(hit.bottom).toBeCloseTo(768 - 8, 0);
  });

  it("flies when it is flung, and lands within the page", async () => {
    serve([IDLE, "被鼠标拖拽悬空反馈"]);
    const { pet, root } = await mount();
    write("position", { x: 0.25, y: 0.5, mode: "instant" });
    await waitFor(() => expect(root.style.left).toBe(left(0.25)));
    const box = root.getBoundingClientRect();
    const [x, y] = [box.x + 120, box.y + 60];
    drag(pet, [
      [x, y],
      [x + 60, y - 40],
      [x + 120, y - 80],
      [x + 180, y - 120],
    ]);
    // Let go in motion: it keeps flying the way it was flung, frame after frame, then lands.
    expect(queue.size).toBeGreaterThan(0);
    const released = Number.parseFloat(root.style.left);
    frames(3);
    expect(Number.parseFloat(root.style.left)).toBeGreaterThan(released);
    frames(3000);
    expect(queue.size).toBe(0);
    expect(Number.parseFloat(root.style.top)).toBeLessThanOrEqual(window.innerHeight);
    expect(Number.parseFloat(root.style.left)).toBeLessThanOrEqual(window.innerWidth);
    expect(saved()).not.toBeNull();
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
    // Every roll lands on a move (animations.ts weighs idle 97, turn 0, move 1 of 100).
    vi.spyOn(Math, "random").mockReturnValue(0.975);
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

  it("rests after a minute without input, and goes on when VNAS says something", async () => {
    serve([IDLE]);
    const { videos } = await mount();
    const pause = vi.mocked(HTMLMediaElement.prototype.pause);
    const play = vi.mocked(HTMLMediaElement.prototype.play);
    vi.useFakeTimers({ shouldClearNativeTimers: true });
    // Input starts the minute again, on the fake clock.
    fireEvent.keyDown(window, { key: "a" });
    pause.mockClear();
    act(() => vi.advanceTimersByTime(59_000));
    expect(pause.mock.contexts).not.toContain(front(videos));
    act(() => vi.advanceTimersByTime(1_000));
    expect(pause.mock.contexts).toContain(front(videos));
    vi.useRealTimers();
    // The member reads VNAS's reply without touching anything: what VNAS has the companion say shows.
    play.mockClear();
    write("say", { text: "照片整理好了" });
    expect(play.mock.contexts).toContain(front(videos));
    expect(await screen.findByText("照片整理好了")).toBeInTheDocument();
  });

  it("stays at rest under the local console's screen saver, and sleeps when the member is back", async () => {
    serve([IDLE, "睡觉-开始", "睡觉-循环", "睡觉-起来"]);
    const { videos } = await mount();
    const pause = vi.mocked(HTMLMediaElement.prototype.pause);
    const play = vi.mocked(HTMLMediaElement.prototype.play);
    vi.useFakeTimers({ shouldClearNativeTimers: true });
    fireEvent.keyDown(window, { key: "a" });
    pause.mockClear();
    act(() => vi.advanceTimersByTime(60_000));
    expect(pause.mock.contexts).toContain(front(videos));
    vi.useRealTimers();
    // The screen saver comes up; VNAS puts the companion to sleep, wakes it, and sleeps it again (D12).
    cover.up = true;
    play.mockClear();
    fetched = [];
    act(() => void companionSleep.enter("main", { start: "睡觉-开始", loop: "睡觉-循环", end: "睡觉-起来" }));
    act(() => void companionSleep.wake("main"));
    act(() => void companionSleep.enter("main", { start: "睡觉-开始", loop: "睡觉-循环", end: "睡觉-起来" }));
    await settle();
    expect(play.mock.contexts).not.toContain(front(videos));
    expect(fetched).toEqual([]);
    // The member is back: the last of them goes on.
    cover.up = false;
    fireEvent.keyDown(window, { key: "a" });
    expect(play.mock.contexts).toContain(front(videos));
    await plays("睡觉-开始");
  });

  it.each([
    ["a key", (target: Element) => (fireEvent.keyDown(target, { key: "a" }), fireEvent.keyUp(target, { key: "a" }))],
    ["a click", (target: Element) => (fireEvent.pointerDown(target), fireEvent.pointerUp(target))],
    ["a wheel", (target: Element) => fireEvent.wheel(target)],
    ["a pointer move", (target: Element) => fireEvent.pointerMove(target)],
  ])("goes on at %s that takes the local console's screen saver away", async (_, press) => {
    serve([IDLE]);
    const { videos } = await mount();
    const pause = vi.mocked(HTMLMediaElement.prototype.pause);
    const play = vi.mocked(HTMLMediaElement.prototype.play);
    vi.useFakeTimers({ shouldClearNativeTimers: true });
    fireEvent.keyDown(window, { key: "a" });
    pause.mockClear();
    act(() => vi.advanceTimersByTime(60_000));
    expect(pause.mock.contexts).toContain(front(videos));
    vi.useRealTimers();
    // As LocalConsoleScreenSaver: the input that takes it away goes no further than the window.
    cover.up = true;
    const saver = (event: Event) => {
      if (!cover.up) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      cover.up = false;
      for (const listener of cover.dismissed) listener();
    };
    const kinds = ["pointerdown", "pointermove", "keydown", "wheel", "touchstart"];
    for (const kind of kinds) window.addEventListener(kind, saver, { capture: true });
    try {
      play.mockClear();
      act(() => void press(document.body));
      expect(cover.up).toBe(false);
      expect(play.mock.contexts).toContain(front(videos));
    } finally {
      for (const kind of kinds) window.removeEventListener(kind, saver, { capture: true });
    }
  });

  it("takes what VNAS says as the member's input, and rests a minute after it", async () => {
    serve([IDLE]);
    const { videos } = await mount();
    const pause = vi.mocked(HTMLMediaElement.prototype.pause);
    vi.useFakeTimers({ shouldClearNativeTimers: true });
    fireEvent.keyDown(window, { key: "a" });
    act(() => vi.advanceTimersByTime(50_000));
    write("say", { text: "照片整理好了" });
    pause.mockClear();
    act(() => vi.advanceTimersByTime(59_000));
    expect(pause.mock.contexts).not.toContain(front(videos));
    act(() => vi.advanceTimersByTime(1_000));
    expect(pause.mock.contexts).toContain(front(videos));
    vi.useRealTimers();
  });

  it("keeps a clip that finishes loading while the page is hidden still until the page is back", async () => {
    serve([IDLE, "东张西望"]);
    const { videos } = await mount();
    const pause = vi.mocked(HTMLMediaElement.prototype.pause);
    const play = vi.mocked(HTMLMediaElement.prototype.play);
    write("anim", { name: "东张西望" });
    await plays("东张西望");
    hide(true);
    play.mockClear();
    pause.mockClear();
    await ready(videos);
    const loaded = front(videos);
    expect(play.mock.contexts).not.toContain(loaded);
    expect(pause.mock.contexts).toContain(loaded);
    hide(false);
    expect(play.mock.contexts).toContain(loaded);
  });
});
