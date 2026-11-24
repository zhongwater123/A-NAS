import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";

import { isoDate } from "../lib/time";
import { reminder, todosHandlers } from "../test/handlers/todos";
import { apiError, http, server } from "../test/server";
import { forgetTodos } from "./queries";
import { TodoNotices } from "./TodoNotices";

// A test that wakes the local console tells who listens, as the screen saver does.
const woken = new Set<() => void>();
const onWake = (listener: () => void) => {
  woken.add(listener);
  return () => woken.delete(listener);
};

afterEach(() => forgetTodos());

// The page comes back into view. A browser's visibilitychange bubbles up to
// window, where TanStack Query listens.
const pageShown = () => document.dispatchEvent(new Event("visibilitychange", { bubbles: true }));

const daysFromToday = (days: number) => {
  const date = new Date();
  date.setDate(date.getDate() + days);
  return isoDate(date);
};

const rent = reminder({ todoId: "rent", title: "交物业费", due: daysFromToday(2) });
const water = reminder({ todoId: "water", title: "交水费", due: daysFromToday(0), stage: "today" });
const car = reminder({
  todoId: "car",
  title: "续车险",
  category: "insurance",
  due: daysFromToday(-1),
  stage: "overdue",
});
const passport = reminder({ todoId: "passport", title: "换护照", category: "id_document", due: daysFromToday(3) });

describe("TodoNotices", () => {
  it("tells what is due and acknowledges only the reminders it shows", async () => {
    const served = todosHandlers({ reminders: [car, water, rent, passport] });
    server.use(...served.handlers);
    const user = userEvent.setup();
    render(<TodoNotices userId="user:mom" shared={false} onOpen={vi.fn()} />);
    const notice = await screen.findByRole("status", { name: "待办提醒" });
    expect(notice.closest("[data-vnas-overlay]")).toBeTruthy();
    expect(within(notice).getByText("续车险").nextSibling?.textContent).toBe("已过期 1 天");
    expect(within(notice).getByText("交水费").nextSibling?.textContent).toBe("今天截止");
    expect(within(notice).getByText("交物业费").nextSibling?.textContent).toBe("还有 2 天");
    expect(within(notice).getByText("另有 1 件")).toBeTruthy();
    expect(within(notice).queryByText("换护照")).toBeNull();

    await user.click(within(notice).getByRole("button", { name: "知道了" }));
    await waitFor(() => expect(served.sent.acknowledged).toEqual([[car.key, water.key, rent.key]]));
    // The one it had not shown comes up next.
    const next = await screen.findByRole("status", { name: "待办提醒" });
    expect(within(next).getByText("换护照")).toBeTruthy();
    expect(within(next).queryByText("另有 1 件")).toBeNull();
  });

  it("hides itself after a while on this page alone, unless pointed at", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const served = todosHandlers({ reminders: [rent] });
    server.use(...served.handlers);
    render(<TodoNotices userId="user:mom" shared={false} onOpen={vi.fn()} />);
    const notice = await screen.findByRole("status", { name: "待办提醒" });
    const holder = notice.parentElement as HTMLElement;
    fireEvent.pointerEnter(holder);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000);
    });
    expect(screen.getByRole("status", { name: "待办提醒" })).toBeTruthy();
    fireEvent.pointerLeave(holder);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(16_000);
    });
    expect(screen.queryByRole("status", { name: "待办提醒" })).toBeNull();
    expect(served.sent.acknowledged).toEqual([]);
  });

  it("is not held by a pointer that was over the notice it closed", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const served = todosHandlers({ reminders: [rent] });
    server.use(...served.handlers);
    render(<TodoNotices userId="user:mom" shared={false} onOpen={vi.fn()} />);
    const notice = await screen.findByRole("status", { name: "待办提醒" });
    fireEvent.pointerEnter(notice.parentElement as HTMLElement);
    fireEvent.click(within(notice).getByRole("button", { name: "关闭" }));
    expect(screen.queryByRole("status", { name: "待办提醒" })).toBeNull();

    server.use(...todosHandlers({ reminders: [rent, water] }).handlers);
    await act(async () => {
      pageShown();
    });
    expect(await screen.findByText("交水费")).toBeTruthy();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(16_000);
    });
    expect(screen.queryByRole("status", { name: "待办提醒" })).toBeNull();
  });

  it("on the local console shows again what hid itself when someone wakes the screen", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const served = todosHandlers({ reminders: [rent] });
    server.use(...served.handlers);
    render(<TodoNotices userId="user:mom" shared onWake={onWake} onOpen={vi.fn()} />);
    await screen.findByRole("status", { name: "待办提醒" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(16_000);
    });
    expect(screen.queryByRole("status", { name: "待办提醒" })).toBeNull();

    act(() => woken.forEach((listener) => listener()));
    expect(await screen.findByRole("status", { name: "待办提醒" })).toBeTruthy();
    expect(served.sent.acknowledged).toEqual([]);
  });

  it("closes, or opens the window, without acknowledging", async () => {
    const served = todosHandlers({ reminders: [rent, water] });
    server.use(...served.handlers);
    const onOpen = vi.fn();
    const user = userEvent.setup();
    const { unmount } = render(<TodoNotices userId="user:mom" shared={false} onOpen={onOpen} />);
    await user.click(await screen.findByRole("button", { name: /去看看/ }));
    expect(onOpen).toHaveBeenCalledWith("rent");
    expect(screen.queryByRole("status", { name: "待办提醒" })).toBeNull();
    unmount();

    // The next visit tells them again.
    render(<TodoNotices userId="user:mom" shared={false} onOpen={onOpen} />);
    const notice = await screen.findByRole("status", { name: "待办提醒" });
    await user.click(within(notice).getByRole("button", { name: "关闭" }));
    expect(screen.queryByRole("status", { name: "待办提醒" })).toBeNull();
    expect(served.sent.acknowledged).toEqual([]);
  });

  it("on the family's shared screen names a medical matter by its category and private ones by count", async () => {
    const checkup = reminder({ todoId: "checkup", title: "儿童医院复查", category: "medical", due: daysFromToday(1) });
    const own = reminder({ todoId: "own", title: "还信用卡", scope: "private", due: daysFromToday(2) });
    server.use(...todosHandlers({ reminders: [checkup, own] }).handlers);
    render(<TodoNotices userId="user:mom" shared onWake={onWake} onOpen={vi.fn()} />);
    const notice = await screen.findByRole("status", { name: "待办提醒" });
    expect(within(notice).getByText("一件医疗的事")).toBeTruthy();
    expect(within(notice).getByText("你有 1 件自己的待办要看")).toBeTruthy();
    expect(screen.queryByText(/儿童医院/)).toBeNull();
    expect(screen.queryByText(/信用卡/)).toBeNull();
  });

  it("shows nothing of the account signed in before, even when the next one's read fails", async () => {
    server.use(...todosHandlers({ reminders: [rent] }).handlers);
    const { rerender } = render(<TodoNotices userId="user:mom" shared={false} onOpen={vi.fn()} />);
    await screen.findByRole("status", { name: "待办提醒" });
    // dad signs in on the same page while the disk is not ready.
    server.use(
      http.get("/api/v1/todos/reminders", ({ response }) => response(503).json(apiError("family_unavailable"))),
    );
    rerender(<TodoNotices userId="user:dad" shared={false} onOpen={vi.fn()} />);
    expect(screen.queryByRole("status", { name: "待办提醒" })).toBeNull();
    await act(async () => {
      pageShown();
    });
    expect(screen.queryByRole("status", { name: "待办提醒" })).toBeNull();
  });

  it("shows nothing once the session is gone", async () => {
    server.use(...todosHandlers({ reminders: [rent] }).handlers);
    render(<TodoNotices userId="user:mom" shared={false} onOpen={vi.fn()} />);
    await screen.findByRole("status", { name: "待办提醒" });
    server.use(
      http.get("/api/v1/todos/reminders", () =>
        HttpResponse.json(apiError("authentication_required"), { status: 401 }),
      ),
    );
    await act(async () => {
      pageShown();
    });
    await waitFor(() => expect(screen.queryByRole("status", { name: "待办提醒" })).toBeNull());
  });

  it("does not bring back what was acknowledged with a read that left before", async () => {
    const acknowledged = new Set<string>();
    let gate: Promise<void> | undefined;
    let open = () => {};
    server.use(
      http.get("/api/v1/todos/reminders", async ({ response }) => {
        // What the server lists when the read arrives, answered when the gate opens.
        const items = [rent, water].filter((item) => !acknowledged.has(item.key));
        await gate;
        return response(200).json({ items });
      }),
      http.post("/api/v1/todos/reminders/seen", async ({ request, response }) => {
        for (const key of (await request.json()).keys) acknowledged.add(key);
        return response(204).empty();
      }),
    );
    const user = userEvent.setup();
    render(<TodoNotices userId="user:mom" shared={false} onOpen={vi.fn()} />);
    const notice = await screen.findByRole("status", { name: "待办提醒" });
    gate = new Promise((resolve) => (open = resolve));
    // The page comes back into view: a read leaves, and waits.
    await act(async () => {
      pageShown();
    });
    await user.click(within(notice).getByRole("button", { name: "知道了" }));
    await waitFor(() => expect(acknowledged.size).toBe(2));
    expect(screen.queryByRole("status", { name: "待办提醒" })).toBeNull();
    await act(async () => {
      open();
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
    expect(screen.queryByRole("status", { name: "待办提醒" })).toBeNull();
  });

  it("reads again when the page comes back and just after midnight, without polling", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(new Date(2026, 10, 17, 23, 58));
    let reads = 0;
    server.use(
      http.get("/api/v1/todos/reminders", ({ response }) => {
        reads += 1;
        return response(200).json({ items: [] });
      }),
    );
    render(<TodoNotices userId="user:mom" shared={false} onOpen={vi.fn()} />);
    await waitFor(() => expect(reads).toBe(1));
    // The rest of the day reads nothing more.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(reads).toBe(1);
    // Just past midnight the stages move on.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2 * 60_000);
    });
    await waitFor(() => expect(reads).toBe(2));
    await act(async () => {
      pageShown();
    });
    await waitFor(() => expect(reads).toBe(3));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30 * 60_000);
    });
    expect(reads).toBe(3);
  });
});
