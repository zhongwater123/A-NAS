import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import type { FamilyRecordInput } from "../api.generated";
import { session } from "../test/fixtures";
import { emptySchedule, familyBaby, familyHandlers, familyOverview, familyRecord } from "../test/handlers/family";
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

// Radix places its menus with ResizeObserver and pointer capture, which jsdom
// does not have.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
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
    expect(await screen.findByRole("heading", { name: /小米粒/ })).toBeTruthy();
    expect(screen.getByText("7 个月 14 天")).toBeTruthy();
    expect(await screen.findByText("百白破疫苗第 2 剂可以接种了")).toBeTruthy();
    expect(await screen.findByText("今天第一次笑出了声")).toBeTruthy();
    const measurement = screen.getByText("身长").closest("article")!;
    expect(within(measurement).getByText(/7.20/)).toBeTruthy();
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
    await screen.findByText("还没有任何记录");
    await userEvent.click(screen.getByRole("button", { name: "记一笔" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "日志" }));
    const dialog = await screen.findByRole("dialog", { name: "记录日志" });
    await userEvent.type(within(dialog).getByRole("textbox", { name: "写点什么" }), "外婆来看你了");
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

  it("asks a guardian to add the first baby", async () => {
    server.use(...familyHandlers({ overview: familyOverview({ babies: [] }) }));
    render(<FamilyPanel userId="user:mom" onShowPhoto={vi.fn()} />);
    expect(await screen.findByRole("heading", { name: "添加第一个宝宝" })).toBeTruthy();
  });

  it("lists babies in the sidebar and switches channels", async () => {
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
