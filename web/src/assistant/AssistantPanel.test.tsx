import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { AssistantMessage } from "../api.generated";
import { resetCompanionCommands } from "../companion/commands";
import { companionLock, companionPlayback, resetCompanionHost } from "../companion/host";
import { companionState, resetWorkStatus, setCompanionReady, startTurn } from "../companion/workStatus";
import { assistantHandlers, assistantSession } from "../test/handlers/assistant";
import { server } from "../test/server";
import type { Approval } from "./api";

// The window against a stand-in for the member's Hermes: the gateway's events
// are pushed by the test, REST answers come from the test server.
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
  handlers.clear();
  resetWorkStatus();
  resetCompanionHost();
  resetCompanionCommands();
  setCompanionReady(false);
});

// What the companion would show: the page's GET /state section the VNAS window feeds.
const shown = () => (companionState.read().sections.workStatus.data as { state?: string } | null)?.state ?? null;
/** The clip the shown step asks for. */
const clip = () => (companionState.read().sections.workStatus.data as { anim?: string } | null)?.anim ?? null;

const history: AssistantMessage[] = [
  { id: 1, role: "user", content: "打开相册", timestamp: 1_760_000_000 },
  { id: 2, role: "assistant", content: "相册已经打开。", timestamp: 1_760_000_001 },
];

async function open(approvals: Approval[] = [], stored = history, reply = "相册已经打开。") {
  server.use(
    ...assistantHandlers({
      sessions: [assistantSession(STORED, { title: "操作桌面", messageCount: 2 })],
      messages: stored,
      approvals,
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
    const stored: AssistantMessage[] = [
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
    ];
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

  it("moves the companion for the turns this window starts, and stops once the window closes", async () => {
    const card = (status: Approval["status"]): Approval => ({
      requestId: "req-9",
      sessionId: LIVE,
      storedSessionId: STORED,
      kind: "command",
      status,
      createdAt: Date.now() / 1000,
      command: { text: "rm -rf 缓存" },
    });
    // A turn driven from another device, and its card, are not this device's companion's.
    await open([card("awaiting")]);
    await emit("message.start");
    expect(shown()).toBeNull();
    await emit("message.complete", { status: "complete" });

    // This window sends the next prompt: it registers as the conversation's browser first.
    const user = userEvent.setup();
    await user.type(screen.getByRole("textbox"), "清理缓存");
    await user.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() =>
      expect(client.request).toHaveBeenCalledWith(
        "browser.controller.register",
        expect.objectContaining({ session_id: LIVE }),
      ),
    );
    await emit("message.start");
    expect(shown()).toBe("thinking");
    expect(clip()).toBe("工作状态-思考冒泡");
    // Each step with the clip of what it does: looking at the desktop.
    await emit("tool.start", { tool_id: "call_1", name: "browser_snapshot" });
    expect(shown()).toBe("working");
    expect(clip()).toBe("东张西望");
    await emit("anas.approval", card("awaiting"));
    expect(shown()).toBe("waiting");
    expect(clip()).toBe("工作状态-原地踱步张望");
    await emit("anas.approval", card("approved"));
    expect(shown()).toBe("working");
    expect(clip()).toBe("东张西望");
    await emit("message.complete", { status: "complete" });
    expect(shown()).toBe("success");
    expect(clip()).toBe("放烟花");

    cleanup();
    expect(shown()).toBeNull();
  });
  it("carries out VNAS's commands to the companion and answers each one", async () => {
    await open();
    setCompanionReady(true);
    // The window started this turn (it registers before every prompt, as the companion test above shows).
    startTurn(LIVE);
    const results = () => client.request.mock.calls.filter(([method]) => method === "companion.result");
    const command = (command_id: string, action: string, args: Record<string, unknown>) => ({
      command_id,
      action,
      args,
    });

    await emit("anas.companion", command("c".repeat(32), "broadcast", { text: "照片整理好了", duration: 5000 }));
    expect(companionState.read().pets.main.say.data).toEqual({ text: "照片整理好了", duration: 5000, anim: true });
    await waitFor(() =>
      expect(client.request).toHaveBeenCalledWith("companion.result", {
        session_id: LIVE,
        command_id: "c".repeat(32),
        ok: true,
        result: { status: "ok" },
      }),
    );

    // An animation is answered once it has played to its end.
    await emit("anas.companion", command("a".repeat(32), "anim", { name: "点击回应-元气挥手" }));
    const { playbackId } = companionState.read().pets.main.anim.data as { playbackId: string };
    expect(results()).toHaveLength(1);
    companionPlayback.report(playbackId, "ended");
    await waitFor(() =>
      expect(client.request).toHaveBeenCalledWith("companion.result", {
        session_id: LIVE,
        command_id: "a".repeat(32),
        ok: true,
        result: { status: "ended", playbackId },
      }),
    );

    // Another conversation's command is answered, and refused; a malformed one is not answered at all.
    await act(async () => {
      for (const handler of handlers)
        handler({ type: "anas.companion", session_id: "live0002", payload: command("b".repeat(32), "wake", {}) });
    });
    await waitFor(() =>
      expect(client.request).toHaveBeenCalledWith("companion.result", {
        session_id: "live0002",
        command_id: "b".repeat(32),
        ok: false,
        error: { reason: "not-controlling", message: expect.any(String) },
      }),
    );
    await emit("anas.companion", { action: "wake", args: {} });
    await emit("anas.companion", { command_id: "d".repeat(32), args: {} });
    await act(() => new Promise((resolve) => setTimeout(resolve, 20)));
    expect(results()).toHaveLength(3);

    // A window that closes stops waiting on an animation and answers nothing, and the lock VNAS
    // took goes with it, as the product service can no longer release it.
    await emit("anas.companion", command("e".repeat(32), "anim", { name: "点击回应-元气挥手" }));
    await emit("anas.companion", command("f".repeat(32), "lock", {}));
    await waitFor(() => expect(results()).toHaveLength(4));
    expect(companionLock.locked("main")).toBe(true);
    cleanup();
    expect(companionPlayback.waiterCount()).toBe(0);
    expect(companionLock.locked("main")).toBe(false);
    await act(() => new Promise((resolve) => setTimeout(resolve, 20)));
    expect(results()).toHaveLength(4);
  });
});
