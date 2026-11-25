import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";

import { setSession } from "../api/client";
import type { AgentToken } from "../api.generated";
import { session } from "../test/fixtures";
import { agentTokenHandlers } from "../test/handlers/workspace";
import { server } from "../test/server";
import { AgentTokens } from "./AgentTokens";

const token = (id: string, name: string, lastUsedAt?: string): AgentToken => ({
  id,
  name,
  kind: "claude-code",
  scope: "workspace",
  createdAt: "2026-11-20T08:00:00Z",
  lastUsedAt,
});

let store: { tokens: AgentToken[] };
beforeEach(() => {
  setSession(session({}, { csrfToken: "csrf" }));
  store = { tokens: [] };
  server.use(...agentTokenHandlers(store));
});

describe("已连接的 agent", () => {
  it("is not there for an account that connected none", async () => {
    const { container } = render(<AgentTokens />);
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(container.textContent).toBe("");
  });

  it("lists the agents and cuts one off", async () => {
    const user = userEvent.setup();
    store.tokens = [token("tok_1", "办公电脑", new Date().toISOString()), token("tok_2", "笔记本")];
    render(<AgentTokens />);
    expect(await screen.findByText("Claude Code · 刚刚使用")).toBeTruthy();
    expect(screen.getByText("Claude Code · 还没用过")).toBeTruthy();
    await user.click(screen.getAllByRole("button", { name: "撤销" })[0]);
    const dialog = await screen.findByRole("dialog", { name: "撤销“办公电脑”？" });
    await user.click(within(dialog).getByRole("button", { name: "撤销" }));
    await waitFor(() => expect(store.tokens.map((item) => item.id)).toEqual(["tok_2"]));
    expect(screen.queryByText("办公电脑")).toBeNull();
  });
});
