import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { WORK_STATUS_STATES } from "./dsh-pet/shared/work-status";
import {
  companionState,
  forgetAll,
  isPrompting,
  reportCards,
  reportEvent,
  resetWorkStatus,
  stageOf,
  startTurn,
  stopTurn,
} from "./workStatus";

const leaf = () => companionState.read().sections.workStatus;
const shown = () => (leaf().data as { state?: string } | null)?.state ?? null;
const tool = (type: "tool.start" | "tool.complete", name = "browser_snapshot") => reportEvent("s1", type, { name });

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  resetWorkStatus();
  vi.useRealTimers();
});

describe("what VNAS is doing, as dsh-pet's host sums it up", () => {
  it("maps the gateway's events onto dsh-pet's stages", () => {
    expect(stageOf("message.start")).toBe("thinking");
    expect(stageOf("tool.start", { name: "search_files" })).toBe("working");
    expect(stageOf("tool.complete", { name: "search_files" })).toBe("result");
    expect(stageOf("tool.start", { name: "_bookkeeping" })).toBeUndefined();
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

  it("shows a card waiting in this window's turn, and only while it waits", () => {
    startTurn("s1");
    tool("tool.start");
    reportCards([
      { requestId: "r1", sessionId: "s1", status: "awaiting" },
      { requestId: "r2", sessionId: "elsewhere", status: "awaiting" },
    ]);
    expect(shown()).toBe("waiting");
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
