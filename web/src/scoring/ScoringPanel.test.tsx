import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import type { ScoringBatchInput } from "../api.generated";
import { session } from "../test/fixtures";
import {
  adminBatch,
  dimensionStats,
  dimensionsOf,
  raterBatch,
  raterEntry,
  raterHandlers,
  raterStats,
  scoringCatalogue,
  scoringMe,
} from "../test/handlers/scoring";
import { apiError, http, server } from "../test/server";
import { ScoringPanel } from "./ScoringPanel";

// jsdom loads no images; a decoded photo is what the stage waits for.
beforeEach(() => {
  setSession(session({ id: "user:alice", role: "member" }, { csrfToken: "csrf" }));
  HTMLImageElement.prototype.decode = () => Promise.resolve();
  window.localStorage.clear();
});
afterEach(() => {
  delete (HTMLImageElement.prototype as Partial<HTMLImageElement>).decode;
});

const shownPhoto = () => screen.findByRole("img", { name: "要打分的照片" });
// The stage's count sets its number apart from the words around it.
const counted = (text: string) => (_: string, element: Element | null) =>
  element?.matches("[aria-live]") === true && element.textContent === text;

// The window reads the account's roles again when it opens; administrators
// read the 打分项 a batch can choose.
function serveAccess(admin: boolean, rater: boolean) {
  server.use(
    http.get("/api/v1/modules", ({ response }) => response(200).json({ items: [{ id: "scoring" }] })),
    http.get("/api/v1/modules/scoring/me", ({ response }) => response(200).json(scoringMe(admin, rater))),
    http.get("/api/v1/modules/scoring/admin/dimensions", ({ response }) =>
      response(200).json({ items: scoringCatalogue }),
    ),
  );
}

describe("ScoringPanel for a rater", () => {
  beforeEach(() => serveAccess(false, true));

  it("shows the guide first, then scores with the keys and changes the last score", async () => {
    const batch = raterBatch({ id: `b-${Math.random()}` });
    const { handlers, answered } = raterHandlers(batch, ["t1", "t2", "t3", "t4"]);
    server.use(...handlers);
    const user = userEvent.setup();
    render(<ScoringPanel userId="user:alice" access={{ admin: false, rater: true }} />);

    await user.click(await screen.findByRole("button", { name: "开始打分" }));
    expect(screen.getByText("看构图与光线")).toBeTruthy();
    expect(await screen.findByText("4.50 分")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "开始打分" }));

    const first = await shownPhoto();
    expect(first.getAttribute("src")).toContain("/claims/t1/image");
    // 3, then two tenths up and one hundredth down: 3.19.
    await user.keyboard("3{ArrowRight}{ArrowRight}{Shift>}{ArrowLeft}{/Shift}");
    expect((screen.getByRole("textbox", { name: "分数，0 到 5 之间，最多两位小数" }) as HTMLInputElement).value).toBe(
      "3.19",
    );
    expect(screen.getByRole("slider", { name: "分数" }).getAttribute("aria-valuetext")).toBe("3.19");
    await user.keyboard("{Enter}");
    await waitFor(() =>
      expect(screen.getByRole("img", { name: "要打分的照片" }).getAttribute("src")).toContain("/claims/t2/image"),
    );
    expect(screen.getByText(counted("已打 1 张"))).toBeTruthy();
    await waitFor(() => expect(answered).toHaveLength(1));
    expect(answered[0]).toMatchObject({ token: "t1", body: { kind: "score", score: 3.19 } });
    expect(answered[0].body.submission).toMatch(/^[0-9a-f-]{36}$/);
    // The next photo starts with no score.
    expect((screen.getByRole("textbox", { name: "分数，0 到 5 之间，最多两位小数" }) as HTMLInputElement).value).toBe(
      "",
    );
    expect(screen.getByRole("slider", { name: "分数" }).getAttribute("aria-valuetext")).toBe("未打分");
    expect((screen.getByRole("button", { name: /确定/ }) as HTMLButtonElement).disabled).toBe(true);
    // The photo just scored now sits on the ruler, whole, at its score.
    const thumbnail = screen.getByRole("img", { name: "3.19 分的照片" });
    expect(thumbnail.getAttribute("src")).toContain("/claims/t1/thumbnail");
    // Hovering it sets the photo beside the one being scored, and leaving puts it away.
    await user.hover(thumbnail);
    const beside = await screen.findByRole("img", { name: "你打过 3.19 分的照片" });
    expect(beside.getAttribute("src")).toContain("/claims/t1/image");
    expect(screen.getByText("这一张")).toBeTruthy();
    await user.unhover(thumbnail);
    await waitFor(() => expect(screen.queryByRole("img", { name: "你打过 3.19 分的照片" })).toBeNull());

    // A score typed in the box: refused past two decimals, sent once valid.
    const box = screen.getByRole("textbox", { name: "分数，0 到 5 之间，最多两位小数" });
    await user.clear(box);
    await user.type(box, "4.555");
    expect(screen.getByRole("alert").textContent).toBe("0 到 5 之间，最多两位小数");
    expect((screen.getByRole("button", { name: /确定/ }) as HTMLButtonElement).disabled).toBe(true);
    await user.clear(box);
    await user.type(box, "5{Enter}");
    await waitFor(() => expect(answered).toHaveLength(2));
    expect(answered[1]).toMatchObject({ token: "t2", body: { score: 5 } });
    screen.getByRole("application").focus();

    // Backspace takes the last step back, to the photo just scored with its
    // score; a new key replaces it.
    await user.keyboard("{Backspace}");
    expect((await screen.findByRole("status")).textContent).toBe("正在改上一张 · Esc 不改");
    await waitFor(() =>
      expect(screen.getByRole("img", { name: "要打分的照片" }).getAttribute("src")).toContain("/claims/t2/image"),
    );
    expect((screen.getByRole("textbox", { name: "分数，0 到 5 之间，最多两位小数" }) as HTMLInputElement).value).toBe(
      "5.00",
    );
    await user.keyboard("4{Enter}");
    await waitFor(() => expect(answered).toHaveLength(3));
    expect(answered[2]).toMatchObject({ token: "t2", body: { score: 4 } });
    expect(answered[2].body.submission).not.toBe(answered[1].body.submission);
    expect(screen.getByText(counted("已打 2 张"))).toBeTruthy();

    await user.keyboard("s");
    await waitFor(() => expect(answered).toHaveLength(4));
    expect(answered[3]).toMatchObject({ token: "t3", body: { kind: "skip" } });
    expect(answered[3].body.score).toBeUndefined();
  });

  it("opens a batch it has seen the guide of on the photos, and says when the batch is done", async () => {
    const batch = raterBatch({ id: "b-done", rated: 5 });
    window.localStorage.setItem("a-nas.scoring.guide.user:alice.b-done", "1");
    const { handlers } = raterHandlers(batch, ["only"]);
    server.use(...handlers);
    const user = userEvent.setup();
    render(<ScoringPanel userId="user:alice" access={{ admin: false, rater: true }} />);
    await user.click(await screen.findByRole("button", { name: "继续打分" }));
    await shownPhoto();
    await user.keyboard("3");
    await user.click(screen.getByRole("button", { name: /确定/ }));
    expect(await screen.findByText("这个批次你已经打完了")).toBeTruthy();
    expect(screen.getByText(counted("已打 6 张"))).toBeTruthy();
  });

  it("scores a batch's 打分项 one after another, answers after the last and steps back", async () => {
    const batch = raterBatch({
      id: `b-${Math.random()}`,
      dimensions: dimensionsOf("overall", "composition", "sharpness"),
    });
    window.localStorage.setItem(`a-nas.scoring.guide.user:alice.${batch.id}`, "1");
    const { handlers, answered } = raterHandlers(batch, ["t1", "t2", "t3"]);
    server.use(...handlers);
    const user = userEvent.setup();
    render(<ScoringPanel userId="user:alice" access={{ admin: false, rater: true }} />);
    await user.click(await screen.findByRole("button", { name: "开始打分" }));
    await shownPhoto();
    const items = screen.getByRole("list", { name: "打分项" });
    const current = () => within(items).getByRole("button", { current: "step" }).textContent;
    const arrow = (name: string) => screen.getByRole("button", { name }) as HTMLButtonElement;
    const box = (name: string) =>
      (screen.getByRole("textbox", { name: `${name}，0 到 5 之间，最多两位小数` }) as HTMLInputElement).value;
    // A new photo starts empty: nothing to confirm, and no way on or back yet.
    expect(current()).toContain("总分");
    expect((screen.getByRole("button", { name: /下一项/ }) as HTMLButtonElement).disabled).toBe(true);
    expect(arrow("下一张").disabled).toBe(true);
    expect(arrow("上一张").disabled).toBe(true);
    // Enter confirms 总分 and moves on; nothing is sent yet.
    await user.keyboard("3{Enter}");
    expect(current()).toContain("构图");
    expect(within(items).getByRole("button", { name: /总分/ }).textContent).toContain("3.00");
    // A pick of the pointer, once released, moves on by itself.
    await user.keyboard("2");
    await user.pointer([
      { keys: "[MouseLeft>]", target: screen.getByRole("slider", { name: "构图" }) },
      { keys: "[/MouseLeft]" },
    ]);
    await waitFor(() => expect(current()).toContain("清晰度"));
    // ↑ goes back without losing what was set.
    await user.keyboard("{ArrowUp}");
    expect(current()).toContain("构图");
    await user.keyboard("{ArrowDown}");
    // The way on opens once every item has a score.
    expect(arrow("下一张").disabled).toBe(true);
    await user.keyboard("4");
    expect(arrow("下一张").disabled).toBe(false);
    expect(answered).toHaveLength(0);
    // A pick on the last item answers the photo and the next one comes, empty.
    await user.pointer([
      { keys: "[MouseLeft>]", target: screen.getByRole("slider", { name: "清晰度" }) },
      { keys: "[/MouseLeft]" },
    ]);
    await waitFor(() => expect(answered).toHaveLength(1));
    expect(answered[0]).toMatchObject({
      token: "t1",
      body: { kind: "score", score: 3, scores: { composition: 2, sharpness: 4 } },
    });
    await waitFor(() =>
      expect(screen.getByRole("img", { name: "要打分的照片" }).getAttribute("src")).toContain("/claims/t2/image"),
    );
    expect(current()).toContain("总分");
    expect(within(items).getByRole("button", { name: /构图/ }).textContent).toContain("—");
    expect(box("总分")).toBe("");

    // 撤销一步 goes back one item, and from the first item to the last
    // photo's last item; Esc comes back with what was set here.
    await user.keyboard("3{Enter}");
    expect(current()).toContain("构图");
    await user.keyboard("{Backspace}");
    expect(current()).toContain("总分");
    expect(box("总分")).toBe("3.00");
    await user.keyboard("{Backspace}");
    expect((await screen.findByRole("status")).textContent).toBe("正在改上一张 · Esc 不改");
    await waitFor(() =>
      expect(screen.getByRole("img", { name: "要打分的照片" }).getAttribute("src")).toContain("/claims/t1/image"),
    );
    expect(current()).toContain("清晰度");
    expect(box("清晰度")).toBe("4.00");
    expect(arrow("上一张").disabled).toBe(true);
    await user.keyboard("{Escape}");
    await waitFor(() =>
      expect(screen.getByRole("img", { name: "要打分的照片" }).getAttribute("src")).toContain("/claims/t2/image"),
    );
    expect(box("总分")).toBe("3.00");

    // The arrows: back to the last photo, and on with its scores as they stand.
    await user.click(arrow("上一张"));
    await waitFor(() =>
      expect(screen.getByRole("img", { name: "要打分的照片" }).getAttribute("src")).toContain("/claims/t1/image"),
    );
    expect(current()).toContain("总分");
    await user.click(arrow("下一张"));
    await waitFor(() => expect(answered).toHaveLength(2));
    expect(answered[1]).toMatchObject({ token: "t1", body: { score: 3, scores: { composition: 2, sharpness: 4 } } });
    await waitFor(() =>
      expect(screen.getByRole("img", { name: "要打分的照片" }).getAttribute("src")).toContain("/claims/t2/image"),
    );
    expect(box("总分")).toBe("3.00");
  });

  it("names the 打分项 of a batch that starts with a custom one and answers it first", async () => {
    const batch = raterBatch({ id: `b-${Math.random()}`, dimensions: dimensionsOf("custom_1", "overall") });
    window.localStorage.setItem(`a-nas.scoring.guide.user:alice.${batch.id}`, "1");
    const { handlers, answered } = raterHandlers(batch, ["t1", "t2"]);
    server.use(...handlers);
    const user = userEvent.setup();
    render(<ScoringPanel userId="user:alice" access={{ admin: false, rater: true }} />);
    await user.click(await screen.findByRole("button", { name: "开始打分" }));
    await shownPhoto();
    // The custom one comes first, with what it asks to look at.
    expect(screen.getByText("整张照片的颜色是否协调")).toBeTruthy();
    expect(screen.getByRole("slider", { name: "色彩和谐" })).toBeTruthy();
    await user.keyboard("3{Enter}");
    expect(screen.getByRole("slider", { name: "总分" })).toBeTruthy();
    await user.keyboard("4{Enter}");
    await waitFor(() => expect(answered).toHaveLength(1));
    expect(answered[0]).toMatchObject({ token: "t1", body: { kind: "score", score: 3, scores: { overall: 4 } } });
  });

  it("scores a batch of three levels with 1, 3 and 5 and the arrows, showing the level", async () => {
    const batch = raterBatch({ id: `b-${Math.random()}`, scale: "three" });
    window.localStorage.setItem(`a-nas.scoring.guide.user:alice.${batch.id}`, "1");
    const { handlers, answered } = raterHandlers(batch, ["t1", "t2"]);
    server.use(...handlers);
    const user = userEvent.setup();
    render(<ScoringPanel userId="user:alice" access={{ admin: false, rater: true }} />);
    await user.click(await screen.findByRole("button", { name: "开始打分" }));
    await shownPhoto();
    const slider = screen.getByRole("slider", { name: "分数" });
    // Nothing to type, and 2 is no level.
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.getByText("点选一档，或按 1、3、5")).toBeTruthy();
    await user.keyboard("2");
    expect(slider.getAttribute("aria-valuetext")).toBe("未打分");
    await user.keyboard("3");
    expect(slider.getAttribute("aria-valuetext")).toBe("中");
    await user.keyboard("{ArrowRight}");
    expect(slider.getAttribute("aria-valuetext")).toBe("好");
    await user.keyboard("{Enter}");
    await waitFor(() => expect(answered).toHaveLength(1));
    expect(answered[0]).toMatchObject({ token: "t1", body: { kind: "score", score: 5 } });
  });

  it("keeps every score from a rater of a batch that hides them, and answers in its tenths", async () => {
    const batch = raterBatch({ id: `b-${Math.random()}`, scale: "tenths", hideDecimals: true });
    window.localStorage.setItem(`a-nas.scoring.guide.user:alice.${batch.id}`, "1");
    const { handlers, answered } = raterHandlers(batch, ["t1", "t2"]);
    server.use(...handlers);
    const user = userEvent.setup();
    render(<ScoringPanel userId="user:alice" access={{ admin: false, rater: true }} />);
    await user.click(await screen.findByRole("button", { name: "开始打分" }));
    await shownPhoto();
    expect(screen.queryByRole("textbox")).toBeNull();
    await user.keyboard("{ArrowRight}{ArrowRight}");
    expect(screen.getByRole("slider", { name: "分数" }).getAttribute("aria-valuetext")).toBe("已选");
    expect(screen.getByText("已选")).toBeTruthy();
    expect(screen.queryByText("2.6")).toBeNull();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(answered).toHaveLength(1));
    expect(answered[0]).toMatchObject({ token: "t1", body: { kind: "score", score: 2.6 } });
  });

  it("shows the photo at 100% on a click or while Z is held, in every batch", async () => {
    const batch = raterBatch({ id: `b-${Math.random()}` });
    window.localStorage.setItem(`a-nas.scoring.guide.user:alice.${batch.id}`, "1");
    const { handlers } = raterHandlers(batch, ["t1", "t2"]);
    let gone = false;
    // Handlers are tried in order: this one first, falling through until the original is gone.
    server.use(
      http.untyped.get("/api/v1/modules/scoring/claims/:token/full", () =>
        gone ? HttpResponse.json(apiError("original_changed"), { status: 410 }) : undefined,
      ),
      ...handlers,
    );
    // jsdom has no object URLs and loads no images: the 100% view stays loading.
    const created = vi.fn(() => "blob:full");
    const { createObjectURL, revokeObjectURL } = URL;
    URL.createObjectURL = created;
    URL.revokeObjectURL = vi.fn();
    try {
      const user = userEvent.setup();
      render(<ScoringPanel userId="user:alice" access={{ admin: false, rater: true }} />);
      await user.click(await screen.findByRole("button", { name: "开始打分" }));
      await shownPhoto();
      await user.keyboard("{z>}");
      expect(await screen.findByRole("status", { name: "正在载入 100% 原图" })).toBeTruthy();
      await waitFor(() => expect(created).toHaveBeenCalled());
      await user.keyboard("{/z}");
      expect(screen.queryByRole("status", { name: "正在载入 100% 原图" })).toBeNull();
      // A click looks, and another click comes back.
      await user.click(await shownPhoto());
      const loading = await screen.findByRole("status", { name: "正在载入 100% 原图" });
      await user.click(loading);
      expect(screen.queryByRole("status", { name: "正在载入 100% 原图" })).toBeNull();
      // A photo whose original changed says so.
      await user.keyboard("3{Enter}");
      await waitFor(() =>
        expect(screen.getByRole("img", { name: "要打分的照片" }).getAttribute("src")).toContain("/claims/t2/image"),
      );
      gone = true;
      await user.keyboard("{z>}");
      expect(await screen.findByText("原图已不在，或已不是导入时的照片")).toBeTruthy();
      await user.keyboard("{/z}");
    } finally {
      URL.createObjectURL = createObjectURL;
      URL.revokeObjectURL = revokeObjectURL;
    }
  });

  it("lists no batch until an administrator opens one", async () => {
    server.use(http.get("/api/v1/modules/scoring/batches", ({ response }) => response(200).json({ items: [] })));
    render(<ScoringPanel userId="user:alice" access={{ admin: false, rater: true }} />);
    expect(await screen.findByText("现在没有要打分的批次")).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "照片打分" })).toBeNull();
  });
});

describe("ScoringPanel for an administrator", () => {
  beforeEach(() => {
    setSession(session({ id: "user:owner", role: "admin" }, { csrfToken: "csrf" }));
    serveAccess(true, false);
  });

  it("creates a batch from a folder of 照片打分, scoring 打分项 in the order they are picked", async () => {
    const created: ScoringBatchInput[] = [];
    const made: { name: string; description?: string }[] = [];
    server.use(
      http.post("/api/v1/modules/scoring/admin/dimensions", async ({ request, response }) => {
        const body = await request.json();
        made.push(body);
        return body.name === "构图"
          ? response(409).json(apiError("dimension_exists", "a scoring dimension has this name"))
          : response(201).json({ id: "custom_2", name: body.name, description: body.description ?? "", custom: true });
      }),
      http.get("/api/v1/modules/scoring/admin/batches", ({ response }) => response(200).json({ items: [] })),
      http.get("/api/v1/modules/scoring/admin/sources", ({ response }) =>
        response(200).json({ items: [{ name: "风景第一批" }, { name: "城市" }] }),
      ),
      http.post("/api/v1/modules/scoring/admin/batches", async ({ request, response }) => {
        const body = await request.json();
        created.push(body);
        return response(201).json(
          adminBatch({
            name: body.name,
            state: "importing",
            import: { files: 0, imported: 0, skipped: 0, skippedBy: [], waitingForSpace: false },
            answers: 0,
          }),
        );
      }),
    );
    const user = userEvent.setup();
    render(<ScoringPanel userId="user:owner" access={{ admin: true, rater: false }} />);
    expect(await screen.findByText("还没有批次")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "新建批次" }));
    const dialog = await screen.findByRole("dialog", { name: "新建批次" });
    await within(dialog).findByRole("option", { name: "照片打分 / 城市" });
    await user.selectOptions(within(dialog).getByRole("combobox", { name: "照片文件夹" }), "城市");
    expect((within(dialog).getByRole("textbox", { name: "批次名称" }) as HTMLInputElement).value).toBe("城市");
    await user.click(within(dialog).getByRole("button", { name: "多一人" }));
    expect((within(dialog).getByRole("spinbutton", { name: "每张照片几人打分" }) as HTMLInputElement).value).toBe("4");
    await user.type(within(dialog).getByRole("textbox", { name: "打分标准" }), "5 分是可以印出来挂墙上的");
    // Nothing is picked at first, and a batch needs one 打分项 at least.
    const create = within(dialog).getByRole("button", { name: "新建" }) as HTMLButtonElement;
    await within(dialog).findByRole("button", { name: "总分" });
    expect(within(dialog).getByText("至少选一项", { exact: false })).toBeTruthy();
    expect(create.disabled).toBe(true);
    // Clicks in quick succession each count, and take out again.
    const light = within(dialog).getByRole("button", { name: "光影色彩" });
    const liking = within(dialog).getByRole("button", { name: "喜好程度" });
    act(() => {
      light.click();
      liking.click();
    });
    expect(within(dialog).getByRole("button", { name: "喜好程度，第 2 项" })).toBe(liking);
    act(() => {
      light.click();
      liking.click();
    });
    expect(liking.getAttribute("aria-pressed")).toBe("false");
    // The order of the clicks is the order raters score them; another click
    // takes one out and the later ones move up.
    await user.click(within(dialog).getByRole("button", { name: "清晰度" }));
    await user.click(within(dialog).getByRole("button", { name: "色彩和谐" }));
    await user.click(within(dialog).getByRole("button", { name: "总分" }));
    await user.click(within(dialog).getByRole("button", { name: "清晰度，第 1 项" }));
    expect(within(dialog).getByRole("button", { name: "色彩和谐，第 1 项" }).getAttribute("aria-pressed")).toBe("true");
    expect(within(dialog).getByRole("button", { name: "总分，第 2 项" })).toBeTruthy();
    // A new custom one is picked at once, last; a taken name is refused.
    await user.click(within(dialog).getByRole("button", { name: "自定义…" }));
    const maker = within(dialog).getByRole("group", { name: "新的打分项" });
    await user.type(within(maker).getByRole("textbox", { name: /名称/ }), "构图{Enter}");
    expect(await within(maker).findByRole("alert")).toHaveProperty("textContent", "已有同名的打分项");
    await user.clear(within(maker).getByRole("textbox", { name: /名称/ }));
    await user.type(within(maker).getByRole("textbox", { name: /名称/ }), "空间层次");
    await user.type(within(maker).getByRole("textbox", { name: /说明/ }), "前景、中景、远景是否分明");
    await user.click(within(maker).getByRole("button", { name: "添加并选上" }));
    expect(await within(dialog).findByRole("button", { name: "空间层次，第 3 项" })).toBeTruthy();
    expect(within(dialog).queryByRole("group", { name: "新的打分项" })).toBeNull();
    expect(made.at(-1)).toEqual({ name: "空间层次", description: "前景、中景、远景是否分明" });
    expect(within(dialog).getByText("长边不足 2400 像素的照片不加入批次。", { exact: false })).toBeTruthy();
    // The number line with two decimals, shown to raters, unless chosen otherwise.
    expect((within(dialog).getByRole("radio", { name: /两位小数/ }) as HTMLInputElement).checked).toBe(true);
    const shown = () => within(dialog).queryByRole("checkbox", { name: /打分人看得到小数/ }) as HTMLInputElement | null;
    expect(shown()?.checked).toBe(true);
    await user.click(within(dialog).getByRole("radio", { name: /一位小数/ }));
    await user.click(shown()!);
    expect(within(dialog).getByText(/导出的分数仍精确到 0\.1/)).toBeTruthy();
    // Three levels have no decimals to hide, and going back shows them again.
    await user.click(within(dialog).getByRole("radio", { name: /三档/ }));
    expect(shown()).toBeNull();
    await user.click(within(dialog).getByRole("radio", { name: /一位小数/ }));
    expect(shown()?.checked).toBe(true);
    await user.click(shown()!);
    expect(create.disabled).toBe(false);
    await user.click(create);
    await waitFor(() =>
      expect(created).toEqual([
        {
          name: "城市",
          source: "城市",
          required: 4,
          rubric: "5 分是可以印出来挂墙上的",
          dimensions: ["custom_1", "overall", "custom_2"],
          scale: "tenths",
          hideDecimals: true,
        },
      ]),
    );
    expect(await screen.findByText("正在读取文件夹")).toBeTruthy();
  });

  it("says when an import waits for space on the data volume", async () => {
    server.use(
      http.get("/api/v1/modules/scoring/admin/batches", ({ response }) =>
        response(200).json({
          items: [
            adminBatch({
              state: "importing",
              import: { files: 160, imported: 40, skipped: 2, skippedBy: [], waitingForSpace: true },
            }),
          ],
        }),
      ),
      http.get("/api/v1/modules/scoring/admin/batches/{batchId}/stats", ({ response }) =>
        response(200).json({ items: [] }),
      ),
    );
    const user = userEvent.setup();
    render(<ScoringPanel userId="user:owner" access={{ admin: true, rater: false }} />);
    const row = await screen.findByRole("button", { name: /风景第一批/ });
    expect(row.textContent).toContain("空间不足，导入已暂停");
    await user.click(row);
    expect((await screen.findByText(/腾出空间后自动继续/)).textContent).toContain("已处理 42 / 160 个文件");
  });

  it("shows a batch's coverage and each rater's statistics, and closes it", async () => {
    let state: "open" | "closed" = "open";
    server.use(
      http.get("/api/v1/modules/scoring/admin/batches", ({ response }) =>
        response(200).json({
          items: [
            adminBatch({
              state,
              import: {
                files: 160,
                imported: 118,
                skipped: 42,
                skippedBy: [
                  { reason: "too_small", count: 40 },
                  { reason: "unsupported_type", count: 2 },
                ],
                waitingForSpace: false,
              },
            }),
          ],
        }),
      ),
      http.get("/api/v1/modules/scoring/admin/batches/{batchId}/stats", ({ response }) =>
        response(200).json({
          items: [
            raterStats(),
            raterStats({
              rater: { id: "user:bob", username: "bob" },
              fastShare: 0.17,
              dimensions: [dimensionStats({ agreement: null, repeatDifference: null, longestRun: 23 })],
            }),
          ],
        }),
      ),
      http.patch("/api/v1/modules/scoring/admin/batches/{batchId}", async ({ request, response }) => {
        const body = await request.json();
        state = body.state ?? state;
        return response(200).json(adminBatch({ state }));
      }),
    );
    const user = userEvent.setup();
    render(<ScoringPanel userId="user:owner" access={{ admin: true, rater: false }} />);
    const row = await screen.findByRole("button", { name: /风景第一批/ });
    expect(row.querySelector("img")?.getAttribute("src")).toBe(
      "/api/v1/modules/scoring/admin/batches/b1/cover?photos=" + adminBatch().photos,
    );
    await user.click(row);
    const table = await screen.findByRole("table");
    const alice = within(table).getByRole("row", { name: /小艾/ });
    expect(alice.textContent).toContain("2.90 ± 0.60");
    expect(alice.textContent).toContain("0.71");
    expect(alice.textContent).toContain("3.4 秒");
    expect(within(table).getByRole("row", { name: /bob/ }).textContent).toContain("—");
    // Habits that suggest a rater is not looking are called out.
    expect(within(table).getByText("17% 的照片在 1 秒内打完")).toBeTruthy();
    expect(within(table).getByText("连续 23 张同分")).toBeTruthy();
    // Skipped files are counted by reason.
    expect(screen.getByText(/导入时跳过/).textContent).toBe(
      "导入时跳过 42 个文件：长边不足 2400 像素 40 个，格式不支持 2 个。",
    );
    // A batch of 总分 alone has no tabs of 打分项.
    expect(screen.queryByRole("group", { name: "按打分项看" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "结束批次" }));
    expect(await screen.findByRole("button", { name: "重新开放" })).toBeTruthy();
    expect(state).toBe("closed");
  });

  it("shows each 打分项's statistics and exports the chosen columns and rows", async () => {
    const dimensions = dimensionsOf("overall", "composition");
    server.use(
      http.get("/api/v1/modules/scoring/admin/batches", ({ response }) =>
        response(200).json({ items: [adminBatch({ dimensions })] }),
      ),
      http.get("/api/v1/modules/scoring/admin/batches/{batchId}/stats", ({ response }) =>
        response(200).json({
          items: [
            raterStats({
              dimensions: [dimensionStats(), dimensionStats({ dimension: "composition", mean: 3.4, longestRun: 18 })],
            }),
          ],
        }),
      ),
      http.get("/api/v1/modules/scoring/admin/batches/{batchId}/export/summary", ({ response }) =>
        response(200).json({ scores: 120, skips: 4, superseded: 3, repeats: 6 }),
      ),
    );
    const user = userEvent.setup();
    render(<ScoringPanel userId="user:owner" access={{ admin: true, rater: false }} />);
    await user.click(await screen.findByRole("button", { name: /风景第一批/ }));
    const table = await screen.findByRole("table");
    expect(within(table).getByRole("row", { name: /小艾/ }).textContent).toContain("2.90 ± 0.60");
    await user.click(within(screen.getByRole("group", { name: "按打分项看" })).getByRole("button", { name: "构图" }));
    expect(within(table).getByRole("row", { name: /小艾/ }).textContent).toContain("3.40 ± 0.60");
    expect(within(table).getByText("构图连续 18 张同分")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "导出" }));
    const dialog = await screen.findByRole("dialog", { name: "导出“风景第一批”" });
    expect(await within(dialog).findByText("120")).toBeTruthy();
    // Image and rater are in every row; the 打分项 and the defaults start chosen.
    const columns = within(dialog).getByLabelText("导出的列");
    expect(columns.textContent).toBe("image_idimage_pathraternicknameoverallcompositionduration_ssubmitted_at");
    await user.click(within(dialog).getByRole("button", { name: "原图宽高" }));
    await user.click(within(dialog).getByRole("button", { name: "JSON Lines" }));
    // Rows of other kinds bring their kind along.
    await user.click(within(dialog).getByRole("button", { name: "跳过与标记" }));
    expect(within(dialog).getByText("124")).toBeTruthy();
    const link = within(dialog).getByRole("link", { name: "导出" });
    expect(link.getAttribute("href")).toBe(
      "/api/v1/modules/scoring/admin/batches/b1/export?format=jsonl&fields=overall%2Ccomposition%2Cnickname%2Cduration%2Csubmitted_at%2Cimage_size%2Ckind&include=skips",
    );
    // jsdom cannot download; the page's own handling of the click is what counts.
    link.addEventListener("click", (event) => event.preventDefault());
    await user.click(link);
    // The choice is kept for next time, beside the 打分项.
    expect(JSON.parse(window.localStorage.getItem("a-nas.scoring.export") ?? "{}")).toEqual({
      format: "jsonl",
      include: ["skips"],
      fields: ["nickname", "duration", "submitted_at", "image_size", "kind"],
    });
  });

  it("chooses raters among the members", async () => {
    const changes: string[] = [];
    server.use(
      http.get("/api/v1/modules/scoring/admin/batches", ({ response }) => response(200).json({ items: [] })),
      http.get("/api/v1/modules/scoring/admin/raters", ({ response }) =>
        response(200).json({ items: [raterEntry("user:alice", "alice", true), raterEntry("user:bob", "bob", false)] }),
      ),
      http.put("/api/v1/modules/scoring/admin/raters/{accountId}", ({ params, response }) => {
        changes.push(`+${params.accountId}`);
        return response(204).empty();
      }),
      http.delete("/api/v1/modules/scoring/admin/raters/{accountId}", ({ params, response }) => {
        changes.push(`-${params.accountId}`);
        return response(204).empty();
      }),
    );
    const user = userEvent.setup();
    render(<ScoringPanel userId="user:owner" access={{ admin: true, rater: false }} />);
    await user.click(await screen.findByRole("button", { name: "打分人" }));
    expect(await screen.findByText("已选 1 人")).toBeTruthy();
    await user.click(screen.getByRole("switch", { name: /bob/ }));
    await user.click(screen.getByRole("switch", { name: /alice/ }));
    await waitFor(() => expect(changes).toEqual(["+user:bob", "-user:alice"]));
    expect(screen.getByText("已选 1 人")).toBeTruthy();
  });
});

describe("the stage's outbox", () => {
  it("keeps an answer that could not be sent and sends it again", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const { Outbox } = await import("./model");
      const sent: string[] = [];
      let fail = true;
      const refused: string[] = [];
      const outbox = new Outbox(
        async (answer) => {
          if (fail) throw new TypeError("offline");
          sent.push(answer.token);
          return sent.length;
        },
        (caught) => caught instanceof TypeError,
        (answer) => refused.push(answer.token),
      );
      const states: number[] = [];
      outbox.subscribe((state) => states.push(state.waiting));
      outbox.push({ token: "a", body: { submission: "aaaaaaaa", kind: "skip", viewMs: 1 } });
      outbox.push({ token: "b", body: { submission: "bbbbbbbb", kind: "skip", viewMs: 1 } });
      await vi.advanceTimersByTimeAsync(10);
      expect(outbox.current()).toMatchObject({ waiting: 2, retrying: true });
      fail = false;
      await vi.advanceTimersByTimeAsync(2_500);
      expect(sent).toEqual(["a", "b"]);
      expect(outbox.current()).toMatchObject({ waiting: 0, retrying: false, rated: 2 });
      expect(refused).toEqual([]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("says when it waits for space on the data volume, not for the connection", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const { Outbox } = await import("./model");
      const { retryable } = await import("./api");
      const { APIError } = await import("../api/client");
      let full = true;
      const outbox = new Outbox(
        async () => {
          if (full) throw new APIError(507, "insufficient_storage");
          return 1;
        },
        retryable,
        () => undefined,
      );
      outbox.push({ token: "a", body: { submission: "aaaaaaaa", kind: "skip", viewMs: 1 } });
      await vi.advanceTimersByTimeAsync(10);
      expect(outbox.current()).toMatchObject({ waiting: 1, retrying: true, noSpace: true });
      full = false;
      await vi.advanceTimersByTimeAsync(2_500);
      expect(outbox.current()).toMatchObject({ waiting: 0, retrying: false, noSpace: false });
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("the 100% view", () => {
  it("tells why the server refused it", async () => {
    const { fetchFull } = await import("./api");
    let refusal: [number, string] = [507, "insufficient_storage"];
    server.use(
      http.untyped.get("/api/v1/modules/scoring/claims/:token/full", () =>
        HttpResponse.json(apiError(refusal[1]), { status: refusal[0] }),
      ),
    );
    await expect(fetchFull("t1")).rejects.toThrow("数据卷剩余空间不足");
    refusal = [410, "original_changed"];
    await expect(fetchFull("t1")).rejects.toThrow("原图已不在，或已不是导入时的照片");
  });
});
