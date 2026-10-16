import { TextMessagePartProvider } from "@assistant-ui/react";
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { MarkdownText } from "./AssistantPanel";

const reply = [
  "改好了，内容是：",
  "",
  "```markdown",
  "# 计划",
  "- 交电费",
  "```",
  "",
  "| 文件 | 大小 |",
  "| --- | --- |",
  "| 计划.md | 1 KiB |",
].join("\n");

function renderReply(text: string) {
  return render(<TextMessagePartProvider text={text}><MarkdownText /></TextMessagePartProvider>);
}

describe("an answer's Markdown", () => {
  afterEach(() => vi.restoreAllMocks());

  it("gives a code block the window's own header instead of Streamdown's buttons", async () => {
    const { container } = renderReply(reply);
    expect(await screen.findByText("markdown")).toBeTruthy();
    expect(screen.getByRole("button", { name: "复制代码" })).toBeTruthy();
    expect(container.querySelector("pre")?.textContent).toContain("- 交电费");
    // Streamdown styles its controls with Tailwind, which A-NAS does not load.
    for (const control of ["code-block-copy-button", "code-block-download-button", "table-fullscreen"]) {
      expect(container.querySelector(`[data-streamdown="${control}"]`)).toBeNull();
    }
    expect(container.querySelectorAll("button")).toHaveLength(1);
  });

  it("copies the code without the Clipboard API, which plain HTTP lacks", async () => {
    // jsdom has no execCommand; the browser's copies the selected field.
    const copied: (string | undefined)[] = [];
    Object.defineProperty(document, "execCommand", { configurable: true, value: vi.fn(() => {
      copied.push(document.querySelector("textarea")?.value);
      return true;
    }) });
    renderReply(reply);
    fireEvent.click(await screen.findByRole("button", { name: "复制代码" }));
    expect(copied).toEqual(["# 计划\n- 交电费"]);
    expect(screen.getByRole("button", { name: "已复制" })).toBeTruthy();
  });
});
