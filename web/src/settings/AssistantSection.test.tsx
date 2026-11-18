import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { setSession } from "../api/client";
import type { AssistantKeyRequest, AssistantKeyState } from "../api.generated";
import { session } from "../test/fixtures";
import { apiError, http, server } from "../test/server";
import { AssistantSection } from "./AssistantSection";

const testKey = "sk-0123456789abcdef0123456789abcdef";

// serve answers the key's page as the Host Agent would and records what was saved.
function serve(initial: AssistantKeyState) {
  let state = initial;
  const sent: { saved: AssistantKeyRequest[]; deleted: number } = { saved: [], deleted: 0 };
  server.use(
    http.get("/api/v1/assistant-key", ({ response }) => response(200).json(state)),
    http.put("/api/v1/assistant-key", async ({ request, response }) => {
      const body = await request.json();
      sent.saved.push(body);
      if (body.key.length < 16) return response(400).json(apiError("invalid_key"));
      state = { configured: true, gateway: "running" };
      return response(200).json(state);
    }),
    http.delete("/api/v1/assistant-key", ({ response }) => {
      sent.deleted++;
      state = { configured: false, gateway: "stopped" };
      return response(200).json(state);
    }),
  );
  return sent;
}

describe("Settings → VNAS", () => {
  beforeEach(() => setSession(session()));
  afterEach(() => setSession());

  it("saves the key only after the administrator agrees to send what VNAS handles to DeepSeek", async () => {
    const sent = serve({ configured: false, gateway: "stopped" });
    let changes = 0;
    const user = userEvent.setup();
    render(<AssistantSection onKeyChange={() => changes++} />);

    expect(await screen.findByText("未填写")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "填写" }));
    const dialog = await screen.findByRole("dialog", { name: "填写 DeepSeek 密钥" });
    expect(dialog).toHaveTextContent("都会发到 DeepSeek 的服务器处理");
    await user.type(screen.getByLabelText("API 密钥"), testKey);
    const save = screen.getByRole("button", { name: "保存" });
    expect(save).toBeDisabled();
    await user.click(screen.getByRole("checkbox", { name: "我已了解，并同意这些内容发往 DeepSeek" }));
    await user.click(save);

    expect(await screen.findByText("已保存，全家成员都能使用 VNAS")).toBeInTheDocument();
    expect(screen.getByText("已填写")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "替换" })).toBeInTheDocument();
    expect(screen.queryByText(testKey)).not.toBeInTheDocument();
    expect(sent.saved).toEqual([{ key: testKey, acknowledged: true }]);
    expect(changes).toBe(1);
  });

  it("says why a key was refused and keeps the dialog open", async () => {
    serve({ configured: false, gateway: "stopped" });
    const user = userEvent.setup();
    render(<AssistantSection />);

    await user.click(await screen.findByRole("button", { name: "填写" }));
    // The field asks for 16 characters; past the browser's own check the server still decides.
    const field = screen.getByLabelText("API 密钥");
    field.removeAttribute("minlength");
    await user.type(field, "sk-short");
    await user.click(screen.getByRole("checkbox", { name: "我已了解，并同意这些内容发往 DeepSeek" }));
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(await screen.findByText("这不是 DeepSeek 的 API 密钥，请检查后重新粘贴")).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "填写 DeepSeek 密钥" })).toBeInTheDocument();
  });

  it("deletes the key after confirming, which turns VNAS off for everyone", async () => {
    const sent = serve({ configured: true, gateway: "failed" });
    const user = userEvent.setup();
    render(<AssistantSection />);

    expect(await screen.findByText("模型网关出错")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "删除" }));
    const dialog = await screen.findByRole("dialog", { name: "删除 DeepSeek 密钥" });
    expect(dialog).toHaveTextContent("对话、记忆和定时任务会保留");
    await user.click(screen.getAllByRole("button", { name: "删除" }).at(-1)!);

    expect(await screen.findByText("已删除，VNAS 已关闭")).toBeInTheDocument();
    expect(screen.getByText("未填写")).toBeInTheDocument();
    expect(sent.deleted).toBe(1);
  });
});
