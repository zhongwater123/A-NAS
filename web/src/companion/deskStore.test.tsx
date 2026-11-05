import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { CompanionFloating } from "./CompanionFloating";
import { CompanionPanel } from "./CompanionPanel";
import { createDesk, deskKey } from "./deskStore";
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
