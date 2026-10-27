import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import { forgetPeople, rememberPerson } from "../people/people";
import { feedbackComment, feedbackHandlers, feedbackItem } from "../test/handlers/feedback";
import { apiError, formOf, http, server } from "../test/server";
import type { FeedbackItem } from "./api";
import { FeedbackChannel } from "./FeedbackChannel";
import { describeTime } from "./time";

const capture = vi.hoisted(() => ({ result: undefined as Blob | undefined }));
vi.mock("./capture", async (original) => ({
  ...(await original<typeof import("./capture")>()),
  captureDesktop: vi.fn(async () => {
    if (!capture.result) throw new Error("capture failed");
    return capture.result;
  }),
}));

const upload = feedbackItem("feedback:upload", {
  text: "上传 2 GB 视频时进度停在 99%",
  images: [{ id: "feedback-image:shot", contentType: "image/png", sizeBytes: 2048 }],
  createdBy: "lin",
  createdAt: "2026-10-12T08:00:00Z",
});
const heic = feedbackItem("feedback:heic", {
  text: "希望相册支持 HEIC",
  version: 3,
  createdBy: "owner",
  createdAt: "2026-10-12T07:00:00Z",
});
const reply = feedbackComment("feedback-comment:reply", {
  text: "我这边也能复现",
  images: [{ id: "feedback-image:reply", contentType: "image/png", sizeBytes: 1024 }],
  createdBy: "lin",
  createdAt: "2026-10-12T06:30:00Z",
});
const subtitles = feedbackItem("feedback:subtitles", {
  text: "全屏字幕偏小",
  createdBy: "owner",
  createdAt: "2026-10-11T07:00:00Z",
  resolved: true,
  resolvedBy: "lin",
  resolvedAt: "2026-10-12T06:00:00Z",
  comments: [reply],
});

// The text of an item or a comment as the list shows it, not the draft in an
// editor's text box.
const shown = { ignore: "textarea" };

// Whether a line runs through the whole text: feedback.css draws it as the
// text's background, which grows across it when the item is handled.
const struck = (text: HTMLElement) => getComputedStyle(text).backgroundSize.startsWith("100%");

const png = (name = "shot.png") => new File([new Uint8Array([137, 80, 78, 71])], name, { type: "image/png" });

beforeEach(() => {
  localStorage.clear();
  setSession({
    csrfToken: "csrf-test",
    expiresAt: "2026-10-12T20:00:00Z",
    user: {
      id: "user:owner",
      username: "owner",
      role: "admin",
      status: "active",
      createdAt: "2026-10-01T00:00:00Z",
      mustChangePassword: false,
    },
  });
  let next = 0;
  vi.stubGlobal(
    "URL",
    Object.assign(URL, { createObjectURL: vi.fn(() => `blob:preview-${++next}`), revokeObjectURL: vi.fn() }),
  );
  capture.result = undefined;
});
afterEach(() => {
  vi.unstubAllGlobals();
  setSession();
  forgetPeople();
});

async function openPanel(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: /^问题反馈(，\d+ 条待处理)?$/ }));
  return screen.getByRole("dialog", { name: "问题反馈" });
}

describe("问题反馈", () => {
  it("names authors by nickname next to their avatar", async () => {
    rememberPerson({ id: "user:lin", username: "lin", nickname: "林老师", avatarVersion: "0123abcd" });
    server.use(...feedbackHandlers(() => [upload, heic]));
    const user = userEvent.setup();
    render(<FeedbackChannel />);
    const panel = await openPanel(user);
    const card = (await within(panel).findByText(upload.text)).closest("li") as HTMLElement;
    expect(within(card).getByText("林老师")).toBeTruthy();
    expect(within(card).queryByText("lin")).toBeNull();
    // The avatar comes right before the name; it is decorative, the name is read.
    const avatar = (name: HTMLElement) => name.previousElementSibling;
    expect(avatar(within(card).getByText("林老师"))?.querySelector("img")?.getAttribute("src")).toBe(
      "/api/v1/users/user%3Alin/avatar?v=0123abcd",
    );
    // An author without a profile keeps the account name and its initial.
    const other = within(panel).getByText(heic.text).closest("li") as HTMLElement;
    expect(within(other).getByText("owner")).toBeTruthy();
    expect(avatar(within(other).getByText("owner"))?.textContent).toBe("O");
  });

  it("shows the open count on the edge button and lists open items first", async () => {
    server.use(...feedbackHandlers(() => [upload, heic, subtitles]));
    const focus = vi.spyOn(HTMLElement.prototype, "focus");
    const user = userEvent.setup();
    render(<FeedbackChannel />);
    const panel = await openPanel(user);
    // A window wider than the screen makes the desktop scrollable; focusing
    // the composer must not scroll it.
    expect(document.activeElement).toBe(within(panel).getByLabelText("新反馈内容"));
    expect(focus).toHaveBeenCalledWith({ preventScroll: true });
    focus.mockRestore();
    expect(within(panel).getByRole("tab", { name: "待处理2" }).getAttribute("aria-selected")).toBe("true");
    const list = await within(panel).findByRole("list", { name: "反馈清单" });
    expect(
      within(list)
        .getAllByRole("checkbox")
        .map((box) => box.getAttribute("aria-checked")),
    ).toEqual(["false", "false"]);
    expect(within(list).getByRole("button", { name: "查看附图 1" }).querySelector("img")?.getAttribute("src")).toBe(
      "/api/v1/feedback/feedback%3Aupload/images/feedback-image%3Ashot",
    );

    await user.click(within(panel).getByRole("tab", { name: "已处理1" }));
    const handled = within(panel).getByText("全屏字幕偏小");
    expect(struck(handled)).toBe(true);
    expect(within(handled.closest("li")!).getByText("（已处理）")).toBeTruthy();
    expect(within(panel).getByText("lin 已处理")).toBeTruthy();
    await user.click(within(panel).getByRole("tab", { name: "全部3" }));
    expect(
      within(panel)
        .getAllByRole("checkbox")
        .map((box) => box.getAttribute("aria-checked")),
    ).toEqual(["false", "false", "true"]);

    await user.click(within(panel).getByRole("button", { name: "关闭问题反馈" }));
    expect(screen.queryByRole("dialog", { name: "问题反馈" })).toBeNull();
    expect(document.activeElement?.getAttribute("aria-label")).toBe("问题反馈，2 条待处理");
  });

  it("sends text with pasted and captured images and keeps the panel's draft until it is sent", async () => {
    const created: FeedbackItem = {
      ...heic,
      id: "feedback:new",
      text: "设置页面错位",
      images: [],
      createdAt: "2026-10-12T09:00:00Z",
    };
    const posts: Request[] = [];
    server.use(
      ...feedbackHandlers(() => [heic]),
      http.post("/api/v1/feedback", ({ request, response }) => {
        posts.push(request);
        return response(201).json(created);
      }),
    );
    capture.result = new Blob(["webp"], { type: "image/webp" });
    const user = userEvent.setup();
    render(<FeedbackChannel />);
    let panel = await openPanel(user);
    const composer = within(panel).getByRole("form", { name: "新反馈" });
    await user.type(within(composer).getByLabelText("新反馈内容"), "设置页面错位");
    fireEvent.paste(within(composer).getByLabelText("新反馈内容"), {
      clipboardData: { files: [png()], types: ["Files"] },
    });
    // Only images are accepted.
    fireEvent.paste(within(composer).getByLabelText("新反馈内容"), {
      clipboardData: { files: [new File(["<svg/>"], "x.svg", { type: "image/svg+xml" })], types: ["Files"] },
    });
    expect(within(composer).getByRole("alert").textContent).toBe("只支持 PNG、JPEG、GIF 或 WebP 图片");
    await user.click(within(composer).getByRole("button", { name: "截取界面" }));
    expect(await within(composer).findByRole("button", { name: "查看界面截图" })).toBeTruthy();
    expect(within(composer).getByText("2/6")).toBeTruthy();

    // Closing the panel keeps the draft.
    await user.click(within(panel).getByRole("button", { name: "关闭问题反馈" }));
    panel = await openPanel(user);
    expect((within(panel).getByLabelText("新反馈内容") as HTMLTextAreaElement).value).toBe("设置页面错位");

    await user.keyboard("{Control>}{Enter}{/Control}");
    await within(panel).findByText("设置页面错位", shown);
    expect(posts).toHaveLength(1);
    const body = formOf(posts[0]);
    expect(body.get("text")).toBe("设置页面错位");
    expect(body.getAll("image").map((image) => (image as File).type)).toEqual(["image/png", "image/webp"]);
    expect(posts[0].headers.get("X-CSRF-Token")).toBe("csrf-test");
    expect((within(panel).getByLabelText("新反馈内容") as HTMLTextAreaElement).value).toBe("");
    expect(within(panel).queryByRole("list", { name: "附图" })).toBeNull();
  });

  it("strikes through a handled item and keeps it in place until the view changes", async () => {
    const patches: Array<{ id: string; body: unknown }> = [];
    server.use(
      ...feedbackHandlers(() => [upload, heic]),
      http.patch("/api/v1/feedback/{feedbackId}", async ({ params, request, response }) => {
        patches.push({ id: params.feedbackId, body: await request.json() });
        return response(200).json({ ...heic, resolved: true, resolvedBy: "owner", resolvedAt: "2026-10-12T09:00:00Z" });
      }),
    );
    const user = userEvent.setup();
    render(<FeedbackChannel />);
    const panel = await openPanel(user);
    const text = await within(panel).findByText("希望相册支持 HEIC");
    const item = text.closest("li")!;
    expect(struck(text)).toBe(false);
    await user.click(within(item).getByRole("checkbox", { name: "标记为已处理" }));
    expect(struck(text)).toBe(true);
    expect(within(item).getByRole("checkbox", { name: "标记为待处理" }).getAttribute("aria-checked")).toBe("true");
    expect(await within(item).findByText("owner 已处理")).toBeTruthy();
    expect(patches).toEqual([{ id: heic.id, body: { resolved: true } }]);
    expect(within(panel).getByRole("tab", { name: "待处理1" })).toBeTruthy();

    await user.click(within(panel).getByRole("tab", { name: "已处理1" }));
    await user.click(within(panel).getByRole("tab", { name: "待处理1" }));
    expect(within(panel).queryByText("希望相册支持 HEIC")).toBeNull();
  });

  it("edits from the version it read and saves over a concurrent edit after showing it", async () => {
    let latest = { ...upload };
    const edits: Array<{ id: string; form: FormData }> = [];
    server.use(
      ...feedbackHandlers(() => [latest]),
      http.put("/api/v1/feedback/{feedbackId}", ({ params, request, response }) => {
        edits.push({ id: params.feedbackId, form: formOf(request) });
        if (edits.length === 1) {
          latest = {
            ...upload,
            text: "进度停在 99%，刷新后才完成",
            version: 2,
            updatedBy: "lin",
            updatedAt: "2026-10-12T09:00:00Z",
          };
          return response(409).json(apiError("feedback_changed", "feedback changed since it was read"));
        }
        return response(200).json({ ...latest, text: "我的版本", images: [], version: 3, updatedBy: "owner" });
      }),
    );
    const user = userEvent.setup();
    render(<FeedbackChannel />);
    const panel = await openPanel(user);
    const item = (await within(panel).findByText(upload.text)).closest("li")!;
    await user.click(within(item).getByRole("button", { name: "编辑反馈" }));
    const editor = within(panel).getByRole("form", { name: "编辑反馈" });
    await user.click(within(editor).getByRole("button", { name: "移除附图 1" }));
    const field = within(editor).getByLabelText("编辑反馈内容");
    await user.clear(field);
    await user.type(field, "我的版本");
    await user.click(within(editor).getByRole("button", { name: "保存" }));

    expect(await within(editor).findByText("lin 刚刚修改了这条反馈")).toBeTruthy();
    expect(within(editor).getByText("进度停在 99%，刷新后才完成")).toBeTruthy();
    expect((field as HTMLTextAreaElement).value).toBe("我的版本");
    await user.click(within(editor).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(within(panel).getByText("我的版本", shown)).toBeTruthy());

    expect(edits.map(({ id, form }) => [id, form.get("version"), form.get("text"), form.getAll("keep")])).toEqual([
      [upload.id, "1", "我的版本", []],
      [upload.id, "2", "我的版本", []],
    ]);
    expect(within(panel).getByText("已编辑")).toBeTruthy();
  });

  it("deletes an item only after confirming", async () => {
    const deleted: string[] = [];
    server.use(
      ...feedbackHandlers(() => [upload, heic]),
      http.delete("/api/v1/feedback/{feedbackId}", ({ params, response }) => {
        deleted.push(params.feedbackId);
        return response(204).empty();
      }),
    );
    const user = userEvent.setup();
    render(<FeedbackChannel />);
    const panel = await openPanel(user);
    const item = (await within(panel).findByText("希望相册支持 HEIC")).closest("li")!;
    await user.click(within(item).getByRole("button", { name: "删除反馈" }));
    const confirm = within(item).getByRole("group", { name: "确认删除" });
    await user.click(within(confirm).getByRole("button", { name: "取消" }));
    expect(deleted).toEqual([]);
    await user.click(within(item).getByRole("button", { name: "删除反馈" }));
    await user.click(within(item).getByRole("button", { name: "删除" }));
    await waitFor(() => expect(within(panel).queryByText("希望相册支持 HEIC")).toBeNull());
    expect(deleted).toEqual([heic.id]);
    expect(within(panel).getByRole("tab", { name: "待处理1" })).toBeTruthy();
  });

  it("opens attached images in a viewer that Escape closes without closing the panel", async () => {
    server.use(...feedbackHandlers(() => [upload]));
    const user = userEvent.setup();
    render(<FeedbackChannel />);
    const panel = await openPanel(user);
    await user.click(await within(panel).findByRole("button", { name: "查看附图 1" }));
    const viewer = screen.getByRole("dialog", { name: "查看图片" });
    expect(within(viewer).getByRole("img").getAttribute("src")).toContain("feedback-image%3Ashot");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "查看图片" })).toBeNull();
    expect(screen.getByRole("dialog", { name: "问题反馈" })).toBeTruthy();
  });
});

describe("问题反馈的评论", () => {
  it("opens a thread from the comment count and replies with a pasted image", async () => {
    const answered: FeedbackItem = {
      ...heic,
      comments: [
        feedbackComment("feedback-comment:new", {
          text: "1.0.2 会支持",
          images: [{ id: "feedback-image:new", contentType: "image/png", sizeBytes: 4 }],
          createdBy: "owner",
          createdAt: "2026-10-12T09:00:00Z",
        }),
      ],
    };
    const posts: Array<{ id: string; request: Request }> = [];
    server.use(
      ...feedbackHandlers(() => [heic, subtitles]),
      http.post("/api/v1/feedback/{feedbackId}/comments", ({ params, request, response }) => {
        posts.push({ id: params.feedbackId, request });
        return response(201).json(answered);
      }),
    );
    const user = userEvent.setup();
    render(<FeedbackChannel />);
    const panel = await openPanel(user);
    await user.click(within(panel).getByRole("tab", { name: "全部2" }));

    // An item with comments shows their count; its thread opens on demand.
    const handled = within(panel).getByText("全屏字幕偏小").closest("li")!;
    expect(within(handled).queryByRole("region", { name: "评论" })).toBeNull();
    await user.click(within(handled).getByRole("button", { name: "1 条评论" }));
    const thread = within(handled).getByRole("region", { name: "评论" });
    expect(within(thread).getByText("我这边也能复现")).toBeTruthy();
    expect(
      within(thread).getByRole("button", { name: "查看评论附图 1" }).querySelector("img")?.getAttribute("src"),
    ).toBe("/api/v1/feedback/feedback%3Asubtitles/images/feedback-image%3Areply");
    // The discussion is not struck through with the handled item: the line runs
    // through the item's own text only.
    const text = within(handled).getByText("全屏字幕偏小");
    expect(struck(text)).toBe(true);
    expect(text.contains(thread)).toBe(false);
    expect(struck(within(thread).getByText("我这边也能复现"))).toBe(false);

    // An empty thread opens straight into the comment box.
    const item = within(panel).getByText("希望相册支持 HEIC").closest("li")!;
    await user.click(within(item).getByRole("button", { name: "写评论" }));
    const box = within(item).getByLabelText("写评论内容");
    expect(document.activeElement).toBe(box);
    await user.type(box, "1.0.2 会支持");
    fireEvent.paste(box, { clipboardData: { files: [png()], types: ["Files"] } });
    // A comment holds at most three images.
    fireEvent.paste(box, { clipboardData: { files: [png("a.png"), png("b.png"), png("c.png")], types: ["Files"] } });
    expect(within(item).getByRole("alert").textContent).toBe("最多添加 3 张图片");
    expect(within(item).getByText("3/3")).toBeTruthy();
    await user.click(within(item).getByRole("button", { name: "评论" }));

    expect(await within(item).findByText("1.0.2 会支持", shown)).toBeTruthy();
    expect(within(item).getByRole("button", { name: "1 条评论" })).toBeTruthy();
    expect(posts.map(({ id }) => id)).toEqual([heic.id]);
    const body = formOf(posts[0].request);
    expect(body.get("text")).toBe("1.0.2 会支持");
    expect(body.getAll("image")).toHaveLength(3);
    expect(posts[0].request.headers.get("X-CSRF-Token")).toBe("csrf-test");
    expect((within(item).getByLabelText("写评论内容") as HTMLTextAreaElement).value).toBe("");
  });

  it("lets anyone edit a comment, saving over a concurrent edit after showing it, and delete one after confirming", async () => {
    let latest: FeedbackItem = { ...subtitles };
    const edits: Array<{ ids: string[]; form: FormData }> = [];
    const deleted: string[][] = [];
    server.use(
      ...feedbackHandlers(() => [latest]),
      http.put("/api/v1/feedback/{feedbackId}/comments/{commentId}", ({ params, request, response }) => {
        edits.push({ ids: [params.feedbackId, params.commentId], form: formOf(request) });
        if (edits.length === 1) {
          latest = {
            ...subtitles,
            comments: [
              { ...reply, text: "只在全屏时出现", version: 2, updatedBy: "lin", updatedAt: "2026-10-12T09:00:00Z" },
            ],
          };
          return response(409).json(apiError("comment_changed", "feedback comment changed since it was read"));
        }
        return response(200).json({
          ...latest,
          comments: [
            {
              ...reply,
              text: "已在 1.0.2 修复",
              images: [],
              version: 3,
              updatedBy: "owner",
              updatedAt: "2026-10-12T09:01:00Z",
            },
          ],
        });
      }),
      http.delete("/api/v1/feedback/{feedbackId}/comments/{commentId}", ({ params, response }) => {
        deleted.push([params.feedbackId, params.commentId]);
        return response(200).json({ ...latest, comments: [] });
      }),
    );
    const user = userEvent.setup();
    render(<FeedbackChannel />);
    const panel = await openPanel(user);
    await user.click(within(panel).getByRole("tab", { name: "已处理1" }));
    const item = within(panel).getByText("全屏字幕偏小").closest("li")!;
    await user.click(within(item).getByRole("button", { name: "1 条评论" }));

    // The signed-in owner edits lin's comment and drops its image.
    await user.click(within(item).getByRole("button", { name: "编辑评论" }));
    const editor = within(item).getByRole("form", { name: "编辑评论" });
    await user.click(within(editor).getByRole("button", { name: "移除附图 1" }));
    const field = within(editor).getByLabelText("编辑评论内容");
    await user.clear(field);
    await user.type(field, "已在 1.0.2 修复");
    await user.click(within(editor).getByRole("button", { name: "保存" }));
    expect(await within(editor).findByText("lin 刚刚修改了这条评论")).toBeTruthy();
    expect(within(editor).getByText("只在全屏时出现")).toBeTruthy();
    await user.click(within(editor).getByRole("button", { name: "保存" }));
    expect(await within(item).findByText("已在 1.0.2 修复", shown)).toBeTruthy();
    const comment = [subtitles.id, reply.id];
    expect(edits.map(({ ids, form }) => [...ids, form.get("version"), form.get("text"), form.getAll("keep")])).toEqual([
      [...comment, "1", "已在 1.0.2 修复", []],
      [...comment, "2", "已在 1.0.2 修复", []],
    ]);
    // The comment, not the item, shows that it was edited.
    expect(within(within(item).getByRole("region", { name: "评论" })).getByText("已编辑")).toBeTruthy();

    await user.click(within(item).getByRole("button", { name: "删除评论" }));
    const confirm = within(item).getByRole("group", { name: "确认删除评论" });
    expect(deleted).toEqual([]);
    await user.click(within(confirm).getByRole("button", { name: "删除" }));
    await waitFor(() => expect(within(item).queryByText("已在 1.0.2 修复")).toBeNull());
    expect(deleted).toEqual([comment]);
    expect(within(item).getByRole("button", { name: "写评论" })).toBeTruthy();
  });
});

describe("describeTime", () => {
  const now = new Date(2026, 9, 12, 15, 30);
  it("reads like a note list", () => {
    expect(describeTime(new Date(2026, 9, 12, 15, 29, 30).toISOString(), now)).toBe("刚刚");
    expect(describeTime(new Date(2026, 9, 12, 15, 5).toISOString(), now)).toBe("25 分钟前");
    expect(describeTime(new Date(2026, 9, 12, 9, 7).toISOString(), now)).toBe("今天 09:07");
    expect(describeTime(new Date(2026, 9, 11, 22, 0).toISOString(), now)).toBe("昨天 22:00");
    expect(describeTime(new Date(2026, 2, 1, 8, 0).toISOString(), now)).toBe("3月1日");
    expect(describeTime(new Date(2025, 11, 31, 8, 0).toISOString(), now)).toBe("2025年12月31日");
  });
});
