import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { Approval } from "./assistantApi";
import { CommandCard, QuestionCard } from "./AssistantPanel";

const base = { requestId: "req-3", sessionId: "0a1b2c3d", storedSessionId: "20261015_120000_abcdef", createdAt: 1 };

describe("cards for what the assistant asks", () => {
  it("shows a dangerous command and lets the member allow it once or for the conversation", () => {
    const onDecide = vi.fn(() => Promise.resolve());
    const approval: Approval = {
      ...base,
      kind: "command",
      status: "awaiting",
      command: { text: "rm -rf /nas/助手/旧草稿", description: "recursive delete", forSession: true },
    };
    render(<CommandCard approval={approval} onDecide={onDecide} />);
    expect(screen.getByText("VNAS 想运行一条命令：会删除整个文件夹")).toBeTruthy();
    expect(screen.getByText("rm -rf /nas/助手/旧草稿")).toBeTruthy();
    expect(screen.getByText(/个人空间与共享空间不会被改动/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "本对话中都允许" }));
    expect(onDecide).toHaveBeenCalledWith(true, true);
  });

  it("shows a script without Hermes's wrapper, and no conversation-wide choice when Hermes offers none", () => {
    const onDecide = vi.fn(() => Promise.resolve());
    const approval: Approval = {
      ...base,
      kind: "command",
      status: "awaiting",
      command: { text: "execute_code <<'PY'\nprint(1)\nPY", script: true },
    };
    render(<CommandCard approval={approval} onDecide={onDecide} />);
    expect(screen.getByText("VNAS 想运行一段代码")).toBeTruthy();
    expect(screen.getByText("print(1)")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "本对话中都允许" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "拒绝" }));
    expect(onDecide).toHaveBeenCalledWith(false, false);
  });

  it("collects an answer from a choice and from what the member writes", () => {
    const onAnswer = vi.fn(() => Promise.resolve());
    const approval: Approval = {
      ...base,
      requestId: "srq-00000000000b",
      kind: "question",
      status: "awaiting",
      questions: [
        { id: "q1", text: "放到哪个文件夹？", choices: ["照片 (Recommended)", "文档"] },
        { id: "q2", text: "改个名字吗？" },
      ],
    };
    render(<QuestionCard approval={approval} onAnswer={onAnswer} onSkip={() => Promise.resolve()} />);
    const send = screen.getByRole("button", { name: "回答" }) as HTMLButtonElement;
    expect(send.disabled).toBe(true);
    // Hermes's own mark shows as a badge, not in the choice.
    expect(screen.queryByText(/Recommended/)).toBeNull();
    expect(screen.getByText("推荐")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /^照片/ }));
    fireEvent.change(screen.getByLabelText("回答：改个名字吗？"), { target: { value: "爱弥斯头像.png" } });
    fireEvent.click(send);
    expect(onAnswer).toHaveBeenCalledWith({ q1: "照片", q2: "爱弥斯头像.png" });
  });

  it("answers as Hermes Desktop does: a choice or the text, and a list for several choices", () => {
    const onAnswer = vi.fn(() => Promise.resolve());
    const approval: Approval = {
      ...base,
      requestId: "srq-00000000000c",
      kind: "question",
      status: "awaiting",
      questions: [
        { id: "q1", text: "放到哪里？", choices: ["照片", "文档"] },
        { id: "q2", text: "哪些？", choices: ["猫", "狗"], multiSelect: true },
      ],
    };
    render(<QuestionCard approval={approval} onAnswer={onAnswer} onSkip={() => Promise.resolve()} />);
    fireEvent.click(screen.getByRole("button", { name: "照片" }));
    fireEvent.change(screen.getByLabelText("回答：放到哪里？"), { target: { value: "桌面" } });
    fireEvent.click(screen.getByRole("button", { name: "猫" }));
    fireEvent.click(screen.getByRole("button", { name: "狗" }));
    fireEvent.change(screen.getByLabelText("回答：哪些？"), { target: { value: "兔子" } });
    expect(screen.getByRole("button", { name: "照片" }).getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(screen.getByRole("button", { name: "回答" }));
    expect(onAnswer).toHaveBeenCalledWith({ q1: "桌面", q2: JSON.stringify(["猫", "狗", "兔子"]) });
  });
});
