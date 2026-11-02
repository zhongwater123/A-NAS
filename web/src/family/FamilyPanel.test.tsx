import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import type { FamilyRecordInput } from "../api.generated";
import { session } from "../test/fixtures";
import { emptySchedule, familyBaby, familyHandlers, familyOverview, familyRecord } from "../test/handlers/family";
import { asset } from "../test/handlers/photos";
import { http, server } from "../test/server";
import { summarize } from "./FamilyNotices";
import { FamilyPanel } from "./FamilyPanel";
import { isoDate } from "./format";

// daysFromToday is a date relative to the day the test runs.
const daysFromToday = (days: number) => {
  const date = new Date();
  date.setDate(date.getDate() + days);
  return isoDate(date);
};

// Radix places its menus with ResizeObserver and pointer capture, and motion
// raises records as they scroll into view with IntersectionObserver; jsdom
// has none of them. Nothing here ever scrolls into view, so recordings keep
// their drawn bars and never decode.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  globalThis.IntersectionObserver ??= class {
    readonly root = null;
    readonly rootMargin = "";
    readonly thresholds = [];
    readonly scrollMargin = "";
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords() {
      return [];
    }
  };
  Element.prototype.hasPointerCapture ??= () => false;
  Element.prototype.releasePointerCapture ??= () => undefined;
  Element.prototype.scrollIntoView ??= () => undefined;
});

beforeEach(() => {
  setSession(
    session(
      { id: "user:mom", username: "mom", role: "member" },
      { csrfToken: "csrf", expiresAt: "2026-11-01T00:00:00Z" },
    ),
  );
});

describe("FamilyPanel", () => {
  it("lets an account outside the family ask to join with a role", async () => {
    let asked = "";
    const outside = familyOverview({ me: { status: "none", guardian: false }, members: [], babies: [] });
    server.use(
      ...familyHandlers({ overview: outside }),
      http.post("/api/v1/family/join", async ({ request, response }) => {
        asked = (await request.json()).role;
        return response(200).json({ status: "pending", role: asked, guardian: false });
      }),
    );
    render(<FamilyPanel userId="user:mom" onShowPhoto={vi.fn()} />);
    expect(await screen.findByRole("heading", { name: "加入家庭中心" })).toBeTruthy();
    const join = screen.getByRole("button", { name: "申请加入" });
    expect((join as HTMLButtonElement).disabled).toBe(true);
    await userEvent.click(screen.getByRole("radio", { name: "其他" }));
    await userEvent.type(screen.getByRole("textbox", { name: "自己填写称呼" }), "舅舅");
    // Once asked, the window shows the request waiting.
    server.use(
      http.get("/api/v1/family", ({ response }) =>
        response(200).json({ ...outside, me: { status: "pending", role: "舅舅", guardian: false } }),
      ),
    );
    await userEvent.click(join);
    expect(asked).toBe("舅舅");
    expect(await screen.findByText("等待家长批准")).toBeTruthy();
  });

  it("shows the baby, what is due and the channel's records", async () => {
    server.use(
      ...familyHandlers({
        records: [
          familyRecord(),
          familyRecord({ id: "rec_2", kind: "measurement", text: "", measurement: { heightCm: 66, weightKg: 7.2 } }),
        ],
        schedule: {
          ...emptySchedule,
          doses: [
            {
              code: "dtap",
              name: "百白破疫苗",
              dose: 2,
              programme: true,
              ageLabel: "4月龄",
              dueDate: daysFromToday(-10),
              status: "available",
            },
          ],
        },
      }),
    );
    render(<FamilyPanel userId="user:mom" onShowPhoto={vi.fn()} />);
    const side = await screen.findByRole("complementary", { name: "小米粒的资料" });
    expect(within(side).getByRole("heading", { name: /小米粒/ })).toBeTruthy();
    expect(
      within(side).getByText((_, node) => node?.tagName === "P" && node.textContent === "7 个月 14 天"),
    ).toBeTruthy();
    expect(await within(side).findByRole("button", { name: "记录百白破疫苗第 2 剂" })).toBeTruthy();
    // The records sit in the month of life they happened in.
    const month = await screen.findByRole("region", { name: "7 个月" });
    expect(within(month).getByText("今天第一次笑出了声")).toBeTruthy();
    const measurement = within(month).getByText("身长").closest("article")!;
    expect(within(measurement).getByText(/7.20/)).toBeTruthy();
    // 66 cm at 7 months 13 days lies between the fixture's P25 and P50.
    expect(await within(measurement).findByText("P25—P50")).toBeTruthy();
  });

  it("opens a record's comments and adds one", async () => {
    let said = "";
    server.use(
      ...familyHandlers({ records: [familyRecord()] }),
      http.post("/api/v1/family/records/{recordId}/comments", async ({ request, response }) => {
        said = (await request.json()).text;
        return response(201).json({
          id: "c_1",
          recordId: "rec_1",
          text: said,
          authorId: "user:mom",
          authorName: "mom",
          createdAt: "2026-10-29T10:00:00Z",
        });
      }),
    );
    render(<FamilyPanel userId="user:mom" onShowPhoto={vi.fn()} />);
    await screen.findByText("今天第一次笑出了声");
    await userEvent.click(screen.getByRole("button", { name: "留言" }));
    await userEvent.type(screen.getByRole("textbox", { name: "留言" }), "妈，辛苦了{Enter}");
    await waitFor(() => expect(said).toBe("妈，辛苦了"));
    expect(await screen.findByText("妈，辛苦了")).toBeTruthy();
  });

  it("writes a journal entry and shows it at the top", async () => {
    let added: FamilyRecordInput | undefined;
    const written = familyRecord({ id: "rec_new", text: "外婆来看你了", authorId: "user:mom", authorName: "mom" });
    server.use(
      ...familyHandlers(),
      http.post("/api/v1/family/babies/{babyId}/records", async ({ request, response }) => {
        added = await request.json();
        return response(201).json(written);
      }),
    );
    render(<FamilyPanel userId="user:mom" onShowPhoto={vi.fn()} />);
    await screen.findByText("还没有记录");
    const side = screen.getByRole("complementary", { name: "小米粒的资料" });
    await userEvent.click(within(side).getByRole("button", { name: "日志" }));
    const dialog = await screen.findByRole("dialog", { name: "记录日志" });
    await userEvent.type(within(dialog).getByRole("textbox", { name: "内容" }), "外婆来看你了");
    server.use(
      http.get("/api/v1/family/babies/{babyId}/records", ({ response }) => response(200).json({ records: [written] })),
    );
    await userEvent.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(added?.kind).toBe("journal"));
    expect(added?.text).toBe("外婆来看你了");
    expect(added?.requestKey).toBeTruthy();
    expect(await screen.findByText("外婆来看你了")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("leaves and takes back a reaction with one tap", async () => {
    const calls: string[] = [];
    server.use(
      ...familyHandlers({ records: [familyRecord()] }),
      http.put("/api/v1/family/records/{recordId}/reactions/{reaction}", ({ params, response }) => {
        calls.push(`put ${params.reaction}`);
        return response(200).json({ items: [{ name: "heart", accountIds: ["user:grandma", "user:mom"] }] });
      }),
      http.delete("/api/v1/family/records/{recordId}/reactions/{reaction}", ({ params, response }) => {
        calls.push(`delete ${params.reaction}`);
        return response(200).json({ items: [{ name: "heart", accountIds: ["user:grandma"] }] });
      }),
    );
    render(<FamilyPanel userId="user:mom" onShowPhoto={vi.fn()} />);
    await screen.findByText("今天第一次笑出了声");
    await userEvent.click(screen.getByRole("button", { name: "表情回应" }));
    await userEvent.click(await screen.findByRole("button", { name: "回应 ❤️" }));
    const mine = await screen.findByRole("button", { name: /^❤️ 2/, pressed: true });
    await waitFor(() => expect(calls).toEqual(["put heart"]));
    await userEvent.click(mine);
    expect(await screen.findByRole("button", { name: /^❤️ 1/, pressed: false })).toBeTruthy();
    expect(calls).toEqual(["put heart", "delete heart"]);
  });

  it("opens a record's photos in the window's viewer", async () => {
    const onShowPhoto = vi.fn();
    server.use(
      ...familyHandlers({
        records: [
          familyRecord({
            kind: "photos",
            text: "公园散步",
            photos: [
              { assetId: "asset:1", video: false, role: "album" },
              { assetId: "asset:2", video: false, role: "album" },
            ],
          }),
        ],
      }),
      http.get("/api/v1/photos/assets/{assetId}", ({ params, response }) =>
        response(200).json(asset(String(params.assetId))),
      ),
    );
    render(<FamilyPanel userId="user:mom" onShowPhoto={onShowPhoto} />);
    await userEvent.click(await screen.findByRole("button", { name: "查看第 1 张照片" }));
    const viewer = await screen.findByRole("dialog", { name: "1 / 2" });
    await userEvent.keyboard("{ArrowRight}");
    expect(await within(viewer).findByRole("heading", { name: "2 / 2" })).toBeTruthy();
    await userEvent.click(within(viewer).getByRole("button", { name: "在相册中打开" }));
    expect(onShowPhoto).toHaveBeenCalledWith("asset:2");
    await userEvent.click(within(viewer).getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("writes today's journal from the line at the top of the timeline", async () => {
    let added: FamilyRecordInput | undefined;
    const written = familyRecord({ id: "rec_quick", text: "第一次自己翻身", authorId: "user:mom", authorName: "mom" });
    server.use(
      ...familyHandlers(),
      http.post("/api/v1/family/babies/{babyId}/records", async ({ request, response }) => {
        added = await request.json();
        return response(201).json(written);
      }),
    );
    render(<FamilyPanel userId="user:mom" onShowPhoto={vi.fn()} />);
    const line = await screen.findByRole("textbox", { name: "写日志" });
    await userEvent.type(line, "第一次自己翻身");
    server.use(
      http.get("/api/v1/family/babies/{babyId}/records", ({ response }) => response(200).json({ records: [written] })),
    );
    await userEvent.keyboard("{Control>}{Enter}{/Control}");
    await waitFor(() =>
      expect(added).toMatchObject({ kind: "journal", text: "第一次自己翻身", occurredOn: isoDate(new Date()) }),
    );
    expect(await screen.findByText("第一次自己翻身", { selector: "blockquote" })).toBeTruthy();
  });

  it("starts a photo record from files dropped on the channel", async () => {
    server.use(...familyHandlers());
    render(<FamilyPanel userId="user:mom" onShowPhoto={vi.fn()} />);
    const side = await screen.findByRole("complementary", { name: "小米粒的资料" });
    const channel = side.parentElement!;
    const clip = new File(["clip"], "clip.mp4", { type: "video/mp4" });
    const dataTransfer = { files: [clip], types: ["Files"] };
    fireEvent.dragEnter(channel, { dataTransfer });
    expect(await screen.findByText(/松开以添加到米米的时间线/)).toBeTruthy();
    fireEvent.drop(channel, { dataTransfer });
    const dialog = await screen.findByRole("dialog", { name: "记录照片视频" });
    expect(within(dialog).getByText("clip.mp4")).toBeTruthy();
  });

  it("asks a guardian to add the first baby", async () => {
    server.use(...familyHandlers({ overview: familyOverview({ babies: [] }) }));
    render(<FamilyPanel userId="user:mom" onShowPhoto={vi.fn()} />);
    expect(await screen.findByRole("heading", { name: "还没有宝宝" })).toBeTruthy();
  });

  it("folds the vaccine ages already done in the booklet", async () => {
    const dose = (code: string, name: string, ageLabel: string, days: number, status: "given" | "upcoming") => ({
      code,
      name,
      dose: 1,
      programme: true,
      ageLabel,
      dueDate: daysFromToday(days),
      status,
      ...(status === "given" ? { givenOn: daysFromToday(days) } : {}),
    });
    server.use(
      ...familyHandlers({
        schedule: {
          ...emptySchedule,
          doses: [
            dose("hepb", "乙肝疫苗", "出生时", -220, "given"),
            dose("bcg", "卡介苗", "出生时", -220, "given"),
            dose("polio", "脊灰疫苗", "2月龄", -160, "given"),
            dose("mmr", "麻腮风疫苗", "8月龄", 20, "upcoming"),
          ],
        },
      }),
    );
    render(<FamilyPanel userId="user:mom" onShowPhoto={vi.fn()} />);
    await userEvent.click(await screen.findByRole("tab", { name: "疫苗" }));
    const fold = await screen.findByRole("button", { name: /出生时 — 2月龄的 3 剂都已完成/ });
    expect(screen.getByText("麻腮风疫苗")).toBeTruthy();
    expect(screen.queryByText("卡介苗")).toBeNull();
    await userEvent.click(fold);
    expect(screen.getByText("卡介苗")).toBeTruthy();
    expect(screen.getAllByText("已种")).toHaveLength(3);
  });

  it("lists babies in the rail and switches channels", async () => {
    const second = familyBaby({
      id: "baby_2",
      name: "二宝",
      nickname: "",
      birthDate: "2026-10-01",
      age: { years: 0, months: 0, days: 28, totalMonths: 0 },
    });
    server.use(...familyHandlers({ overview: familyOverview({ babies: [familyBaby(), second] }) }));
    render(<FamilyPanel userId="user:mom" onShowPhoto={vi.fn()} />);
    const babies = await screen.findByRole("navigation", { name: "宝宝" });
    await userEvent.click(within(babies).getByRole("button", { name: /二宝/ }));
    expect(await screen.findByRole("heading", { name: "二宝" })).toBeTruthy();
    expect(screen.getAllByText("28 天").length).toBeGreaterThan(0);
  });
});

describe("summarize", () => {
  it("says one line per baby and one for join requests", () => {
    expect(
      summarize([
        { key: "a", kind: "vaccine", babyId: "b1", babyName: "小米粒", vaccine: "百白破疫苗", dose: 2 },
        { key: "b", kind: "vaccine", babyId: "b1", babyName: "小米粒", vaccine: "麻腮风疫苗", dose: 1 },
        { key: "c", kind: "vaccine", babyId: "b2", babyName: "二宝", vaccine: "乙肝疫苗", dose: 2 },
        { key: "d", kind: "join_request", accountId: "u", username: "uncle", role: "舅舅" },
      ]),
    ).toEqual([
      "小米粒有 2 剂疫苗可以预约接种",
      "二宝的乙肝疫苗第 2 剂快到接种时间了",
      "uncle想以“舅舅”的身份加入家庭",
    ]);
  });
});
