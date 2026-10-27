import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { assistantTask, noticesHandlers, taskNotice, tasksHandlers } from "../test/handlers/assistant";
import { http, server } from "../test/server";
import { scheduledPrompt } from "./AssistantPanel";
import { TasksView } from "./TasksView";
import { plainText, TaskToasts } from "./TaskToasts";

const seconds = (date: Date) => date.getTime() / 1000;
const today = (hour: number, minute = 0) => {
  const date = new Date();
  date.setHours(hour, minute, 0, 0);
  return date;
};

const daily = assistantTask("aaaaaaaaaaaa", {
  name: "每日小结",
  prompt: "总结共享空间昨天新增的文件",
  schedule: { kind: "cron", expr: "0 8 * * *", display: "0 8 * * *" },
  state: "scheduled",
  nextRunAt: seconds(today(8)) + 86400,
  lastRunAt: seconds(today(8, 0)),
  lastStatus: "ok",
  completed: 3,
});
const photos = assistantTask("bbbbbbbbbbbb", {
  name: "整理新照片",
  prompt: "把新照片按地点归类，列给我",
  schedule: { kind: "interval", minutes: 360, display: "every 360m" },
  state: "paused",
  lastRunAt: seconds(today(2)),
  lastStatus: "error",
  lastError: "模型没有响应",
  completed: 1,
});

describe("TasksView", () => {
  it("shows each task's schedule and last run, and opens a run as a conversation", async () => {
    const resumed: string[] = [];
    server.use(
      ...tasksHandlers([photos, daily], {
        [daily.id]: [
          {
            id: "cron_aaaaaaaaaaaa_20261020_080000",
            startedAt: seconds(today(8)),
            endedAt: seconds(today(8)) + 12,
            messageCount: 4,
          },
        ],
      }),
      ...noticesHandlers().handlers,
      http.post("/api/v1/assistant/tasks/{taskId}/resume", ({ params, response }) => {
        resumed.push(params.taskId);
        return response(200).json({ task: { ...photos, state: "scheduled", nextRunAt: seconds(today(23)) } });
      }),
    );
    const onOpenRun = vi.fn();
    const user = userEvent.setup();
    render(<TasksView onOpenRun={onOpenRun} onPlan={() => undefined} />);

    const cards = await screen.findAllByRole("listitem");
    // A task that runs comes before a paused one.
    expect(within(cards[0]).getByText("每日小结")).toBeTruthy();
    expect(within(cards[0]).getByText("每天 08:00")).toBeTruthy();
    expect(within(cards[0]).getByText("已开启")).toBeTruthy();
    expect(within(cards[0]).getByText(/今天 08:00 · 完成/)).toBeTruthy();
    expect(within(cards[1]).getByText("每 6 小时")).toBeTruthy();
    expect(within(cards[1]).getByText("暂停中，不会运行")).toBeTruthy();
    expect(within(cards[1]).getByText("模型没有响应")).toBeTruthy();

    await user.click(within(cards[0]).getByRole("button", { name: /查看结果/ }));
    expect(onOpenRun).toHaveBeenCalledWith("cron_aaaaaaaaaaaa_20261020_080000", "每日小结 · 今天 08:00");

    await user.click(within(cards[1]).getByRole("button", { name: "继续" }));
    await waitFor(() => expect(screen.getAllByText("已开启")).toHaveLength(2));
    expect(resumed).toEqual([photos.id]);
  });

  it("marks a task whose result the member has not read", async () => {
    server.use(
      ...tasksHandlers([daily, photos]),
      ...noticesHandlers([
        taskNotice("n9", { taskId: daily.id, name: daily.name, status: "ok", ranAt: today(8).toISOString() }),
      ]).handlers,
    );
    render(<TasksView onOpenRun={() => undefined} onPlan={() => undefined} />);
    const cards = await screen.findAllByRole("listitem");
    await waitFor(() => expect(within(cards[0]).getByText("新结果")).toBeTruthy());
    expect(within(cards[1]).queryByText("新结果")).toBeNull();
  });

  it("deletes a task only once the member confirms, runs included", async () => {
    const deleted: string[] = [];
    server.use(
      ...tasksHandlers([daily]),
      ...noticesHandlers().handlers,
      http.delete("/api/v1/assistant/tasks/{taskId}", ({ params, response }) => {
        deleted.push(params.taskId);
        return response(204).empty();
      }),
    );
    const user = userEvent.setup();
    render(<TasksView onOpenRun={() => undefined} onPlan={() => undefined} />);
    await user.click(await screen.findByRole("button", { name: "删除任务" }));
    expect(screen.getByText(/运行记录也会一起删除/)).toBeTruthy();
    expect(deleted).toEqual([]);
    await user.click(screen.getByRole("button", { name: "删除" }));
    expect(await screen.findByText("已删除“每日小结”")).toBeTruthy();
    expect(deleted).toEqual([daily.id]);
  });

  it("suggests tasks to ask for when there are none", async () => {
    server.use(...tasksHandlers([]), ...noticesHandlers().handlers);
    const onPlan = vi.fn();
    const user = userEvent.setup();
    render(<TasksView onOpenRun={() => undefined} onPlan={onPlan} />);
    expect(await screen.findByText("还没有定时任务")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: /今天下午 3 点提醒我交电费/ }));
    expect(onPlan).toHaveBeenCalledWith("今天下午 3 点提醒我交电费");
  });
});

describe("TaskToasts", () => {
  it("tells of finished runs only, and reading one acknowledges it", async () => {
    const notices = noticesHandlers([
      {
        id: "n1",
        kind: "admin_viewing",
        actorUsername: "owner",
        reason: "恢复文件",
        createdAt: new Date().toISOString(),
      },
      taskNotice("n2", {
        taskId: "aaaaaaaaaaaa",
        name: "每日小结",
        status: "ok",
        ranAt: today(8).toISOString(),
        sessionId: "cron_aaaaaaaaaaaa_20261020_080000",
        summary: "## 今日小结 共享空间新增 **3** 个文件",
      }),
    ]);
    server.use(...notices.handlers);
    const onOpen = vi.fn();
    const user = userEvent.setup();
    render(<TaskToasts enabled onOpen={onOpen} />);
    await screen.findByText("今日小结 共享空间新增 3 个文件");
    const toast = screen.getByRole("status", { name: "定时任务 每日小结" });
    expect(within(toast).getByText(/VNAS 定时任务 · 今天 08:00/)).toBeTruthy();
    expect(screen.queryByText(/恢复文件/)).toBeNull();
    await user.click(within(toast).getByRole("button", { name: /查看结果/ }));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ sessionId: "cron_aaaaaaaaaaaa_20261020_080000" }));
    expect(screen.queryByRole("status", { name: "定时任务 每日小结" })).toBeNull();
    await waitFor(() => expect(notices.acknowledged).toEqual(["n2"]));
  });

  it("asks nothing when the member has no assistant", async () => {
    const asked = vi.fn();
    server.use(
      http.get("/api/v1/notifications", ({ response }) => {
        asked();
        return response(200).json({ items: [] });
      }),
    );
    render(<TaskToasts enabled={false} onOpen={() => undefined} />);
    // A request would have been sent by now: the client sends it a few promises later.
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(asked).not.toHaveBeenCalled();
  });
});

describe("plainText", () => {
  it("drops Markdown marks but keeps names with underscores", () => {
    expect(plainText("## 结果\n- 找到 `IMG_2031.jpg`\n- 见 [相册](https://x)")).toBe("结果 找到 IMG_2031.jpg 见 相册");
  });
});

describe("scheduledPrompt", () => {
  const hint =
    '[IMPORTANT: You are running as a scheduled cron job. SILENT: respond with exactly "[SILENT]" (nothing else). FAILURE: put [CRON_FAILURE] on the first line.]';
  it("takes the task's prompt from the start of a run", () => {
    expect(
      scheduledPrompt({ id: "1", role: "user", parts: [{ type: "text", text: `${hint}\n\n提醒成员：给妈妈打电话` }] }),
    ).toBe("提醒成员：给妈妈打电话");
  });
  it("leaves what the member wrote alone", () => {
    expect(
      scheduledPrompt({ id: "2", role: "user", parts: [{ type: "text", text: "[IMPORTANT: 这是我写的]\n\n你好" }] }),
    ).toBeUndefined();
    expect(
      scheduledPrompt({ id: "3", role: "assistant", parts: [{ type: "text", text: `${hint}\n\nx` }] }),
    ).toBeUndefined();
  });
});
