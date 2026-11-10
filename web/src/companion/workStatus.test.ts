import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { WORK_STATUS_STATES } from "./dsh-pet/shared/work-status";
import {
  companionState,
  forgetAll,
  isPrompting,
  onTurnStopped,
  reportCards,
  reportEvent,
  resetWorkStatus,
  stageOf,
  startTurn,
  stopTurn,
} from "./workStatus";

const leaf = () => companionState.read().sections.workStatus;
const shown = () => (leaf().data as { state?: string } | null)?.state ?? null;
/** The clip and the bubble's line the shown step asks for. */
const detail = () => {
  const data = leaf().data as { task?: string | null; anim?: string | null } | null;
  return { anim: data?.anim ?? null, task: data?.task ?? null };
};
const tool = (type: "tool.start" | "tool.complete", name = "browser_snapshot") => reportEvent("s1", type, { name });

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  resetWorkStatus();
  vi.useRealTimers();
  vi.restoreAllMocks();
  window.history.replaceState(null, "", "/");
});

describe("what VNAS is doing, as dsh-pet's host sums it up", () => {
  it("maps the gateway's events onto dsh-pet's stages", () => {
    expect(stageOf("message.start")).toBe("thinking");
    expect(stageOf("tool.start", { name: "search_files" })).toBe("working");
    expect(stageOf("tool.complete", { name: "search_files" })).toBe("result");
    expect(stageOf("tool.start", { name: "_bookkeeping" })).toBeUndefined();
    // clarify asks the member, as a card does.
    expect(stageOf("tool.start", { name: "clarify" })).toBe("waiting");
    expect(stageOf("message.complete", { status: "complete" })).toBe("success");
    expect(stageOf("message.complete", { status: "error" })).toBe("error");
    expect(stageOf("message.complete", { status: "interrupted" })).toBeNull();
    for (const type of ["message.start", "tool.start", "tool.complete", "message.complete"])
      expect([...WORK_STATUS_STATES, null, undefined]).toContain(stageOf(type, { name: "x" }));
  });

  it("moves only for a turn this window started", () => {
    reportEvent("s1", "message.start");
    expect(shown()).toBeNull();
    startTurn("s1");
    reportEvent("s1", "message.start");
    expect(shown()).toBe("thinking");
    // The turn is over: what comes later from that conversation is another window's turn.
    reportEvent("s1", "message.complete", { status: "complete" });
    reportEvent("s1", "message.start");
    expect(shown()).toBe("success");
  });

  it("lets a turn go the moment the member stops it here, whatever Hermes still reports of it", () => {
    startTurn("s1");
    tool("tool.start", "search_files");
    expect(shown()).toBe("working");
    stopTurn("s1");
    expect(shown()).toBeNull();
    expect(isPrompting("s1")).toBe(false);
    // Hermes finishes the step under way and ends the turn after the stop.
    tool("tool.complete", "search_files");
    reportEvent("s1", "message.complete", { status: "interrupted" });
    expect(shown()).toBeNull();
    // Stopping a turn the window did not start, or no turn, touches nothing.
    startTurn("s2");
    reportEvent("s2", "message.start");
    stopTurn("s1");
    expect(shown()).toBe("thinking");
  });

  it("keeps a finished turn's success or error 60 s, then shows nothing, so the next result waves again", () => {
    startTurn("s1");
    reportEvent("s1", "message.complete", { status: "error" });
    expect(shown()).toBe("error");
    vi.advanceTimersByTime(59_999);
    expect(shown()).toBe("error");
    vi.advanceTimersByTime(1);
    expect(shown()).toBeNull();
  });

  it("does not let an old error hold back another conversation's work once it is cleared", () => {
    startTurn("s1");
    reportEvent("s1", "message.complete", { status: "error" });
    startTurn("s2");
    reportEvent("s2", "tool.start", { name: "search_files" });
    expect(shown()).toBe("error");
    vi.advanceTimersByTime(60_000);
    expect(shown()).toBe("working");
  });

  it("merges a working to result flip within 5 s, and publishes the latest after it", () => {
    // The same line for each step, so that only the stages differ.
    vi.spyOn(Math, "random").mockReturnValue(0);
    startTurn("s1");
    tool("tool.start");
    const working = leaf().counter;
    vi.advanceTimersByTime(1000);
    tool("tool.complete");
    tool("tool.start");
    tool("tool.complete");
    // Within 5 s of showing working: nothing new is published.
    expect(leaf().counter).toBe(working);
    expect(shown()).toBe("working");
    vi.advanceTimersByTime(4000);
    expect(shown()).toBe("result");
    // result back to working is published at once.
    tool("tool.start");
    expect(shown()).toBe("working");
  });

  it("publishes nothing while what is shown stays the same", () => {
    startTurn("s1");
    reportEvent("s1", "message.start");
    const counter = leaf().counter;
    reportEvent("s1", "reasoning.delta");
    reportEvent("s1", "reasoning.delta");
    expect(leaf().counter).toBe(counter);
  });

  it("shows each step with its own clip and a short line about it", () => {
    vi.spyOn(Math, "random").mockReturnValue(0);
    startTurn("s1");
    reportEvent("s1", "message.start");
    expect(detail()).toEqual({ anim: "工作状态-思考冒泡", task: "正在认真想下一步呢" });
    reportEvent("s1", "tool.start", { name: "terminal", tool_id: "c1", args: { command: "du -sh /nas/照片/2026" } });
    expect(shown()).toBe("working");
    expect(detail()).toEqual({ anim: "写代码", task: "跑一下 du -sh /nas/…，稍等哦" });
    vi.advanceTimersByTime(5000);
    reportEvent("s1", "tool.complete", { name: "terminal", tool_id: "c1" });
    expect(shown()).toBe("result");
    expect(detail()).toEqual({ anim: "工作状态-雀跃庆祝", task: "搞定一步，继续看看哦~" });
    reportEvent("s1", "tool.start", { name: "read_file", tool_id: "c2", args: { path: "/nas/文档/a.md" } });
    expect(detail()).toEqual({ anim: "工作状态-清点归档", task: "我在看 文档/a.md 哦" });
    // VNAS moving the pet shows the stage's own clip and lines, not the last step's.
    reportEvent("s1", "tool.start", { name: "mcp__companion__anim", tool_id: "c3", args: { name: "三球抛接" } });
    expect(detail()).toEqual({ anim: null, task: null });
    reportEvent("s1", "message.complete", { status: "complete" });
    expect(shown()).toBe("success");
    expect(detail()).toEqual({ anim: "放烟花", task: "全部搞定啦！" });
  });

  it("keeps a step's line while it lasts, and names the todo list's step while thinking", () => {
    startTurn("s1");
    reportEvent("s1", "message.start");
    const first = detail().task;
    for (let i = 0; i < 20; i++) reportEvent("s1", "reasoning.delta", { name: undefined });
    expect(detail().task).toBe(first);
    reportEvent("s1", "todo.updated", {
      todos: [
        { status: "completed", content: "列出照片" },
        { status: "in_progress", content: "按拍摄月份把照片分进文件夹" },
      ],
    });
    expect(detail()).toEqual({ anim: "工作状态-思考冒泡", task: "下一步：按拍摄月份把照片分进文件…" });
    // A list with nothing left to do names nothing.
    reportEvent("s1", "todo.updated", { todos: [{ status: "completed", content: "列出照片" }] });
    expect(detail().task).not.toContain("下一步");
  });

  it("names nothing a step works on on the local console's shared screen", () => {
    window.history.replaceState(null, "", "/?local-console=1");
    startTurn("s1");
    reportEvent("s1", "todo.updated", { todos: [{ status: "in_progress", content: "整理家庭账本" }] });
    reportEvent("s1", "message.start");
    expect(detail().task).not.toContain("账本");
    for (let i = 0; i < 10; i++) {
      reportEvent("s1", "tool.start", { name: "read_file", tool_id: `c${i}`, args: { path: "/nas/家庭账本.xlsx" } });
      expect(detail()).toEqual({ anim: "工作状态-清点归档", task: "一页一页认真读~" });
    }
  });

  it("tells the page when the member stops a turn here, and only then", () => {
    const stopped = vi.fn();
    const off = onTurnStopped(stopped);
    stopTurn("s1");
    expect(stopped).not.toHaveBeenCalled();
    startTurn("s1");
    reportEvent("s1", "message.start");
    stopTurn("s1");
    expect(stopped).toHaveBeenCalledTimes(1);
    expect(shown()).toBeNull();
    off();
    startTurn("s1");
    stopTurn("s1");
    expect(stopped).toHaveBeenCalledTimes(1);
  });

  it("shows a card waiting in this window's turn, and only while it waits", () => {
    startTurn("s1");
    tool("tool.start");
    reportCards([
      { requestId: "r1", sessionId: "s1", status: "awaiting" },
      { requestId: "r2", sessionId: "elsewhere", status: "awaiting" },
    ]);
    expect(shown()).toBe("waiting");
    expect(detail().anim).toBe("工作状态-原地踱步张望");
    reportCards([{ requestId: "r1", sessionId: "s1", status: "approved" }]);
    expect(shown()).toBe("working");
    reportCards([{ requestId: "r2", sessionId: "elsewhere", status: "awaiting" }]);
    expect(shown()).toBe("working");
  });

  it("goes back to idle when the window loses its connection, cards included", () => {
    startTurn("s1");
    tool("tool.start");
    reportCards([{ requestId: "r1", sessionId: "s1", status: "awaiting" }]);
    expect(shown()).toBe("waiting");
    forgetAll();
    expect(shown()).toBeNull();
    // Events of the turn after the loss no longer count: the window starts the next one itself.
    tool("tool.start");
    expect(shown()).toBeNull();
  });
});
