import { describe, expect, it } from "vitest";

import type { AssistantApproval } from "../api.generated";
import { minutesPrompt, taskSummary } from "./taskState";

const input = { connected: true, ready: true, loading: false, running: false, error: "", messages: [], approvals: [] };
const answer = [
  { id: "a", role: "assistant" as const, parts: [{ type: "text" as const, text: "已经保存了 /nas/个人空间/a.md" }] },
];
const card: AssistantApproval = {
  requestId: "r",
  kind: "change",
  sessionId: "abcdef12",
  status: "awaiting",
  createdAt: 1,
  change: { id: "c", summary: "纪要", status: "awaiting", operations: [{ op: "write", path: "/nas/个人空间/a.md" }] },
};
describe("pet task presentation", () => {
  it("requires executor evidence for saved files", () => {
    expect(taskSummary({ ...input, messages: answer }).paths).toEqual([]);
    expect(taskSummary({ ...input, messages: answer, approvals: [card] })).toMatchObject({
      phase: "waiting",
      paths: [],
    });
    expect(taskSummary({ ...input, approvals: [{ ...card, status: "approved" }] }).paths).toEqual([]);
    expect(
      taskSummary({
        ...input,
        approvals: [{ ...card, status: "applied", change: { ...card.change!, status: "applied" } }],
      }).paths,
    ).toEqual(["/nas/个人空间/a.md"]);
  });
  it("keeps disconnects, errors and interruption distinct from a finished answer", () => {
    expect(taskSummary({ ...input, messages: answer, ready: false }).phase).toBe("connecting");
    expect(taskSummary({ ...input, messages: answer, error: "失败" }).phase).toBe("error");
    expect(taskSummary({ ...input, messages: [{ ...answer[0], interrupted: true }] }).phase).toBe("stopped");
    expect(taskSummary({ ...input, messages: answer, running: true }).phase).toBe("thinking");
  });
  it("quotes an input path and requires the original approval flow", () => {
    const path = '/nas/个人空间/a"b.md';
    expect(minutesPrompt(path, "列待办")).toContain(JSON.stringify({ sourcePath: path, request: "列待办" }));
    expect(minutesPrompt(path, "")).toContain("不要保存到个人空间");
  });
});
