import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { CompanionFloating } from "./CompanionFloating";
import { CompanionPanel } from "./CompanionPanel";
import { createDesk, deskKey, focusDue, usePersonalDesk } from "./deskStore";
import { Stage } from "./Stage";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("a member's focus and todos on this device", () => {
  it("restores a running deadline after reload or sleep and completes only once", () => {
    const desk = createDesk("deadline-owner", localStorage);
    desk.start(25, "focus", null, 10000);
    const restored = createDesk("deadline-owner", localStorage);
    expect(restored.get().focus.deadline).toBe(1510000);
    restored.tick(1600000);
    restored.tick(1800000);
    expect(restored.get().focus.status).toBe("done");
    expect(restored.get().rounds).toBe(1);
    expect(createDesk("deadline-owner", localStorage).get().rounds).toBe(1);
  });
  it("keeps what is valid when stored data is partly bad, and keeps unreadable data aside", () => {
    localStorage.setItem(
      deskKey("partly-bad"),
      JSON.stringify({
        todos: [{ id: "a", text: "留下", done: false }, { id: 1, text: "坏的" }, null],
        focus: { status: "bogus" },
        rounds: 3,
      }),
    );
    const desk = createDesk("partly-bad", localStorage);
    expect(desk.get().todos.map((t) => t.text)).toEqual(["留下"]);
    expect(desk.get().focus.status).toBe("idle");
    expect(desk.get().rounds).toBe(3);
    expect(desk.get().persistent).toBe(true);
    localStorage.setItem(deskKey("unreadable"), "{not json");
    expect(createDesk("unreadable", localStorage).get().persistent).toBe(true);
    expect(localStorage.getItem(`${deskKey("unreadable")}:unreadable`)).toBe("{not json");
  });
  it("does not drop a change that could not be saved when the desk is shown again", () => {
    let stored: string | null = null;
    let full = false;
    const storage = {
      getItem: () => stored,
      setItem: (_key: string, text: string) => {
        if (full) throw new Error("quota");
        stored = text;
      },
    };
    const desk = createDesk("quota-owner", storage);
    desk.add("存得下");
    full = true;
    desk.add("存不下");
    expect(desk.get().persistent).toBe(false);
    const stop = desk.subscribe(() => {});
    expect(desk.get().todos.map((t) => t.text)).toEqual(["存得下", "存不下"]);
    stop();
  });
  it("keeps counting down when the system clock goes back", () => {
    const start = 5_000_000;
    const back = start - 60 * 60000;
    const desk = createDesk("clock-back", localStorage);
    desk.start(10, "focus", null, start);
    desk.tick(start + 3 * 60000);
    desk.tick(back);
    expect(desk.get().focus.status).toBe("running");
    expect((desk.get().focus.deadline ?? 0) - back).toBe(10 * 60000);
    desk.tick(back + 10 * 60000 - 1);
    expect(desk.get().focus.status).toBe("running");
    desk.tick(back + 10 * 60000);
    expect(desk.get().focus.status).toBe("done");
  });
  it("lets the custom minutes be typed freely and rounds them only when done", () => {
    render(<CompanionPanel userId="minutes-owner" />);
    fireEvent.mouseDown(screen.getByRole("tab", { name: "专注计时" }), { button: 0, ctrlKey: false });
    const field = screen.getByRole("spinbutton", { name: "专注时长（分钟）" });
    fireEvent.change(field, { target: { value: "" } });
    expect(field).toHaveValue(null);
    expect(screen.getByRole("timer")).toHaveTextContent("25:00");
    fireEvent.change(field, { target: { value: "4" } });
    fireEvent.change(field, { target: { value: "40" } });
    expect(field).toHaveValue(40);
    expect(screen.getByRole("timer")).toHaveTextContent("40:00");
    fireEvent.change(field, { target: { value: "120" } });
    expect(screen.getByRole("timer")).toHaveTextContent("90:00");
    fireEvent.blur(field);
    expect(field).toHaveValue(90);
    // An empty field starts the minutes shown, not one minute.
    fireEvent.change(field, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "开始专注" }));
    expect(screen.getByRole("timer")).toHaveTextContent("90:00");
  });
  it("still reminds about a round that finished after the clock went back", () => {
    const start = 50_000_000;
    const desk = createDesk("ack-back", localStorage);
    desk.start(1, "focus", null, start);
    desk.tick(start + 60000);
    desk.acknowledgeFocus();
    expect(focusDue(desk.get())).toBe(false);
    desk.reset();
    const back = start - 8 * 3600000;
    desk.start(1, "focus", null, back);
    desk.tick(back + 60000);
    expect(focusDue(desk.get())).toBe(true);
  });
  it("redraws every second only while a focus is running", () => {
    vi.useFakeTimers();
    const rendered = vi.fn();
    function Probe() {
      rendered();
      const desk = usePersonalDesk("tick-owner");
      return (
        <>
          <span>{desk.clock}</span>
          <button type="button" onClick={() => desk.store.start(1, "focus", null, Date.now())}>
            start
          </button>
          <button type="button" onClick={() => desk.store.pause(Date.now())}>
            pause
          </button>
        </>
      );
    }
    const renders = () => rendered.mock.calls.length;
    // One second per act: updates inside a single act are batched into one render.
    const seconds = (count: number) => {
      for (let i = 0; i < count; i++) act(() => vi.advanceTimersByTime(1000));
    };
    render(<Probe />);
    const idle = renders();
    seconds(5);
    expect(renders()).toBe(idle);
    fireEvent.click(screen.getByRole("button", { name: "start" }));
    const started = renders();
    seconds(3);
    expect(renders()).toBe(started + 3);
    expect(screen.getByText("00:57")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "pause" }));
    const paused = renders();
    seconds(5);
    expect(renders()).toBe(paused);
  });
  it("pauses without consuming time, resumes and refuses to replace an active focus", () => {
    const desk = createDesk("pause-owner", localStorage);
    desk.start(1, "focus", null, 1000);
    desk.pause(11000);
    desk.tick(1000000);
    expect(desk.get().focus.remaining).toBe(50000);
    desk.start(45, "focus", null, 1000000);
    expect(desk.get().focus.duration).toBe(60000);
    desk.resume(1000000);
    desk.tick(1049999);
    expect(desk.get().focus.status).toBe("running");
    desk.tick(1050000);
    expect(desk.get().rounds).toBe(1);
  });
  it("links a todo without marking it done, counts only focus and keeps members separate on LAN HTTP", () => {
    vi.stubGlobal("crypto", { getRandomValues: crypto.getRandomValues.bind(crypto) });
    const desk = createDesk("todos-owner", localStorage);
    desk.add("写完第一段");
    const id = desk.get().todos[0].id;
    desk.start(1, "focus", id, 1000);
    desk.tick(61000);
    expect(desk.get().focus.title).toBe("写完第一段");
    expect(desk.get().todos[0].done).toBe(false);
    desk.toggle(id);
    expect(createDesk("todos-owner", localStorage).get().todos[0].done).toBe(true);
    desk.start(5, "break", null, 100000);
    desk.tick(500000);
    expect(desk.get().rounds).toBe(1);
    expect(createDesk("other-owner", localStorage).get().todos).toEqual([]);
  });
  it("bounds corrupt state and keeps controls usable when storage is denied", () => {
    localStorage.setItem(
      deskKey("bad-desk"),
      JSON.stringify({ todos: [], focus: { status: "running", duration: -1 }, rounds: 9 }),
    );
    expect(createDesk("bad-desk", localStorage).get().focus.status).toBe("idle");
    const denied = {
      getItem: () => {
        throw Error("denied");
      },
      setItem: () => {
        throw Error("full");
      },
    };
    const desk = createDesk("denied-desk", denied);
    desk.add("临时事项");
    desk.start(1, "focus", null, 1000);
    desk.tick(61000);
    expect(desk.get().persistent).toBe(false);
    expect(desk.get().rounds).toBe(1);
  });
  it("offers simple tools, moves the running clock with the page pet and never starts peekaboo", () => {
    vi.useFakeTimers();
    render(
      <>
        <CompanionPanel userId="desk-ui" />
        <CompanionFloating userId="desk-ui" onOpen={() => {}} />
      </>,
    );
    fireEvent.mouseDown(screen.getByRole("tab", { name: "专注计时" }), { button: 0, ctrlKey: false });
    expect(screen.getByRole("timer")).toHaveTextContent("25:00");
    expect(screen.queryByRole("button", { name: "陪我一下" })).not.toBeInTheDocument();
    fireEvent.mouseDown(screen.getByRole("tab", { name: "简单待办" }), { button: 0, ctrlKey: false });
    fireEvent.change(screen.getByRole("textbox", { name: "添加待办" }), { target: { value: "整理今天的笔记" } });
    fireEvent.click(screen.getByRole("button", { name: "添加" }));
    act(() => vi.setSystemTime(Date.now() + 500));
    fireEvent.click(screen.getByRole("button", { name: "专注：整理今天的笔记" }));
    expect(screen.getByRole("timer")).toHaveTextContent("25:00");
    act(() => vi.advanceTimersByTime(10000));
    expect(screen.getByRole("timer")).toHaveTextContent("24:50");
    fireEvent.click(screen.getByRole("button", { name: "到网页桌面陪我" }));
    const pet = screen.getByRole("complementary", { name: "网页桌宠" });
    expect(within(pet).getByRole("button", { name: "查看专注计时 24:50" })).toBeVisible();
    act(() => vi.advanceTimersByTime(25 * 60000));
    expect(within(pet).getByRole("button", { name: "专注到点了，打开提醒" })).toBeVisible();
    expect(within(pet).getByRole("status")).toHaveTextContent("这一轮专注完成");
    fireEvent.mouseDown(screen.getByRole("tab", { name: /专注计时/ }), { button: 0, ctrlKey: false });
    fireEvent.click(screen.getByRole("button", { name: "叫回小窝" }));
    expect(screen.queryByRole("complementary", { name: "网页桌宠" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "完成这条待办" })).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "完成这条待办" }));
    fireEvent.mouseDown(screen.getByRole("tab", { name: "简单待办" }), { button: 0, ctrlKey: false });
    expect(screen.getByRole("checkbox", { name: "整理今天的笔记" })).toBeChecked();
    expect(screen.queryByRole("button", { name: "找到躲起来的大肥鱼" })).not.toBeInTheDocument();
  });
  it("still opens when the browser blocks access to localStorage itself", () => {
    vi.spyOn(window, "localStorage", "get").mockImplementation(() => {
      throw Error("blocked");
    });
    render(<CompanionPanel userId="storage-getter-denied" />);
    expect(screen.getByText("浏览器暂时无法保存，这次的计时和待办仅在当前页面保留。")).toBeVisible();
    fireEvent.mouseDown(screen.getByRole("tab", { name: "专注计时" }), { button: 0, ctrlKey: false });
    fireEvent.click(screen.getByRole("button", { name: "开始专注" }));
    expect(screen.getByRole("button", { name: "暂停" })).toBeVisible();
  });
  it("keeps a real task event ahead of a local timer message, and respects quiet mode", () => {
    const page = render(
      <Stage character="naiwa" quiet={false} taskPhase="working" companionMessage="专注完成" companionAction="happy" />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("正在忙这件事");
    page.rerender(<Stage character="naiwa" quiet={false} companionMessage="专注完成" companionAction="happy" />);
    expect(screen.getByRole("status")).toHaveTextContent("专注完成");
    page.rerender(<Stage character="naiwa" quiet companionMessage="专注完成" companionAction="happy" />);
    expect(screen.getByRole("status")).toHaveTextContent("安静陪你");
  });
});
