import { afterEach, describe, expect, it } from "vitest";

import {
  activityOf,
  forgetTurn,
  readAssistantActivity,
  reportAwaiting,
  reportTurn,
  resetAssistantActivity,
} from "./assistantActivity";

afterEach(resetAssistantActivity);

describe("what the desktop shows of VNAS", () => {
  it("reads a turn's stages from the gateway's events", () => {
    expect(activityOf("message.start")).toBe("thinking");
    expect(activityOf("reasoning.delta")).toBe("thinking");
    expect(activityOf("tool.start", { name: "browser_click" })).toBe("working");
    expect(activityOf("tool.complete", { name: "browser_click" })).toBe("result");
    // Hermes's own bookkeeping is not a step of the member's task.
    expect(activityOf("tool.start", { name: "_memory_flush" })).toBeUndefined();
    expect(activityOf("message.delta")).toBeUndefined();
    expect(activityOf("message.complete", { status: "complete" })).toBe("success");
    expect(activityOf("message.complete", { status: "error" })).toBe("error");
    expect(activityOf("message.complete", { status: "interrupted" })).toBeNull();
  });

  it("shows a card waiting for the member first, then the most pressing turn", () => {
    reportTurn("a", "success");
    reportTurn("b", "working");
    expect(readAssistantActivity().state).toBe("working");
    reportAwaiting(1);
    expect(readAssistantActivity().state).toBe("waiting");
    reportAwaiting(0);
    reportTurn("b", "result");
    // A turn still going is not outranked by one that finished elsewhere.
    expect(readAssistantActivity().state).toBe("result");
    reportTurn("b", null);
    expect(readAssistantActivity().state).toBe("success");
    forgetTurn("a");
    expect(readAssistantActivity().state).toBeNull();
  });

  it("counts each change, so the same stage reached again plays again", () => {
    reportTurn("a", "success");
    const first = readAssistantActivity();
    reportTurn("a", "success");
    expect(readAssistantActivity()).toBe(first);
    reportTurn("a", "thinking");
    reportTurn("a", "success");
    expect(readAssistantActivity().state).toBe("success");
    expect(readAssistantActivity().seq).toBeGreaterThan(first.seq);
  });
});
