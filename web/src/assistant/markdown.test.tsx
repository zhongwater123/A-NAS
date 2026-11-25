import { TextMessagePartProvider } from "@assistant-ui/react";
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { APIError } from "../api/client";
import { MarkdownText } from "./AssistantPanel";
import { getTextHandle } from "./api";
import { forgetTextHandles } from "./TextHandle";
import { linkTextHandles } from "./textHandles";

vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), getTextHandle: vi.fn() }));

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
  return render(
    <TextMessagePartProvider text={text}>
      <MarkdownText />
    </TextMessagePartProvider>,
  );
}

describe("an answer's Markdown", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    forgetTextHandles();
  });

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
    Object.defineProperty(document, "execCommand", {
      configurable: true,
      value: vi.fn(() => {
        copied.push(document.querySelector("textarea")?.value);
        return true;
      }),
    });
    renderReply(reply);
    fireEvent.click(await screen.findByRole("button", { name: "复制代码" }));
    expect(copied).toEqual(["# 计划\n- 交电费"]);
    expect(screen.getByRole("button", { name: "已复制" })).toBeTruthy();
  });

  it("shows the number a handle stands for, asked for by the window and not by the assistant", async () => {
    const asked = vi.mocked(getTextHandle).mockImplementation(async (handle) => {
      if (handle !== "1a2b3c") throw new APIError(404, "not_found", "photo library item was not found");
      return {
        label: "身份证号",
        value: "440306199001011236",
        unchecked: true,
        assetId: "photo:p1",
        assetName: "IMG_1.jpg",
      };
    });
    const copied: (string | undefined)[] = [];
    Object.defineProperty(document, "execCommand", {
      configurable: true,
      value: vi.fn(() => {
        copied.push(document.querySelector("textarea")?.value);
        return true;
      }),
    });
    const { container } = renderReply(
      linkTextHandles("你的身份证号是 ⟦身份证号·1a2b3c⟧，另一张的是 ⟦身份证号·ffffff⟧，再说一遍 ⟦身份证号·1a2b3c⟧。"),
    );

    // The number, marked as not checked by anyone yet, never the handle.
    expect(await screen.findAllByText("440306199001011236")).toHaveLength(2);
    expect(screen.getAllByText("待核对")).toHaveLength(2);
    expect(container.textContent).not.toContain("⟦");
    expect(container.querySelector("a")).toBeNull();
    // A handle that stands for nothing any more says so, and tells no number.
    expect(await screen.findByText("身份证号（无法显示）")).toBeTruthy();
    // One request for each handle, however often the answer names it.
    expect(asked.mock.calls.map(([handle]) => handle).sort()).toEqual(["1a2b3c", "ffffff"]);

    fireEvent.click(screen.getAllByRole("button", { name: "复制身份证号" })[0]);
    expect(copied).toEqual(["440306199001011236"]);
  });

  it("never loads a picture from outside the device, only links to it", async () => {
    const { container } = renderReply(
      '![账单](http://203.0.113.9/p.png?d=secret) <img src="http://203.0.113.9/q.png"> ![图](/api/v1/photos/assets/p1/thumbnail)',
    );
    const outside = await screen.findByRole("link", { name: "账单" });
    expect(outside.getAttribute("href")).toBe("http://203.0.113.9/p.png?d=secret");
    expect(screen.getByRole("link", { name: "外部图片" })).toBeTruthy();
    const images = [...container.querySelectorAll("img")];
    expect(images.map((image) => image.getAttribute("src"))).toEqual(["/api/v1/photos/assets/p1/thumbnail"]);
  });
});
