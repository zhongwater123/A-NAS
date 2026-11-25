import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import { isoDate } from "../lib/time";
import { forgetPeople, rememberPerson } from "../people/people";
import { session } from "../test/fixtures";
import { reminder, todo, todosHandlers } from "../test/handlers/todos";
import { apiError, http, server } from "../test/server";
import { forgetTodos, todoClient } from "./queries";
import { TodosPanel } from "./TodosPanel";

// daysFromToday is a date relative to the day the test runs.
const daysFromToday = (days: number) => {
  const date = new Date();
  date.setDate(date.getDate() + days);
  return isoDate(date);
};

// Radix places its menus with ResizeObserver and pointer capture; jsdom has neither.
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

// The NAS LAN entry is HTTP: getRandomValues exists, randomUUID does not.
const getRandomValues = crypto.getRandomValues.bind(crypto);
beforeEach(() => {
  vi.stubGlobal("crypto", { getRandomValues });
  setSession(
    session(
      { id: "user:mom", username: "mom", role: "member" },
      { csrfToken: "csrf", expiresAt: "2026-11-01T00:00:00Z" },
    ),
  );
  rememberPerson({ id: "user:dad", username: "dad", nickname: "爸爸" });
});

afterEach(() => {
  vi.unstubAllGlobals();
  forgetPeople();
  forgetTodos();
});

const rent = todo({ id: "todo_rent", title: "交物业费", due: daysFromToday(2), amountCents: 12850 });
const passport = todo({ id: "todo_passport", title: "换护照", category: "id_document", ownerId: "user:dad" });
const checkup = todo({
  id: "todo_checkup",
  title: "体检复查",
  category: "medical",
  due: daysFromToday(1),
  snoozedUntil: daysFromToday(3),
});
const insurance = todo({
  id: "todo_insurance",
  title: "续车险",
  category: "insurance",
  status: "done",
  closedAt: new Date(Date.now() - 3 * 86_400_000).toISOString(),
  closedById: "user:gone",
  closedByName: "grandpa",
});

describe("TodosPanel", () => {
  it("shows what is open, what waits and what was closed, the urgent marked", async () => {
    // In the server's order: open by due date, undated last, then closed.
    const served = todosHandlers({ todos: [checkup, rent, passport, insurance] });
    server.use(...served.handlers);
    render(<TodosPanel userId="user:mom" />);

    const open = await screen.findByRole("region", { name: "待处理" });
    const rows = within(open).getAllByRole("listitem");
    expect(rows.map((row) => row.querySelector("strong")?.textContent)).toEqual(["交物业费", "换护照"]);
    expect(within(rows[0]).getByText("还有 2 天").className).toMatch(/urgent/);
    expect(within(rows[0]).getByText("缴费 · 全家 · ¥128.50")).toBeTruthy();
    expect(within(rows[1]).getByText("不限日期")).toBeTruthy();
    expect(within(rows[1]).getByText("换证 · 爸爸")).toBeTruthy();

    const waiting = screen.getByRole("region", { name: "推迟中" });
    expect(within(waiting).getByText(/^推迟到/)).toBeTruthy();
    expect(within(waiting).getByText("体检复查")).toBeTruthy();

    // A deleted account is named as it was.
    const closed = screen.getByRole("region", { name: "最近处理" });
    expect(within(closed).getByText("续保 · 全家 · grandpa处理")).toBeTruthy();
    expect(within(closed).getByText("已处理 · 3 天前")).toBeTruthy();
  });

  it("says all is done when nothing is open", async () => {
    server.use(...todosHandlers({ todos: [] }).handlers);
    render(<TodosPanel userId="user:mom" />);
    expect(await screen.findByText("都处理完了")).toBeTruthy();
  });

  it("adds a to-do for the whole family once, with a request key and no scope", async () => {
    const served = todosHandlers({ todos: [] });
    server.use(...served.handlers);
    const user = userEvent.setup();
    render(<TodosPanel userId="user:mom" />);
    await user.click(await screen.findByRole("button", { name: "新建" }));
    const dialog = await screen.findByRole("dialog", { name: "新建待办" });
    const save = within(dialog).getByRole("button", { name: "保存" }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);

    await user.type(within(dialog).getByRole("textbox", { name: "什么事" }), "交这个月的物业管理费");
    expect(within(dialog).getByText(/10\/10 个字/)).toBeTruthy();
    await user.type(within(dialog).getByRole("textbox", { name: "什么事" }), "用");
    expect(save.disabled).toBe(true);
    await user.clear(within(dialog).getByRole("textbox", { name: "什么事" }));
    await user.type(within(dialog).getByRole("textbox", { name: "什么事" }), "交物业费");
    await user.selectOptions(within(dialog).getByRole("combobox", { name: "类别" }), "bill");
    await within(dialog).findByRole("option", { name: "爸爸" });
    await user.selectOptions(within(dialog).getByRole("combobox", { name: "谁的" }), "user:dad");
    await user.type(within(dialog).getByRole("textbox", { name: "金额（元）" }), "128.5");
    fireEvent.change(within(dialog).getByLabelText("截止日"), { target: { value: daysFromToday(5) } });
    await user.click(save);

    await waitFor(() => expect(served.sent.added).toHaveLength(1));
    const [body] = served.sent.added as Array<Record<string, unknown>>;
    expect(body).toEqual({
      requestKey: expect.stringMatching(/^[0-9a-f-]{36}$/),
      title: "交物业费",
      category: "bill",
      due: daysFromToday(5),
      ownerId: "user:dad",
      amountCents: 12850,
    });
    expect(body).not.toHaveProperty("scope");
    expect(screen.queryByRole("dialog")).toBeNull();
    const open = screen.getByRole("region", { name: "待处理" });
    expect(within(open).getByText("交物业费")).toBeTruthy();
  });

  it("marks done, ignores, snoozes and opens again", async () => {
    const served = todosHandlers({ todos: [rent, passport, insurance] });
    server.use(...served.handlers);
    const user = userEvent.setup();
    render(<TodosPanel userId="user:mom" />);

    await user.click(await screen.findByRole("button", { name: "已处理：交物业费" }));
    await waitFor(() => expect(served.sent.status).toEqual([{ todoId: "todo_rent", status: "done" }]));
    expect(await screen.findByText("已处理“交物业费”")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "更多操作：换护照" }));
    await user.click(await screen.findByRole("menuitem", { name: "推迟 7 天" }));
    await waitFor(() => expect(served.sent.snoozed).toEqual([{ todoId: "todo_passport", days: 7 }]));
    const waiting = await screen.findByRole("region", { name: "推迟中" });
    expect(within(waiting).getByText("换护照")).toBeTruthy();

    await user.click(within(waiting).getByRole("button", { name: "更多操作：换护照" }));
    await user.click(await screen.findByRole("menuitem", { name: "忽略" }));
    await waitFor(() => expect(served.sent.status).toContainEqual({ todoId: "todo_passport", status: "ignored" }));

    await user.click(screen.getByRole("button", { name: "重新打开：续车险" }));
    await waitFor(() => expect(served.sent.status).toContainEqual({ todoId: "todo_insurance", status: "open" }));
  });

  it("edits from the version it read, and reads again when someone saved first", async () => {
    const served = todosHandlers({ todos: [{ ...rent, version: 3 }] });
    server.use(...served.handlers);
    const user = userEvent.setup();
    render(<TodosPanel userId="user:mom" />);

    await user.click(await screen.findByRole("button", { name: "更多操作：交物业费" }));
    await user.click(await screen.findByRole("menuitem", { name: "编辑" }));
    const dialog = await screen.findByRole("dialog", { name: "编辑待办" });
    expect((within(dialog).getByRole("textbox", { name: "金额（元）" }) as HTMLInputElement).value).toBe("128.50");
    await user.clear(within(dialog).getByRole("textbox", { name: "金额（元）" }));
    await user.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(served.sent.updated).toHaveLength(1));
    expect(served.sent.updated[0]).toEqual({
      todoId: "todo_rent",
      body: { title: "交物业费", category: "bill", due: rent.due, version: 3 },
    });
    expect(await screen.findByText("已保存")).toBeTruthy();
    // The list is read again after the save; let that read finish first.
    await waitFor(() => expect(todoClient.isFetching()).toBe(0));

    let reads = 0;
    server.use(
      http.patch("/api/v1/todos/{todoId}", ({ response }) => response(409).json(apiError("changed"))),
      http.get("/api/v1/todos", ({ response }) => {
        reads += 1;
        return response(200).json({ items: [{ ...rent, title: "交水费", version: 5 }] });
      }),
    );
    await user.click(screen.getByRole("button", { name: "更多操作：交物业费" }));
    await user.click(await screen.findByRole("menuitem", { name: "编辑" }));
    await user.click(
      within(await screen.findByRole("dialog", { name: "编辑待办" })).getByRole("button", { name: "保存" }),
    );
    expect(await screen.findByText("别人刚改过这件事，已刷新，请再改一次")).toBeTruthy();
    expect(await screen.findByText("交水费")).toBeTruthy();
    expect(reads).toBe(1);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("edits a closed to-do where it is, keeping who closed it", async () => {
    const served = todosHandlers({ todos: [insurance] });
    server.use(...served.handlers);
    const user = userEvent.setup();
    render(<TodosPanel userId="user:mom" />);
    const closed = await screen.findByRole("region", { name: "最近处理" });
    await user.click(within(closed).getByRole("button", { name: "更多操作：续车险" }));
    await user.click(await screen.findByRole("menuitem", { name: "编辑" }));
    const dialog = await screen.findByRole("dialog", { name: "编辑待办" });
    await user.type(within(dialog).getByRole("textbox", { name: "金额（元）" }), "3200");
    await user.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(served.sent.updated).toHaveLength(1));
    expect(served.sent.status).toEqual([]);
    const after = screen.getByRole("region", { name: "最近处理" });
    expect(await within(after).findByText("续保 · 全家 · ¥3,200.00 · grandpa处理")).toBeTruthy();
  });

  it("offers only active accounts, and keeps the owner a to-do already has", async () => {
    // grandpa's account is disabled: still a profile, no longer an owner.
    rememberPerson({ id: "user:grandpa", username: "grandpa" });
    const theirs = todo({ id: "todo_theirs", title: "换老花镜", ownerId: "user:grandpa", ownerName: "grandpa" });
    const served = todosHandlers({ todos: [rent, theirs] });
    server.use(...served.handlers);
    const user = userEvent.setup();
    render(<TodosPanel userId="user:mom" />);

    await user.click(await screen.findByRole("button", { name: "新建" }));
    let dialog = await screen.findByRole("dialog", { name: "新建待办" });
    await within(dialog).findByRole("option", { name: "爸爸" });
    const offered = (box: HTMLElement) =>
      within(within(box).getByRole("combobox", { name: "谁的" }))
        .getAllByRole("option")
        .map((option) => option.textContent);
    expect(offered(dialog)).toEqual(["全家", "爸爸", "mom"]);
    await user.click(within(dialog).getByRole("button", { name: "取消" }));

    await user.click(screen.getByRole("button", { name: "更多操作：换老花镜" }));
    await user.click(await screen.findByRole("menuitem", { name: "编辑" }));
    dialog = await screen.findByRole("dialog", { name: "编辑待办" });
    await within(dialog).findByRole("option", { name: "爸爸" });
    expect(offered(dialog)).toEqual(["全家", "爸爸", "mom", "grandpa"]);
    expect((within(dialog).getByRole("combobox", { name: "谁的" }) as HTMLSelectElement).value).toBe("user:grandpa");
  });

  it("acknowledges a reminder from the window", async () => {
    const due = reminder({ todoId: "todo_rent", due: rent.due });
    const served = todosHandlers({ todos: [rent], reminders: [due] });
    server.use(...served.handlers);
    const user = userEvent.setup();
    render(<TodosPanel userId="user:mom" />);
    const reminders = await screen.findByRole("region", { name: "要提醒的" });
    expect(within(reminders).getByText("还有 2 天")).toBeTruthy();
    await user.click(within(reminders).getByRole("button", { name: "知道了：交物业费" }));
    await waitFor(() => expect(served.sent.acknowledged).toEqual([[due.key]]));
    expect(screen.queryByRole("region", { name: "要提醒的" })).toBeNull();
  });

  it("shows the to-do another window asks for, and hands the request back", async () => {
    server.use(...todosHandlers({ todos: [rent, passport] }).handlers);
    const intent = { todoId: "todo_passport" };
    const onIntentHandled = vi.fn();
    render(<TodosPanel userId="user:mom" intent={intent} onIntentHandled={onIntentHandled} />);
    await waitFor(() => expect(onIntentHandled).toHaveBeenCalledWith(intent));
    const row = (await screen.findByText("换护照")).closest("li");
    await waitFor(() => expect(row?.getAttribute("aria-current")).toBe("true"));
  });

  it("explains a disk that is not ready in its own words", async () => {
    server.use(
      http.get("/api/v1/todos", ({ response }) => response(503).json(apiError("family_unavailable"))),
      http.get("/api/v1/todos/reminders", ({ response }) => response(503).json(apiError("family_unavailable"))),
    );
    render(<TodosPanel userId="user:mom" />);
    expect(await screen.findByText("待办暂时不可用（数据盘未就绪），请稍后再试")).toBeTruthy();
    // Only the window's own heading; how the to-dos work is in the spec, not under the title.
    expect(screen.getByRole("heading", { name: "待办" }).nextElementSibling?.tagName).toBe("BUTTON");
    expect(screen.getByRole("button", { name: "重试" })).toBeTruthy();
  });
});
