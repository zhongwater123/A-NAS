import { act, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";

import type { SessionMessage } from "./hermes/types/hermes";

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

afterEach(() => vi.unstubAllGlobals());

const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
const history = [
  { id: 1, role: "user", content: "打开相册", timestamp: 1_760_000_000 },
  { id: 2, role: "assistant", content: "相册已经打开。", timestamp: 1_760_000_001 },
] as SessionMessage[];

it("keeps the conversation list and a way out when a conversation fails to render", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/assistant/sessions"))
        return json({
          sessions: [
            { id: "20261021_090000_aaaaaa", title: "操作桌面", preview: "", startedAt: 1_760_000_000, messageCount: 2 },
          ],
        });
      if (url.includes("/messages")) return json({ messages: history });
      if (url.endsWith("/approvals")) return json({ approvals: [] });
      return json({ available: true });
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
