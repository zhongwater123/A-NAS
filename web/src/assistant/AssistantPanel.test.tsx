import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Approval } from "./api";
import type { SessionMessage } from "./hermes/types/hermes";
import { stubAPI } from "../test/api";

// The window against a stand-in for the member's Hermes: the gateway's events
// are pushed by the test, REST answers come from a stubbed fetch.
type Handler = (event: { type: string; session_id?: string; payload?: unknown }) => void;
const handlers = new Set<Handler>();
const client = {
  request: vi.fn(async (method: string): Promise<unknown> => {
    if (method === "session.resume") return { session_id: LIVE, running: true };
    if (method === "session.active_list") return { sessions: [] };
    return {};
  }),
  onEvent: (handler: Handler) => {
    handlers.add(handler);
    return () => handlers.delete(handler);
  },
  on: () => () => undefined,
};
vi.mock("./useAssistantGateway", () => ({
  useAssistantGateway: () => ({ client, generation: 1, ready: true, problem: "" }),
}));

const { AssistantPanel } = await import("./AssistantPanel");

const LIVE = "live0001";
const STORED = "20261021_090000_aaaaaa";

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;
Element.prototype.scrollTo ??= function scrollTo() {};

afterEach(() => {
  vi.unstubAllGlobals();
  handlers.clear();
});

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

const history: SessionMessage[] = [
  { id: 1, role: "user", content: "打开相册", timestamp: 1_760_000_000 } as SessionMessage,
  { id: 2, role: "assistant", content: "相册已经打开。", timestamp: 1_760_000_001 } as SessionMessage,
];

async function open(approvals: Approval[] = [], stored = history, reply = "相册已经打开。") {
  stubAPI(
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/assistant/sessions"))
        return json({
          sessions: [{ id: STORED, title: "操作桌面", preview: "", startedAt: 1_760_000_000, messageCount: 2 }],
        });
      if (url.includes("/messages")) return json({ messages: stored });
      if (url.endsWith("/approvals")) return json({ approvals });
      return json({ available: true });
    }),
  );
  render(<AssistantPanel username="admin" />);
  const row = await screen.findByText("操作桌面");
  await act(async () => {
    row.click();
  });
  await screen.findByText(reply);
  await waitFor(() => expect(client.request).toHaveBeenCalledWith("session.resume", expect.anything()));
}

const emit = (type: string, payload: unknown = {}) =>
  act(async () => {
    for (const handler of handlers) handler({ type, session_id: LIVE, payload });
  });

describe("the VNAS window", () => {
  it("opens a conversation in which Hermes closed a turn that did not complete", async () => {
    // Hermes's own boundary row after a failed turn (agent/turn_failure_copy.py),
    // which its history serves as a notice rather than the model's answer.
    const stored = [
      ...history,
      { id: 3, role: "user", content: "把照片都移到共享空间", timestamp: 1_760_000_010 },
      {
        id: 4,
        role: "assistant",
        content: "",
        timestamp: 1_760_000_011,
        tool_calls: [{ id: "call_00_a", type: "function", function: { name: "browser_snapshot", arguments: "{}" } }],
      },
      {
        id: 5,
        role: "tool",
        tool_call_id: "call_00_a",
        tool_name: "browser_snapshot",
        content: '- button "相册" [ref=e1]',
        timestamp: 1_760_000_012,
      },
      {
        id: 6,
        role: "assistant",
        display_kind: "failed_turn",
        timestamp: 1_760_000_013,
        content:
          "This turn did not complete. Some actions may already have run; verify their effects before resending.",
      },
    ] as SessionMessage[];
    await open([], stored, "这一轮没有完成，其中一些操作可能已经执行；重新发送前请先确认结果。");
    expect(screen.getByText("相册已经打开。")).toBeTruthy();
    expect(screen.queryByText(/This turn did not complete/)).toBeNull();
    // What ran before the turn failed is still shown.
    expect(screen.getByText("1 个动作")).toBeTruthy();
  });

  it("stays up when a turn reuses a finished tool call's id", async () => {
    await open();
    const crashes: unknown[] = [];
    const spy = vi.spyOn(console, "error").mockImplementation((error) => {
      crashes.push(error);
    });
    try {
      // Hermes passes the provider's ids through, and some providers repeat them (#87857).
      for (let step = 0; step < 6; step++) {
        await emit("tool.start", { tool_id: "call_0", name: "browser_snapshot" });
        await emit("tool.complete", {
          tool_id: "call_0",
          name: "browser_snapshot",
          result: '- button "相册" [ref=e1]',
        });
      }
      await emit("message.delta", { text: "看完了。" });
    } finally {
      spy.mockRestore();
    }
    expect(crashes.map(String).filter((error) => error.includes("Duplicate key"))).toEqual([]);
    expect(await screen.findByText("看完了。")).toBeTruthy();
  });

  it("shows only the cards waiting for the member, and drops one once it is answered", async () => {
    const card = (requestId: string, status: Approval["status"], text: string): Approval => ({
      requestId,
      kind: "command",
      sessionId: LIVE,
      storedSessionId: STORED,
      status,
      createdAt: Date.now() / 1000,
      command: { text },
    });
    await open([
      card("req-1", "approved", "rm -rf 旧草稿"),
      card("req-2", "rejected", "rm 临时文件"),
      card("req-3", "awaiting", "rm -rf 缓存"),
    ]);
    expect(await screen.findByText("rm -rf 缓存")).toBeTruthy();
    expect(screen.queryByText("rm -rf 旧草稿")).toBeNull();
    expect(screen.queryByText("rm 临时文件")).toBeNull();

    // Allowed in another window of the member: the product service tells every window.
    await emit("anas.approval", card("req-3", "approved", "rm -rf 缓存"));
    expect(screen.queryByText("rm -rf 缓存")).toBeNull();
    expect(screen.queryByRole("region", { name: "本对话中等你确认或回答的事" })).toBeNull();
  });
});
