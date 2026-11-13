import { render, screen, waitFor, within } from "@testing-library/react";
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
  scoringMe,
} from "../test/handlers/scoring";
import { http, server } from "../test/server";
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

// The window reads the account's roles again when it opens.
function serveAccess(admin: boolean, rater: boolean) {
  server.use(
    http.get("/api/v1/modules", ({ response }) => response(200).json({ items: [{ id: "scoring" }] })),
    http.get("/api/v1/modules/scoring/me", ({ response }) => response(200).json(scoringMe(admin, rater))),
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
    expect((screen.getByRole("textbox", { name: "分数，0 到 5，最多两位小数" }) as HTMLInputElement).value).toBe(
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
    const box = screen.getByRole("textbox", { name: "分数，0 到 5，最多两位小数" });
    await user.clear(box);
    await user.type(box, "4.555");
    expect(screen.getByRole("alert").textContent).toBe("0 到 5 之间，最多两位小数");
    expect((screen.getByRole("button", { name: /确定/ }) as HTMLButtonElement).disabled).toBe(true);
    await user.clear(box);
    await user.type(box, "5{Enter}");
    await waitFor(() => expect(answered).toHaveLength(2));
    expect(answered[1]).toMatchObject({ token: "t2", body: { score: 5 } });
    screen.getByRole("application").focus();

    // Backspace goes back to the photo just scored; a new key replaces its score.
    await user.keyboard("{Backspace}");
    expect((await screen.findByRole("status")).textContent).toBe("正在改上一张 · 原来 5.00 · Esc 取消");
    await waitFor(() =>
      expect(screen.getByRole("img", { name: "要打分的照片" }).getAttribute("src")).toContain("/claims/t2/image"),
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
    await user.click(screen.getByRole("button", { name: /确定/ }));
    expect(await screen.findByText("这个批次你已经打完了")).toBeTruthy();
    expect(screen.getByText(counted("已打 6 张"))).toBeTruthy();
  });

  it("scores a batch's 打分项 one after another on the same ruler and answers once", async () => {
    const batch = raterBatch({
      id: `b-${Math.random()}`,
      dimensions: dimensionsOf("overall", "composition", "sharpness"),
    });
    window.localStorage.setItem(`a-nas.scoring.guide.user:alice.${batch.id}`, "1");
    const { handlers, answered } = raterHandlers(batch, ["t1", "t2"]);
    server.use(...handlers);
    const user = userEvent.setup();
    render(<ScoringPanel userId="user:alice" access={{ admin: false, rater: true }} />);
    await user.click(await screen.findByRole("button", { name: "开始打分" }));
    await shownPhoto();
    const items = screen.getByRole("list", { name: "打分项" });
    const current = () => within(items).getByRole("button", { current: "step" }).textContent;
    expect(current()).toContain("总分");
    expect(screen.getByRole("button", { name: /下一项/ })).toBeTruthy();
    // Enter confirms 总分 and moves on; nothing is sent yet.
    await user.keyboard("3{Enter}");
    expect(current()).toContain("构图");
    expect(within(items).getByRole("button", { name: /总分/ }).textContent).toContain("3.00");
    expect(screen.getByRole("textbox", { name: "构图，0 到 5，最多两位小数" })).toBeTruthy();
    // A pick of the pointer, once released, moves on by itself.
    const slider = screen.getByRole("slider", { name: "构图" });
    await user.keyboard("2");
    await user.pointer([{ keys: "[MouseLeft>]", target: slider }, { keys: "[/MouseLeft]" }]);
    await waitFor(() => expect(current()).toContain("清晰度"));
    expect(screen.getByText("看 100%", { exact: false })).toBeTruthy();
    expect(answered).toHaveLength(0);
    // The last one waits for 确定, even after a pick.
    await user.keyboard("4");
    await user.pointer([
      { keys: "[MouseLeft>]", target: screen.getByRole("slider", { name: "清晰度" }) },
      { keys: "[/MouseLeft]" },
    ]);
    await new Promise((resolve) => window.setTimeout(resolve, 500));
    expect(answered).toHaveLength(0);
    // ↑ goes back without losing what was set.
    await user.keyboard("{ArrowUp}");
    expect(current()).toContain("构图");
    await user.keyboard("{ArrowDown}");
    await user.click(screen.getByRole("button", { name: /确定/ }));
    await waitFor(() => expect(answered).toHaveLength(1));
    expect(answered[0]).toMatchObject({
      token: "t1",
      body: { kind: "score", score: 3, scores: { composition: 2, sharpness: 4 } },
    });
    // The next photo starts on 总分 again.
    await waitFor(() =>
      expect(screen.getByRole("img", { name: "要打分的照片" }).getAttribute("src")).toContain("/claims/t2/image"),
    );
    expect(current()).toContain("总分");
  });

  it("shows the photo at 100% while Z is held, in batches that score sharpness", async () => {
    const batch = raterBatch({ id: `b-${Math.random()}`, dimensions: dimensionsOf("overall", "sharpness") });
    window.localStorage.setItem(`a-nas.scoring.guide.user:alice.${batch.id}`, "1");
    const { handlers } = raterHandlers(batch, ["t1", "t2"]);
    let gone = false;
    // Handlers are tried in order: this one first, falling through until the original is gone.
    server.use(
      http.untyped.get("/api/v1/modules/scoring/claims/:token/full", () =>
        gone ? new HttpResponse(null, { status: 410 }) : undefined,
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
      // A photo whose original changed says so.
      await user.keyboard("3{Enter}4{Enter}");
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

  it("creates a batch from a folder of 照片打分", async () => {
    const created: ScoringBatchInput[] = [];
    server.use(
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
            import: { files: 0, imported: 0, skipped: 0, skippedBy: [] },
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
    // 总分 is always scored; the others are chosen, and kept in the catalogue's order.
    const overall = await within(dialog).findByRole("button", { name: /总分/ });
    expect(overall.getAttribute("aria-pressed")).toBe("true");
    expect((overall as HTMLButtonElement).disabled).toBe(true);
    await user.click(within(dialog).getByRole("button", { name: "清晰度" }));
    await user.click(within(dialog).getByRole("button", { name: "构图" }));
    expect(within(dialog).getByText("长边不足 2400 像素的照片不加入批次。", { exact: false })).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "新建" }));
    await waitFor(() =>
      expect(created).toEqual([
        {
          name: "城市",
          source: "城市",
          required: 4,
          rubric: "5 分是可以印出来挂墙上的",
          dimensions: ["overall", "composition", "sharpness"],
        },
      ]),
    );
    expect(await screen.findByText("正在读取文件夹")).toBeTruthy();
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
});
