import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import type { ScoringBatchInput } from "../api.generated";
import { session } from "../test/fixtures";
import { adminBatch, raterBatch, raterEntry, raterHandlers, raterStats } from "../test/handlers/scoring";
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
    http.get("/api/v1/modules/scoring/me", ({ response }) => response(200).json({ admin, rater })),
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
          adminBatch({ name: body.name, state: "importing", import: { files: 0, imported: 0, skipped: 0 } }),
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
    await user.click(within(dialog).getByRole("button", { name: "新建" }));
    await waitFor(() =>
      expect(created).toEqual([{ name: "城市", source: "城市", required: 4, rubric: "5 分是可以印出来挂墙上的" }]),
    );
    expect(await screen.findByText("正在读取文件夹")).toBeTruthy();
  });

  it("shows a batch's coverage and each rater's statistics, and closes it", async () => {
    let state: "open" | "closed" = "open";
    server.use(
      http.get("/api/v1/modules/scoring/admin/batches", ({ response }) =>
        response(200).json({ items: [adminBatch({ state })] }),
      ),
      http.get("/api/v1/modules/scoring/admin/batches/{batchId}/stats", ({ response }) =>
        response(200).json({
          items: [
            raterStats(),
            raterStats({
              rater: { id: "user:bob", username: "bob" },
              agreement: null,
              repeatDifference: null,
              fastShare: 0.17,
              longestRun: 23,
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
    expect(within(table).getByText("17% 的分数在 1 秒内打出")).toBeTruthy();
    expect(within(table).getByText("连续 23 张同分")).toBeTruthy();
    expect(screen.getByRole("link", { name: "导出" }).getAttribute("href")).toBe(
      "/api/v1/modules/scoring/admin/batches/b1/export",
    );
    await user.click(screen.getByRole("button", { name: "结束批次" }));
    expect(await screen.findByRole("button", { name: "重新开放" })).toBeTruthy();
    expect(state).toBe("closed");
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
