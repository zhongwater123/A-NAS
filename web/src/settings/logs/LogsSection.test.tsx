import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Session } from "../../api.generated";
import { type LogsOptions, logsHandlers } from "../../test/handlers/logs";
import { server } from "../../test/server";
import type { ActivityEvent, SystemLogEntry } from "./api";
import { forgetRememberedFilters } from "./common";
import { LogsSection } from "./LogsSection";

const admin: Session = {
  csrfToken: "csrf",
  expiresAt: "2026-10-12T22:00:00Z",
  user: {
    id: "user:owner",
    username: "owner",
    role: "admin",
    status: "active",
    createdAt: "2026-10-01T00:00:00Z",
    mustChangePassword: false,
  },
};
const member: Session = {
  ...admin,
  user: {
    id: "user:alice",
    username: "alice",
    role: "member",
    status: "active",
    createdAt: "2026-10-01T00:00:00Z",
    mustChangePassword: false,
  },
};
const alice = { id: "user:alice", username: "alice" };
const owner = { id: "user:owner", username: "owner" };

const today = (hours: number, minutes = 0) => new Date(new Date().setHours(hours, minutes, 0, 0)).toISOString();

const activity: ActivityEvent[] = [
  {
    id: "audit:9",
    occurredAt: today(0, 9),
    category: "session",
    action: "session.sign_in_failed",
    resourceType: "user",
    detail: '{"via":"lan","address":"172.18.45.99","username":"root","reason":"unknown_user"}',
  },
  {
    id: "audit:8",
    occurredAt: today(0, 8),
    category: "file",
    action: "file.uploaded",
    actor: alice,
    resourceType: "file",
    resourceId: "entry:3",
    detail: "c.jpg",
    space: { id: "space:shared", kind: "shared" },
  },
  {
    id: "audit:7",
    occurredAt: today(0, 7),
    category: "file",
    action: "file.uploaded",
    actor: alice,
    resourceType: "file",
    resourceId: "entry:2",
    detail: "b.jpg",
    space: { id: "space:shared", kind: "shared" },
  },
  {
    id: "audit:6",
    occurredAt: today(0, 6),
    category: "file",
    action: "file.uploaded",
    actor: alice,
    resourceType: "file",
    resourceId: "entry:1",
    detail: "a.jpg",
    space: { id: "space:shared", kind: "shared" },
  },
  {
    id: "audit:5",
    occurredAt: today(0, 5),
    category: "file",
    action: "file.uploaded",
    actor: alice,
    resourceType: "file",
    redacted: true,
    space: { id: "space:alice", kind: "private", owner: alice },
  },
  {
    id: "audit:4",
    occurredAt: today(0, 4),
    category: "session",
    action: "session.signed_in",
    actor: alice,
    subject: alice,
    resourceType: "user",
    resourceId: "user:alice",
    detail: '{"via":"lan","address":"172.18.45.20"}',
  },
];

const older: ActivityEvent = {
  id: "audit:1",
  occurredAt: "2026-01-05T08:00:00Z",
  category: "account",
  action: "user.created",
  actor: owner,
  subject: alice,
  resourceType: "user",
  resourceId: "user:alice",
  detail: "member",
};

const logLine = (cursor: string, minutes: number, overrides: Partial<SystemLogEntry> = {}): SystemLogEntry => ({
  cursor,
  time: today(0, minutes),
  service: "api",
  unit: "anas-api.service",
  level: "info",
  message: "A-NAS API listening",
  pid: 671,
  ...overrides,
});

const systemLines = [
  logLine("s=1;i=3", 30, {
    service: "photos",
    unit: "anas-photos.service",
    level: "error",
    message: "photo reconciliation failed",
    fields: { error: "read-only file system" },
  }),
  logLine("s=1;i=2", 20, { level: "warning", message: "photo service unreachable" }),
  logLine("s=1;i=1", 10),
];

// serve answers both logs from options, with both accounts in the member
// filter; it returns the queries of the reads.
function serve(options: Omit<LogsOptions, "users"> = {}) {
  const { handlers, sent } = logsHandlers({ users: [admin.user, member.user], ...options });
  server.use(...handlers);
  return sent;
}

// sentence matches the element that holds text whole, as a row's one-line
// description does, not the parts it is built from.
const sentence = (text: string) => (_: string, element: Element | null) =>
  element?.textContent === text && !Array.from(element.children).some((child) => child.textContent === text);

beforeEach(() => forgetRememberedFilters());
afterEach(() => {
  vi.useRealTimers();
});

describe("activity log", () => {
  it("shows a day-grouped timeline with merged uploads, hidden names and details", async () => {
    serve({ activity: () => ({ items: activity, scope: "all" }) });
    const user = userEvent.setup();
    render(<LogsSection session={admin} />);
    expect(screen.getByRole("heading", { name: "日志" })).toBeTruthy();
    const todayRegion = await screen.findByRole("region", { name: /^今天/ });
    const rows = within(todayRegion).getAllByRole("listitem");
    const sentences = [
      "有人尝试以root登录，失败",
      "alice上传了 3 个文件",
      "alice在个人空间上传了 1 个文件",
      "alice登录了",
    ];
    expect(rows).toHaveLength(sentences.length);
    rows.forEach((row, index) => expect(within(row).getByText(sentence(sentences[index]))).toBeTruthy());
    expect(rows[0].textContent).toContain("账号不存在 · 局域网 · 172.18.45.99");
    expect(rows[1].textContent).toContain("c.jpg、b.jpg、a.jpg");
    expect(rows[2].textContent).not.toContain(".pdf");

    await user.click(within(rows[2]).getByRole("button"));
    expect(within(rows[2]).getByText("其他成员个人空间中的内容，名称已隐藏")).toBeTruthy();
    await user.click(within(rows[1]).getByRole("button", { expanded: false }));
    const items = within(rows[1]).getAllByRole("button", { expanded: false });
    await user.click(items[0]);
    expect(within(rows[1]).getByText("entry:3")).toBeTruthy();
    expect(within(rows[1]).getByText("file.uploaded")).toBeTruthy();
    expect(screen.getByText(/相册中的操作只有本人能看到/)).toBeTruthy();
  });

  it("filters by category, account and text and pages back with the cursor", async () => {
    const sent = serve({
      activity: (params) =>
        params.get("before")
          ? { items: [older], scope: "all" }
          : {
              items: activity.filter((item) => !params.get("category") || item.category === params.get("category")),
              next: params.get("category") ? undefined : "cursor-1",
              scope: "all",
            },
    });
    const user = userEvent.setup();
    render(<LogsSection session={admin} />);
    await screen.findByRole("region", { name: /^今天/ });

    await user.click(screen.getByRole("button", { name: "加载更早的记录" }));
    expect(await screen.findByText("没有更早的记录了")).toBeTruthy();
    expect(sent.activity.at(-1)?.get("before")).toBe("cursor-1");
    expect(screen.getByRole("region", { name: /1月5日/ }).textContent).toContain("添加了成员");

    await user.click(screen.getByRole("radio", { name: "登录" }));
    await waitFor(() => expect(sent.activity.at(-1)?.get("category")).toBe("session"));
    await user.selectOptions(screen.getByRole("combobox", { name: "成员" }), "alice");
    await waitFor(() => expect(sent.activity.at(-1)?.get("actor")).toBe("user:alice"));
    await user.selectOptions(screen.getByRole("combobox", { name: "时间范围" }), "近 7 天");
    await waitFor(() => expect(sent.activity.at(-1)?.get("since")).toBeTruthy());
    await user.type(screen.getByRole("searchbox", { name: "搜索活动记录" }), "172.18");
    await waitFor(() => expect(sent.activity.at(-1)?.get("q")).toBe("172.18"));
  });

  it("says which kinds of records could not be read and offers to clear filters when empty", async () => {
    serve({
      activity: (params) => ({
        items: params.get("category") ? [] : activity.slice(-1),
        scope: "all",
        unavailable: ["photo"],
      }),
    });
    const user = userEvent.setup();
    render(<LogsSection session={admin} />);
    expect(await screen.findByText("相册的活动暂时无法读取，其余记录照常显示。")).toBeTruthy();
    await user.click(screen.getByRole("radio", { name: "存储" }));
    expect(await screen.findByRole("heading", { name: "没有符合条件的记录" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "清除筛选" }));
    expect(screen.getByRole("radio", { name: "全部" }).getAttribute("aria-checked")).toBe("true");
  });

  it("exports the filtered records as CSV that Excel opens", async () => {
    serve({
      activity: (params) =>
        params.get("before") ? { items: [older], scope: "all" } : { items: activity, next: "cursor-1", scope: "all" },
    });
    const blobs: Blob[] = [];
    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      value: (blob: Blob) => {
        blobs.push(blob);
        return "blob:export";
      },
    });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: () => undefined });
    const user = userEvent.setup();
    render(<LogsSection session={admin} />);
    await screen.findByRole("region", { name: /^今天/ });
    await user.click(screen.getByRole("button", { name: "导出" }));
    expect(await screen.findByText("已导出 7 条记录。")).toBeTruthy();
    expect([...new Uint8Array(await blobs[0].arrayBuffer()).slice(0, 3)]).toEqual([0xef, 0xbb, 0xbf]);
    const csv = await blobs[0].text();
    expect(csv).toContain('"时间","类别","成员","描述","来源","操作代码","对象类型","对象 ID","记录 ID"');
    expect(csv).toContain('"有人尝试以 root 登录，失败"');
    expect(csv).toContain('"user.created"');
  });

  it("shows members only their own activity, without the service logs", async () => {
    const sent = serve({ activity: () => ({ items: activity.slice(-1), scope: "self" }) });
    render(<LogsSection session={member} />);
    expect(screen.getByRole("heading", { name: "我的活动" })).toBeTruthy();
    expect(await screen.findByText("你")).toBeTruthy();
    expect(screen.queryByRole("tablist")).toBeNull();
    expect(screen.queryByRole("combobox", { name: "成员" })).toBeNull();
    expect(screen.queryByText(/相册中的操作只有本人能看到/)).toBeNull();
    expect(sent.users).toBe(0);
    expect(sent.systemLogs).toHaveLength(0);
  });

  it("opens on sign-ins when asked from My account", async () => {
    const sent = serve({ activity: () => ({ items: [], scope: "all" }) });
    render(<LogsSection session={admin} preset={{ category: "session", id: 1 }} />);
    await waitFor(() => expect(sent.activity.at(-1)?.get("category")).toBe("session"));
    expect(screen.getByRole("radio", { name: "登录" }).getAttribute("aria-checked")).toBe("true");
  });
});

describe("system log", () => {
  it("lists service lines with levels and fields and filters by level and service", async () => {
    const sent = serve({
      systemLogs: (params) => ({
        items: systemLines.filter((line) => params.get("level") !== "error" || line.level === "error"),
        next: "s=1;i=1",
        dataSource: "simulated",
      }),
    });
    const user = userEvent.setup();
    render(<LogsSection session={admin} />);
    await user.click(screen.getByRole("tab", { name: "系统日志" }));
    const log = await screen.findByRole("log", { name: "系统日志" });
    const lines = within(log).getAllByRole("listitem");
    expect(lines.map((line) => within(line).getByText(/^(错误|警告|信息|调试)$/).textContent)).toEqual([
      "错误",
      "警告",
      "信息",
    ]);
    expect(lines[0].textContent).toContain("相册服务");
    expect(lines[0].textContent).toContain("error=read-only file system");
    expect(screen.getByText("模拟数据")).toBeTruthy();

    await user.click(within(lines[0]).getByRole("button"));
    expect(within(lines[0]).getByText("anas-photos.service")).toBeTruthy();
    expect(within(lines[0]).getByText("s=1;i=3")).toBeTruthy();

    await user.click(screen.getByRole("radio", { name: "仅错误" }));
    await waitFor(() =>
      expect(within(screen.getByRole("log", { name: "系统日志" })).getAllByRole("listitem")).toHaveLength(1),
    );
    expect(sent.systemLogs.at(-1)?.get("level")).toBe("error");
    await user.selectOptions(screen.getByRole("combobox", { name: "服务" }), "相册服务");
    await waitFor(() => expect(sent.systemLogs.at(-1)?.get("service")).toBe("photos"));
  });

  it("follows new lines while live is on", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let newer = false;
    const sent = serve({
      systemLogs: (params) =>
        params.get("after")
          ? { items: newer ? [logLine("s=1;i=4", 40, { message: "media library rescanned" })] : [], dataSource: "live" }
          : { items: systemLines, dataSource: "live" },
    });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<LogsSection session={admin} />);
    await user.click(screen.getByRole("tab", { name: "系统日志" }));
    await screen.findByRole("log", { name: "系统日志" });
    await user.click(screen.getByRole("switch", { name: "实时" }));
    newer = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    await waitFor(() =>
      expect(within(screen.getByRole("log", { name: "系统日志" })).getAllByRole("listitem")).toHaveLength(4),
    );
    expect(sent.systemLogs.at(-1)?.get("after")).toBe("s=1;i=3");
    const [first, ...earlier] = within(screen.getByRole("log", { name: "系统日志" })).getAllByRole("listitem");
    expect(first.textContent).toContain("media library rescanned");
    // A new line flashes as it arrives; the lines already shown do not. (jsdom
    // keeps the animation shorthand as written and fills no longhands.)
    const animation = (line: HTMLElement) => getComputedStyle(line).getPropertyValue("animation");
    expect(animation(first)).toContain("lg-fresh");
    for (const line of earlier) expect(animation(line)).not.toContain("lg-fresh");
    expect(screen.getByText("实时跟随中")).toBeTruthy();
  });

  it("reports a Host Agent that cannot be reached and retries", async () => {
    let failing = true;
    serve({
      systemLogs: () =>
        failing ? new Error("system logs cannot be read right now") : { items: systemLines, dataSource: "live" },
    });
    const user = userEvent.setup();
    render(<LogsSection session={admin} />);
    await user.click(screen.getByRole("tab", { name: "系统日志" }));
    expect(await screen.findByText(/无法读取系统日志/)).toBeTruthy();
    failing = false;
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByRole("log", { name: "系统日志" })).toBeTruthy();
    expect(screen.queryByText("模拟数据")).toBeNull();
  });
});
