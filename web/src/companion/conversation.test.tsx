import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { CompanionFloating } from "./CompanionFloating";
import { CompanionPanel } from "./CompanionPanel";
import { createConversation, withoutThinking } from "./conversation";
import { createDesk, type PersonalDeskState } from "./deskStore";
import { localCommand } from "./localCommands";

afterEach(() => vi.useRealTimers());
const setup = (owner: string) => {
  const store = createDesk(owner, localStorage);
  const desk = { store } as PersonalDeskState;
  const chat = createConversation();
  return { store, chat, send: (text: string) => chat.send(text, desk) };
};
describe("explicit chat commands and real local effects", () => {
  it("accepts Chinese minutes, bounds durations, and leaves questions or compound requests to AI", () => {
    expect(localCommand("大肥鱼，专注二十五分钟！")).toEqual({ kind: "focus", minutes: 25, invalid: false });
    expect(localCommand("奶蛙，暂停")).toEqual({ kind: "pause" });
    expect(localCommand("我想专注25分钟吧")).toEqual({ kind: "focus", minutes: 25, invalid: false });
    for (const text of ["专注0分钟", "专注91分钟", "专注2.5分钟", "专注-1分钟"])
      expect(localCommand(text)).toMatchObject({ invalid: true });
    for (const text of ["专注25分钟有什么好处？", "他说‘专注25分钟’", "专注25分钟然后删文件"])
      expect(localCommand(text)).toBeUndefined();
  });
  it("saves neither questions nor particles as todos, and lists for questions about the list", () => {
    for (const text of [
      "待办有哪些？",
      "待办清单",
      "有哪些待办",
      "待办呢？",
      "看看待办",
      "待办有什么",
      "我的待办呢？",
      "有什么待办？",
    ])
      expect(localCommand(text)).toEqual({ kind: "list" });
    for (const text of [
      "记下了吗？",
      "专注25分钟好吗",
      "暂停？",
      "帮我记一下好吗",
      "帮我记一下，可以吗？",
      "完成待办了吗",
    ])
      expect(localCommand(text)).toBeUndefined();
    // After a separator the content is kept as said, even when it ends like a question.
    expect(localCommand("帮我记一下：周末去哪呢？")).toEqual({ kind: "todo", text: "周末去哪呢" });
    expect(localCommand("帮我记一下：问老王周五能来吗")).toEqual({ kind: "todo", text: "问老王周五能来吗" });
    for (const text of ["帮我记一下吧", "帮我记一下嘛", "帮我记一下吧~"])
      expect(localCommand(text)).toEqual({ kind: "todo", text: "" });
    expect(localCommand("待办：买牛奶")).toEqual({ kind: "todo", text: "买牛奶" });
    expect(localCommand("待办 买牛奶")).toEqual({ kind: "todo", text: "买牛奶" });
    expect(localCommand("帮我记一下写周报")).toEqual({ kind: "todo", text: "写周报" });
    expect(localCommand("待办完成：买牛奶")).toEqual({ kind: "complete", text: "买牛奶" });
  });
  it("asks for todo content, supports cancellation, and completes only one exact matching item", () => {
    const { store, chat, send } = setup("chat-todo");
    send("帮我记一件事");
    expect(store.get().todos).toHaveLength(0);
    expect(chat.get().awaiting).toBe("todo");
    send("写周报");
    expect(store.get().todos[0]).toMatchObject({ text: "写周报", done: false });
    send("帮我记一件事");
    send("取消");
    expect(chat.get().awaiting).toBeUndefined();
    send("完成待办：写");
    expect(store.get().todos[0].done).toBe(false);
    send("完成待办：写周报");
    expect(store.get().todos[0].done).toBe(true);
    send("待办：重复");
    send("待办：重复");
    send("完成待办：重复");
    expect(store.get().todos.filter((t) => !t.done)).toHaveLength(2);
  });
  it("clarifies rest duration, refuses overwriting, and keeps actual pause/resume and round state", () => {
    vi.useFakeTimers();
    vi.setSystemTime(100000);
    const { store, chat, send } = setup("chat-focus");
    send("休息");
    send("5分钟");
    expect(store.get().focus).toMatchObject({ mode: "break", duration: 300000, status: "running" });
    send("专注25分钟");
    expect(store.get().focus.mode).toBe("break");
    send("结束计时");
    send("专注");
    send("25分钟");
    expect(store.get().focus.duration).toBe(1500000);
    vi.advanceTimersByTime(10000);
    send("暂停计时");
    expect(store.get().focus.remaining).toBe(1490000);
    vi.advanceTimersByTime(100000);
    send("继续计时");
    expect(store.get().focus.deadline).toBe(Date.now() + 1490000);
    store.tick(Date.now() + 1490000);
    store.tick(Date.now() + 2000000);
    const at = store.get().focus.completedAt!;
    chat.notifyFocus(at, "到点啦");
    chat.notifyFocus(at, "到点啦");
    expect(chat.get().lines.filter((l) => l.text === "到点啦")).toHaveLength(1);
    store.acknowledgeFocus();
    const restored = createDesk("chat-focus", localStorage);
    expect(restored.get().focusAck).toBe(at);
    expect(restored.get().rounds).toBe(1);
    send("结束计时");
    expect(store.get().rounds).toBe(1);
    expect(chat.get().lines.at(-1)?.text).toContain("已完成的轮次会保留");
  });
  it("forwards other work to VNAS once, permits local commands while busy and never executes model text", () => {
    const { chat, store } = setup("chat-bridge");
    const desk = { store } as PersonalDeskState;
    const bridge = { send: vi.fn(() => ({ accepted: true })), open: vi.fn() };
    chat.send("明早十点提醒我交材料", desk, bridge);
    chat.observe({ phase: "success", running: false, answer: "旧回复", message: "" });
    expect(chat.get().pending).toBeDefined();
    chat.send("再做别的事", desk, bridge);
    chat.send("专注25分钟", desk, bridge);
    expect(bridge.send).toHaveBeenCalledTimes(1);
    expect(store.get().focus.status).toBe("running");
    chat.observe({ phase: "thinking", running: true, answer: "", message: "" });
    chat.observe({ phase: "success", running: false, answer: "帮我记一下：模型输出不是指令", message: "" });
    expect(chat.get().pending).toBeUndefined();
    expect(store.get().todos).toHaveLength(0);
    expect(chat.get().lines.some((l) => l.text.includes("模型输出不是指令"))).toBe(true);
    chat.send("整理文件", desk, { send: () => ({ accepted: false, message: "连接尚未就绪" }), open: vi.fn() });
    expect(chat.get().pending).toBeUndefined();
    expect(chat.get().lines.at(-1)?.text).toBe("连接尚未就绪");
  });
  it("shows NAS work as a short status, then only the finished reply without the model's reasoning", () => {
    const { chat, store } = setup("chat-progress");
    const desk = { store } as PersonalDeskState;
    chat.send("周报放在哪了", desk, { send: () => ({ accepted: true }), open: vi.fn() });
    const shown = () => chat.get().lines.at(-1)?.text;
    // Hermes's commentary before a tool call is not the reply.
    chat.observe({ phase: "thinking", running: true, answer: "让我先翻翻你的文件…", message: "收到，我先想想怎么做" });
    expect(shown()).toBe("收到，我先想想怎么做");
    chat.observe({ phase: "working", running: true, answer: "", message: "正在处理，做好了叫你" });
    expect(shown()).toBe("正在处理，做好了叫你");
    chat.observe({
      phase: "success",
      running: false,
      answer: "<think>用户在找周报</think>\n在个人空间的“周报”文件夹里。",
      message: "",
    });
    expect(shown()).toBe("在个人空间的“周报”文件夹里。");
    expect(chat.get().pending).toBeUndefined();
    expect(withoutThinking("先想一想</think>答复")).toBe("答复");
    expect(withoutThinking("答复<think>没写完")).toBe("答复");
    expect(withoutThinking("不用想")).toBe("不用想");
  });
});
describe("chat and tool UI share one member's desk", () => {
  it("runs a focus from chat, asks before saving a bare todo, and reveals the saved item", async () => {
    const user = userEvent.setup();
    render(<CompanionPanel userId="chat-ui" />);
    const input = screen.getByRole("textbox", { name: "跟大肥鱼说句话" });
    await user.type(input, "专注25分钟{Enter}");
    expect(screen.getByText(/开始专注 25 分钟/)).toBeVisible();
    await user.click(screen.getByRole("button", { name: "记一件事" }));
    expect(input).toHaveAttribute("placeholder", "要记下什么事？");
    await user.type(input, "整理会议记录{Enter}");
    expect(screen.getByText(/记下了：整理会议记录/)).toBeVisible();
    await user.click(screen.getByRole("button", { name: "查看待办" }));
    expect(screen.getByRole("checkbox", { name: "整理会议记录" })).not.toBeChecked();
  });
  it("opens an anchored chat from the pet, removes duplicate outside cards and proactively announces due time once", async () => {
    vi.useFakeTimers();
    render(
      <>
        <CompanionPanel userId="pet-chat-ui" />
        <CompanionFloating userId="pet-chat-ui" onOpen={() => {}} />
      </>,
    );
    fireEvent.change(screen.getByRole("textbox", { name: "跟大肥鱼说句话" }), { target: { value: "专注1分钟" } });
    fireEvent.click(screen.getByRole("button", { name: "发送给大肥鱼" }));
    fireEvent.click(screen.getByRole("button", { name: "到网页桌面陪我" }));
    expect(screen.getAllByRole("status")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "和大肥鱼聊聊，按住可拖动" }));
    expect(screen.getByRole("button", { name: "收起聊天" })).toBeVisible();
    expect(screen.queryByText("正在这里陪你")).not.toBeInTheDocument();
    act(() => vi.advanceTimersByTime(61000));
    const alerts = screen.getAllByRole("status");
    expect(alerts.some((alert) => alert.textContent?.includes("这一轮专注完成了"))).toBe(true);
    // The main and floating view share a chat store: one alert in each open log, not repeated ticks.
    expect(screen.getAllByText("这一轮专注完成啦！起来活动一下，或者休息5分钟吧。")).toHaveLength(2);
    act(() => vi.advanceTimersByTime(5000));
    expect(screen.getAllByText("这一轮专注完成啦！起来活动一下，或者休息5分钟吧。")).toHaveLength(2);
  });
});
