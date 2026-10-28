import { act, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";

import type { AssistantMessage } from "../api.generated";
import { assistantHandlers, assistantSession } from "../test/handlers/assistant";
import { server } from "../test/server";

// An answer that cannot be rendered, whatever the reason.
vi.mock("@assistant-ui/react-streamdown", async (original) => ({
  ...(await original<typeof import("@assistant-ui/react-streamdown")>()),
  StreamdownTextPrimitive: () => {
    throw new Error("this answer cannot be rendered");
  },
}));

const client = {
  request: vi.fn(async (method: string): Promise<unknown> => {
    if (method === "session.resume") return { session_id: "live0001", running: false };
    if (method === "session.active_list") return { sessions: [] };
    return {};
  }),
  onEvent: () => () => undefined,
  on: () => () => undefined,
};
vi.mock("./useAssistantGateway", () => ({
  useAssistantGateway: () => ({ client, generation: 1, ready: true, problem: "" }),
}));

const { AssistantPanel } = await import("./AssistantPanel");

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;
Element.prototype.scrollTo ??= function scrollTo() {};

const history: AssistantMessage[] = [
  { id: 1, role: "user", content: "打开相册", timestamp: 1_760_000_000 },
  { id: 2, role: "assistant", content: "相册已经打开。", timestamp: 1_760_000_001 },
];

it("keeps the conversation list and a way out when a conversation fails to render", async () => {
  server.use(
    ...assistantHandlers({
      sessions: [assistantSession("20261021_090000_aaaaaa", { title: "操作桌面", messageCount: 2 })],
      messages: history,
    }),
  );
  const spy = vi.spyOn(console, "error").mockImplementation(() => undefined);
  try {
    render(<AssistantPanel username="admin" />);
    const row = await screen.findByText("操作桌面");
    await act(async () => {
      row.click();
    });
    expect(await screen.findByText("这个对话暂时无法显示")).toBeTruthy();
    // The window's own boundary did not take over: the list is still there, and
    // the member starts over without reloading WebOS.
    expect(screen.queryByText("这个页面暂时无法显示")).toBeNull();
    expect(screen.getByRole("button", { name: /操作桌面/ })).toBeTruthy();
    await act(async () => {
      screen.getByRole("button", { name: "开始新对话" }).click();
    });
    expect(await screen.findByText("你好，admin")).toBeTruthy();
  } finally {
    spy.mockRestore();
  }
});
