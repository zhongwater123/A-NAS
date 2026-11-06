import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AssistantApproval } from "../api.generated";
import { assistantHandlers, noticesHandlers, tasksHandlers } from "../test/handlers/assistant";
import { entry, filesHandlers, ownSpace, sharedSpace, space } from "../test/handlers/files";
import { server } from "../test/server";
import { type AgentBridge, createConversation } from "./conversation";
import { createDesk, type PersonalDeskState } from "./deskStore";
import { ConnectedTask } from "./TaskExperience";
import type { CompanionView } from "./useCompanionView";

type Handler = (event: { type: string; session_id?: string; payload?: unknown }) => void;
const handlers = new Set<Handler>();
const LIVE = "abcdef12",
  STORED = "20261102_090000_abcdef";
const client = {
  request: vi.fn(
    async (method: string): Promise<unknown> =>
      method === "session.create"
        ? { session_id: LIVE, stored_session_id: STORED }
        : method === "session.resume"
          ? { session_id: LIVE, running: false }
          : {},
  ),
  onEvent: (h: Handler) => {
    handlers.add(h);
    return () => handlers.delete(h);
  },
  on: () => () => undefined,
};
vi.mock("../assistant/useAssistantGateway", () => ({
  useAssistantGateway: () => ({ client, ready: true, generation: 1, problem: "" }),
}));
beforeEach(() => {
  handlers.clear();
  client.request.mockClear();
});
function view() {
  const state = {
    conversationId: "",
    revision: 1,
    character: "naiwa",
    quiet: false,
    playCount: 0,
    active: null,
    pending: null,
  };
  const bindConversation = vi.fn(async (id: string) => {
    state.conversationId = id;
  });
  return {
    local: false,
    remote: { connection: "online", here: true, state, sync: { bindConversation, get: () => ({ here: true, state }) } },
  } as unknown as CompanionView;
}
function fixtures() {
  server.use(
    ...assistantHandlers(),
    ...tasksHandlers([]),
    ...noticesHandlers().handlers,
    ...filesHandlers({
      spaces: [ownSpace, sharedSpace, space("other", { kind: "private", name: "另一个成员", ownerUserId: "other" })],
      entries: [
        entry("会议.md", { spaceId: ownSpace.id }),
        entry("很大.md", { spaceId: ownSpace.id, sizeBytes: 40000 }),
        entry("照片.png", { spaceId: ownSpace.id }),
      ],
      text: "# 实际保存的纪要",
    }),
  );
}
const emit = (type: string, payload: unknown) =>
  act(() => {
    for (const h of handlers) h({ type, session_id: LIVE, payload });
  });
describe("companion minutes workflow on the existing VNAS protocol", () => {
  it("bridges the new chat to the same VNAS protocol and renders only its new turn", async () => {
    fixtures();
    const bridge = { current: undefined as AgentBridge | undefined };
    const chat = createConversation();
    const store = createDesk("chat-protocol", localStorage);
    const desk = { store } as PersonalDeskState;
    render(
      <ConnectedTask
        view={view()}
        userId="user:owner"
        bridge={bridge}
        onChatState={chat.observe}
        onPhase={() => undefined}
      />,
    );
    await waitFor(() => expect(bridge.current).toBeDefined());
    // Cards must finish loading before a NAS prompt is accepted.
    await waitFor(() => expect(screen.getByRole("button", { name: "试试提醒" })).toBeEnabled());
    act(() => chat.send("明早十点提醒我交材料", desk, bridge.current ?? undefined));
    await waitFor(() =>
      expect(client.request).toHaveBeenCalledWith(
        "prompt.submit",
        expect.objectContaining({ text: expect.stringContaining('"request":"明早十点提醒我交材料"') }),
      ),
    );
    emit("message.start", {});
    emit("message.delta", { text: "请先确认明天的具体日期。" });
    emit("message.complete", { status: "complete" });
    await waitFor(() => expect(chat.get().pending).toBeUndefined());
    expect(chat.get().lines.at(-1)?.text).toBe("请先确认明天的具体日期。");
    expect(store.get().todos).toHaveLength(0);
    expect(store.get().focus.status).toBe("idle");
    expect(client.request.mock.calls.filter(([method]) => method === "prompt.submit")).toHaveLength(1);
  });
  it("hands a natural-language reminder to VNAS and never treats the model's words as a saved schedule", async () => {
    fixtures();
    const user = userEvent.setup();
    render(<ConnectedTask view={view()} userId="user:owner" onPhase={() => undefined} />);
    await user.click(screen.getByRole("button", { name: "试试提醒" }));
    expect(client.request.mock.calls.some(([method]) => method === "prompt.submit")).toBe(false);
    expect(screen.getByRole("textbox", { name: "交代一件事" })).toHaveValue("明早十点提醒我交材料");
    await user.click(await screen.findByRole("button", { name: "交给奶蛙" }));
    await waitFor(() =>
      expect(client.request).toHaveBeenCalledWith(
        "prompt.submit",
        expect.objectContaining({
          text: expect.stringContaining('"request":"明早十点提醒我交材料"'),
        }),
      ),
    );
    emit("message.start", {});
    emit("message.delta", { text: "已安排好了，明早提醒你" });
    emit("message.complete", { status: "complete" });
    await user.click(screen.getByText("我的提醒与后台任务"));
    expect(await screen.findByText("还没有定时任务")).toBeVisible();
    expect(screen.queryByRole("button", { name: "保存到个人空间" })).toBeNull();
    expect(client.request.mock.calls.filter(([method]) => method === "prompt.submit")).toHaveLength(1);
  });
  it("drives the pet through actual gateway events, not success words in streamed text", async () => {
    fixtures();
    const user = userEvent.setup();
    const onPhase = vi.fn();
    render(<ConnectedTask view={view()} userId="user:owner" onPhase={onPhase} />);
    await user.click(screen.getByText("整理 NAS 里的会议记录"));
    await user.click(await screen.findByRole("button", { name: "会议.md" }));
    await user.click(screen.getByRole("button", { name: "整理这份记录" }));
    await waitFor(() => expect(client.request).toHaveBeenCalledWith("prompt.submit", expect.anything()));
    emit("message.start", {});
    expect(onPhase).toHaveBeenCalledWith("thinking");
    emit("tool.start", { name: "read_file", tool_id: "read-1", args: { path: "/nas/个人空间/会议.md" } });
    expect(onPhase).toHaveBeenCalledWith("working");
    emit("message.delta", { text: "全部完成啦，文件也保存好了" });
    expect(onPhase).not.toHaveBeenCalledWith("success");
    emit("message.complete", { status: "complete" });
    expect(onPhase).toHaveBeenCalledWith("success");
    emit("message.start", {});
    emit("message.complete", { status: "error" });
    expect(onPhase).toHaveBeenCalledWith("error");
  });
  it("shares before submission, shows real waiting cards, and never confirms them itself", async () => {
    fixtures();
    const user = userEvent.setup();
    const current = view();
    const open = vi.fn();
    const rendered = render(
      <ConnectedTask view={current} userId="user:owner" onPhase={() => undefined} onOpenConversation={open} />,
    );
    await user.click(screen.getByText("整理 NAS 里的会议记录"));
    await user.click(await screen.findByRole("button", { name: "会议.md" }));
    expect(screen.getByRole("button", { name: "很大.md" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "照片.png" })).toBeDisabled();
    expect(screen.queryByRole("option", { name: "另一个成员" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "整理这份记录" }));
    await waitFor(() =>
      expect(client.request).toHaveBeenCalledWith(
        "prompt.submit",
        expect.objectContaining({ session_id: LIVE, text: expect.stringContaining("/nas/个人空间/会议.md") }),
      ),
    );
    expect(current.remote.sync!.bindConversation).toHaveBeenCalledWith(STORED);
    const bindOrder = vi.mocked(current.remote.sync!.bindConversation).mock.invocationCallOrder[0];
    const promptIndex = client.request.mock.calls.findIndex(([method]) => method === "prompt.submit");
    expect(bindOrder).toBeLessThan(client.request.mock.invocationCallOrder[promptIndex]);
    rendered.rerender(
      <ConnectedTask view={{ ...current }} userId="user:owner" onPhase={() => undefined} onOpenConversation={open} />,
    );
    const card: AssistantApproval = {
      requestId: "r",
      kind: "change",
      storedSessionId: STORED,
      sessionId: LIVE,
      status: "awaiting",
      createdAt: 1,
      change: {
        id: "c",
        summary: "纪要",
        status: "awaiting",
        operations: [{ op: "write", path: "/nas/个人空间/会议.md" }],
      },
    };
    emit("anas.approval", card);
    await user.click(await screen.findByRole("button", { name: "去 VNAS 处理确认卡" }));
    expect(open).toHaveBeenCalledWith(STORED);
    expect(screen.queryByText("已确认保存")).not.toBeInTheDocument();
    emit("anas.approval", { ...card, status: "applied", change: { ...card.change!, status: "applied" } });
    expect(await screen.findByText("已确认保存")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "打开文件预览" }));
    expect(await screen.findByText("# 实际保存的纪要")).toBeVisible();
    expect(client.request.mock.calls.some(([method]) => method.includes("confirm"))).toBe(false);
    emit("message.start", {});
    await user.click(screen.getByRole("button", { name: "停止这次回答" }));
    expect(client.request).toHaveBeenCalledWith("session.interrupt", { session_id: LIVE });
  });
  it("does not submit a prompt when sharing the new conversation fails", async () => {
    fixtures();
    const user = userEvent.setup();
    const current = view();
    vi.mocked(current.remote.sync!.bindConversation).mockRejectedValue(new Error("lease lost"));
    render(<ConnectedTask view={current} userId="user:owner" onPhase={() => undefined} />);
    await user.click(screen.getByText("整理 NAS 里的会议记录"));
    await user.click(await screen.findByRole("button", { name: "会议.md" }));
    await user.click(screen.getByRole("button", { name: "整理这份记录" }));
    await screen.findByText("VNAS 暂时没有响应，请稍后再试");
    expect(client.request.mock.calls.some(([method]) => method === "prompt.submit")).toBe(false);
  });
});
