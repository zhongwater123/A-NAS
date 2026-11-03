import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse } from "msw";
import { beforeEach, expect, it, vi } from "vitest";

import type { Notification } from "../api.generated";
import { loadTaskNotices, setTaskNoticeMember } from "../assistant/taskNotices";
import { noticesHandlers, taskNotice } from "../test/handlers/assistant";
import { http, server } from "../test/server";
import { TaskInbox } from "./TaskInbox";

const run = (status = "ok") =>
  taskNotice("new", {
    taskId: "aaaaaaaaaaaa",
    name: "交材料",
    status,
    ranAt: "2026-11-03T10:00:00+08:00",
    sessionId: "cron_aaaaaaaaaaaa_20261103_100000",
    summary: "**该交材料了**",
  });
beforeEach(() => setTaskNoticeMember("reset"));

it("shows existing unread results without replaying a celebration; opening does not claim the human task is done", async () => {
  const notices = noticesHandlers([run()]);
  server.use(...notices.handlers);
  const onSignal = vi.fn(),
    onOpen = vi.fn(),
    onRemindAgain = vi.fn();
  render(<TaskInbox userId="a" enabled announce onSignal={onSignal} onOpen={onOpen} onRemindAgain={onRemindAgain} />);
  await screen.findByText("该交材料了");
  expect(onSignal.mock.calls.some(([value]) => value?.phase === "success")).toBe(false);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "查看结果" }));
  expect(onOpen).toHaveBeenCalledWith("cron_aaaaaaaaaaaa_20261103_100000");
  await user.click(screen.getByRole("button", { name: "十分钟后再提醒" }));
  expect(onRemindAgain).toHaveBeenCalledWith(expect.stringContaining("不要重新运行原任务"));
  expect(notices.acknowledged).toEqual([]);
  await user.click(screen.getByRole("button", { name: "知道了" }));
  await waitFor(() => expect(notices.acknowledged).toEqual(["new"]));
  expect(screen.queryByRole("button", { name: "知道了" })).toBeNull();
});

it("announces a new failed server run once and does not turn it into success", async () => {
  let items: Notification[] = [];
  const loaded = vi.fn();
  server.use(
    http.get("/api/v1/notifications", ({ response }) => {
      loaded();
      return response(200).json({ items });
    }),
  );
  const onSignal = vi.fn();
  render(<TaskInbox userId="a" enabled announce onSignal={onSignal} />);
  await waitFor(() => expect(loaded).toHaveBeenCalled());
  await act(loadTaskNotices); // Finish the empty initial snapshot first.
  onSignal.mockClear();
  items = [run("error")];
  await act(loadTaskNotices);
  expect(await screen.findByText("任务没有完成 · 交材料")).toBeVisible();
  await waitFor(() => expect(onSignal).toHaveBeenCalledWith(expect.objectContaining({ id: "new", phase: "error" })));
  onSignal.mockClear();
  await act(loadTaskNotices);
  expect(onSignal).not.toHaveBeenCalled();
});

it("keeps the card and offers retry when the receipt cannot be saved", async () => {
  server.use(...noticesHandlers([run()]).handlers);
  server.use(http.post("/api/v1/notifications/{notificationId}/acknowledge", () => HttpResponse.error()));
  render(<TaskInbox userId="a" enabled announce onSignal={() => undefined} />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "知道了" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("结果仍会保留");
  expect(screen.getByRole("button", { name: "知道了" })).toBeEnabled();
});
