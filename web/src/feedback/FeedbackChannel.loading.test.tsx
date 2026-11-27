import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { feedbackHandlers, feedbackItem } from "../test/handlers/feedback";
import { formOf, http, server } from "../test/server";

// The panel's code loads the first time it opens (FeedbackChannel.tsx). Each
// test stands in for that download and imports the channel afresh, since
// React keeps a loaded or failed panel for as long as the page lives.
async function channelWithPanel(load: (original: () => Promise<unknown>) => Promise<unknown>) {
  vi.doMock("./FeedbackPanel", load);
  vi.resetModules();
  return (await import("./FeedbackChannel")).FeedbackChannel;
}

const heic = feedbackItem("feedback:heic", { text: "希望相册支持 HEIC", createdAt: "2026-10-12T07:00:00Z" });

afterEach(() => {
  vi.doUnmock("./FeedbackPanel");
  vi.resetModules();
  vi.restoreAllMocks();
});

describe("问题反馈的面板按需加载", () => {
  it("downloads the panel when it first opens, keeps the button busy meanwhile, then sends as before", async () => {
    let arrive!: () => void;
    const download = new Promise<void>((resolve) => (arrive = resolve));
    let requested = 0;
    const FeedbackChannel = await channelWithPanel(async (original) => {
      requested += 1;
      await download;
      return original();
    });
    const created = feedbackItem("feedback:new", { text: "设置页面错位", createdAt: "2026-10-12T09:00:00Z" });
    const posts: Request[] = [];
    let items = [heic];
    server.use(
      ...feedbackHandlers(() => items),
      http.post("/api/v1/feedback", ({ request, response }) => {
        posts.push(request);
        items = [created, ...items];
        return response(201).json(created);
      }),
    );
    const user = userEvent.setup();
    render(<FeedbackChannel />);

    // The button shows with its count before anything asks for the panel.
    const button = await screen.findByRole("button", { name: "问题反馈，1 条待处理" });
    expect(requested).toBe(0);
    await user.click(button);
    expect(requested).toBe(1);
    expect(screen.getByRole("button", { name: "问题反馈，1 条待处理" }).getAttribute("aria-busy")).toBe("true");
    expect(screen.queryByRole("dialog", { name: "问题反馈" })).toBeNull();

    arrive();
    const panel = await screen.findByRole("dialog", { name: "问题反馈" });
    expect(screen.queryByRole("button", { name: "问题反馈，1 条待处理" })).toBeNull();
    const composer = within(panel).getByLabelText("新反馈内容");
    expect(document.activeElement).toBe(composer);
    expect(await within(panel).findByText(heic.text)).toBeTruthy();
    await user.type(composer, "设置页面错位");
    await user.click(within(panel).getByRole("button", { name: "提交" }));
    expect(await within(panel).findByText("设置页面错位", { ignore: "textarea" })).toBeTruthy();
    expect(posts).toHaveLength(1);
    expect(formOf(posts[0]).get("text")).toBe("设置页面错位");

    // The count follows what the panel did; the panel comes back at once.
    await user.click(within(panel).getByRole("button", { name: "关闭问题反馈" }));
    const again = screen.getByRole("button", { name: "问题反馈，2 条待处理" });
    expect(document.activeElement).toBe(again);
    await user.click(again);
    expect(screen.getByRole("dialog", { name: "问题反馈" })).toBeTruthy();
    expect(requested).toBe(1);
  });

  it("says where the panel would be that it did not load, until the page reloads", async () => {
    const FeedbackChannel = await channelWithPanel(async () => {
      throw new TypeError("Failed to fetch dynamically imported module");
    });
    server.use(...feedbackHandlers(() => [heic]));
    const logged = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const user = userEvent.setup();
    render(<FeedbackChannel />);

    await user.click(await screen.findByRole("button", { name: "问题反馈，1 条待处理" }));
    const missing = await screen.findByRole("dialog", { name: "问题反馈" });
    expect(within(missing).getByRole("heading", { name: "问题反馈没有加载出来" })).toBeTruthy();
    expect(within(missing).getByRole("button", { name: "重新载入页面" })).toBeTruthy();
    expect(document.activeElement).toBe(within(missing).getByRole("button", { name: "关闭" }));
    expect(logged).toHaveBeenCalledWith("A-NAS feedback panel did not load", expect.anything());

    await user.click(within(missing).getByRole("button", { name: "关闭" }));
    expect(screen.queryByRole("dialog", { name: "问题反馈" })).toBeNull();
    const button = screen.getByRole("button", { name: "问题反馈，1 条待处理" });
    expect(document.activeElement).toBe(button);
    await user.click(button);
    expect(within(screen.getByRole("dialog", { name: "问题反馈" })).getByText("问题反馈没有加载出来")).toBeTruthy();
  });
});
