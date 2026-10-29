import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse } from "msw";
import { useEffect } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import type { CreateMemberShare, MemberShareJob, Space } from "../api.generated";
import { filesHandlers } from "../test/handlers/files";
import {
  memberShare,
  message,
  notice,
  readCursors,
  shareCreations,
  sharedAt,
  shareSaves,
  sharesHandlers,
} from "../test/handlers/shares";
import { apiError, http, server } from "../test/server";
import { SharesPanel } from "./SharesPanel";
import { SharesProvider, useShares } from "./SharesProvider";

const file = {
  id: "source",
  spaceId: "space",
  name: "计划.txt",
  kind: "file" as const,
  sizeBytes: 2,
  modifiedAt: "2026-10-15T00:00:00Z",
};
// alice shares 计划.txt with bob.
const share = memberShare();
const bob = { id: "bob", username: "Bob" };
const carol = { id: "carol", username: "Carol" };
const mine: Space = { id: "mine", name: "我的空间", kind: "private", writable: true, createdAt: sharedAt };
// The NAS LAN entry is HTTP: getRandomValues exists, randomUUID does not.
const getRandomValues = crypto.getRandomValues.bind(crypto);
beforeEach(() => vi.stubGlobal("crypto", { getRandomValues }));
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  localStorage.clear();
});
function Launcher() {
  const shares = useShares();
  return <button onClick={() => shares.compose([file])}>新分享</button>;
}
// The list stays beside an open share, so files are looked up in their own region.
const files = () => screen.getByRole("region", { name: "分享内容" });
// tick moves the fake clock by ms, then 200 ms more in small steps, so the
// requests that started get their answers: MSW answers through a socket that
// waits on timers (setImmediate), which are fake here, so a reply takes a few
// steps of the clock. 200 ms is far below the 3 s and 5 s polls.
async function tick(ms = 0) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
  for (let step = 0; step < 20; step++) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10);
    });
  }
}

it("shares ordinary files with multiple members, Unicode emoji and a stable request key", async () => {
  const created = shareCreations();
  server.use(...sharesHandlers({ members: [bob, carol] }), created.handler);
  const user = userEvent.setup();
  render(
    <SharesProvider userId="alice" active={false} onOpen={() => undefined}>
      <Launcher />
    </SharesProvider>,
  );
  await user.click(screen.getByText("新分享"));
  await user.click(await screen.findByLabelText("Bob"));
  await user.click(screen.getByLabelText("Carol"));
  await user.type(screen.getByLabelText("留言"), "你好{Enter}计划");
  await user.click(screen.getByRole("button", { name: "选择表情" }));
  await user.click(screen.getByRole("button", { name: "👍" }));
  await user.click(screen.getByRole("button", { name: "发送分享" }));
  await waitFor(() => expect(created.sent).toHaveLength(1));
  expect(created.sent[0]).toMatchObject({
    sourceIds: ["source"],
    recipientIds: ["bob", "carol"],
    message: "你好\n计划👍",
    requestId: expect.any(String),
  });
});

it("lists chosen members as tags that survive a search and can be removed", async () => {
  const created = shareCreations();
  server.use(...sharesHandlers({ members: [bob, carol] }), created.handler);
  const user = userEvent.setup();
  render(
    <SharesProvider userId="alice" active={false} onOpen={() => undefined}>
      <Launcher />
    </SharesProvider>,
  );
  await user.click(screen.getByText("新分享"));
  await user.click(await screen.findByLabelText("Bob"));
  await user.click(screen.getByLabelText("Carol"));
  await user.type(screen.getByRole("searchbox", { name: "搜索成员" }), "Bob");
  const tags = screen.getByRole("list", { name: "已选成员" });
  expect(
    within(tags)
      .getAllByRole("listitem")
      .map((item) => item.textContent),
  ).toEqual(["BBob", "CCarol"]);
  await user.click(within(tags).getByRole("button", { name: "取消选择 Carol" }));
  expect(document.activeElement).toBe(within(tags).getByRole("button", { name: "取消选择 Bob" }));
  expect(screen.getByText("已选 1 人")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "发送分享" }));
  await waitFor(() => expect(created.sent).toHaveLength(1));
  expect(created.sent[0]).toMatchObject({ recipientIds: ["bob"], message: "" });
});

it("opens a received private thread and renders markup as text, marking only displayed messages read", async () => {
  const reads = readCursors();
  server.use(
    ...sharesHandlers({ received: [share], messages: () => [message(1, "<img src=x onerror=alert(1)>\n你好👍")] }),
    reads.handler,
  );
  const user = userEvent.setup();
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await user.click(await screen.findByRole("button", { name: /计划.txt/ }));
  expect(await screen.findByText(/<img src=x onerror=alert/)).toBeTruthy();
  expect(document.querySelector("img[src=x]")).toBeNull();
  await waitFor(() => expect(reads.sent).toContainEqual({ recipientId: "bob", through: 1 }));
  expect(screen.queryByLabelText("选择接收者讨论")).toBeNull();
});

it("reads every page of the notification backlog without replaying it and resumes from the per-account cursor", async () => {
  const offsets: number[] = [];
  const backlog = [
    ...Array.from({ length: 100 }, (_, index) => notice(index + 1, { shareId: `s${index}`, kind: "share" })),
    notice(101, { shareId: "last" }),
  ];
  server.use(...sharesHandlers({ notices: backlog }));
  server.use(
    // Records where each page starts; answering nothing, it passes the request on.
    http.get("/api/v1/shares/notifications", ({ query }) => {
      offsets.push(Number(query.get("after")));
    }),
  );
  const { unmount } = render(
    <SharesProvider userId="bob" active={false} onOpen={() => undefined}>
      <div />
    </SharesProvider>,
  );
  await waitFor(() => expect(localStorage.getItem("anas:shares:notifications:bob")).toBe("101"));
  expect(offsets.slice(0, 2)).toEqual([0, 100]);
  expect(screen.queryByLabelText("关闭分享通知")).toBeNull();
  unmount();
  render(
    <SharesProvider userId="bob" active={false} onOpen={() => undefined}>
      <div />
    </SharesProvider>,
  );
  await waitFor(() => expect(offsets.at(-1)).toBe(101));
  expect(screen.queryByLabelText("关闭分享通知")).toBeNull();
});

it("summarises what is unread when the desktop opens", async () => {
  const fresh = { ...share, recipients: [{ ...share.recipients[0], lastMessage: 0 }] };
  server.use(...sharesHandlers({ received: [fresh] }));
  render(
    <SharesProvider userId="bob" active={false} onOpen={() => undefined}>
      <div />
    </SharesProvider>,
  );
  expect(await screen.findByText("有未读的分享")).toBeTruthy();
  expect(screen.getByText("1 个收到的分享有新内容")).toBeTruthy();
});

it("groups live replies of one conversation into a single toast", async () => {
  vi.useFakeTimers();
  let polls = 0;
  // The toast keeps its plain wording when the share cannot be read: no list
  // holds s1, so reading it answers 404.
  server.use(...sharesHandlers());
  server.use(
    http.get("/api/v1/shares/notifications", ({ query, response }) => {
      polls++;
      const after = Number(query.get("after"));
      return response(200).json({ items: polls === 1 ? [] : [notice(after + 1, { messageId: after + 1 })] });
    }),
  );
  render(
    <SharesProvider userId="bob" active={false} onOpen={() => undefined}>
      <div />
    </SharesProvider>,
  );
  await tick();
  await tick(5000);
  await tick(5000);
  expect(screen.getAllByLabelText("关闭分享通知")).toHaveLength(1);
  expect(screen.getByText("2 条")).toBeTruthy();
});

it("retries an uncertain send with the same key and preserves the draft", async () => {
  const requests: CreateMemberShare[] = [];
  server.use(
    ...sharesHandlers({ members: [bob] }),
    http.post("/api/v1/shares", async ({ request, response }) => {
      requests.push(await request.json());
      // The first send loses its connection before any answer.
      if (requests.length === 1) return HttpResponse.error();
      return response(202).json(share);
    }),
  );
  const user = userEvent.setup();
  render(
    <SharesProvider userId="alice" active={false} onOpen={() => undefined}>
      <Launcher />
    </SharesProvider>,
  );
  await user.click(screen.getByText("新分享"));
  await user.click(await screen.findByLabelText("Bob"));
  await user.type(screen.getByLabelText("留言"), "保留留言");
  await user.click(screen.getByRole("button", { name: "发送分享" }));
  expect((await screen.findByRole("alert")).textContent).toContain("连接中断");
  expect((screen.getByLabelText("留言") as HTMLTextAreaElement).value).toBe("保留留言");
  await user.click(screen.getByRole("button", { name: "发送分享" }));
  await waitFor(() => expect(requests).toHaveLength(2));
  expect(requests[0].requestId).toBe(requests[1].requestId);
});

it("saves a subset in a writable folder and reloads persisted jobs after reopening", async () => {
  const jobs: MemberShareJob[] = [];
  const saves = shareSaves(jobs);
  const tree = {
    ...share,
    entries: [
      { id: "dir", path: "资料", isDir: true, size: 0 },
      { id: "child", path: "资料/计划.txt", isDir: false, size: 2 },
      { id: "other", path: "其他.txt", isDir: false, size: 5 },
    ],
  };
  server.use(
    ...sharesHandlers({ received: [tree], jobs }),
    ...filesHandlers({
      spaces: [{ id: "readonly", name: "只读资料", kind: "shared", writable: false, createdAt: sharedAt }, mine],
      entries: [
        {
          id: "destination",
          spaceId: mine.id,
          name: "收件箱",
          kind: "directory",
          sizeBytes: 0,
          modifiedAt: sharedAt,
        },
      ],
    }),
    saves.handler,
  );
  const user = userEvent.setup();
  const first = render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await user.click(await screen.findByRole("button", { name: /资料 等 2 项/ }));
  await user.click(await within(files()).findByRole("button", { name: "资料" }));
  await user.click(screen.getByLabelText("选择 计划.txt"));
  await user.click(screen.getByRole("button", { name: "保存所选 1 项" }));
  const picker = await screen.findByRole("dialog", { name: "保存到我的空间" });
  expect(within(picker).queryByText("只读资料")).toBeNull();
  await user.click(await within(picker).findByRole("button", { name: "收件箱" }));
  await waitFor(() =>
    expect(within(picker).getByRole("button", { name: "保存到此文件夹" }).hasAttribute("disabled")).toBe(false),
  );
  await user.click(within(picker).getByRole("button", { name: "保存到此文件夹" }));
  await waitFor(() => expect(saves.sent).toHaveLength(1));
  expect(saves.sent[0]).toMatchObject({ entryIds: ["child"], target: { rootId: "mine", parentId: "destination" } });
  expect(await screen.findByText("正在保存")).toBeTruthy();
  first.unmount();
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await user.click(await screen.findByRole("button", { name: /资料 等 2 项/ }));
  expect(await screen.findByText("正在保存")).toBeTruthy();
});

it("switches sender private threads without showing the previous recipient messages", async () => {
  const sent = { ...share, recipients: [...share.recipients, { ...share.recipients[0], userId: "carol" }] };
  const reads = readCursors();
  server.use(
    ...sharesHandlers({
      sent: [sent],
      messages: (recipientId) => [
        message(1, recipientId === "carol" ? "Carol 的私密回复" : "Bob 的私密回复", {
          id: "recipient",
          username: "成员",
        }),
      ],
    }),
    reads.handler,
  );
  const user = userEvent.setup();
  render(
    <SharesProvider userId="alice" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await user.click(screen.getByRole("button", { name: "我发出的分享" }));
  await user.click(await screen.findByRole("button", { name: /计划.txt/ }));
  expect(await screen.findByText("Bob 的私密回复")).toBeTruthy();
  await user.click(within(screen.getByRole("complementary", { name: "私聊" })).getByRole("button", { name: "carol" }));
  expect(await screen.findByText("Carol 的私密回复")).toBeTruthy();
  expect(screen.queryByText("Bob 的私密回复")).toBeNull();
  await waitFor(() => expect(reads.sent.map((item) => item.recipientId)).toContain("carol"));
});

it("suppresses only the active visible thread and not a minimized thread", async () => {
  vi.useFakeTimers();
  let sequence = 0;
  // No list holds s1, so the toast cannot read it and keeps its plain wording.
  server.use(...sharesHandlers());
  server.use(
    http.get("/api/v1/shares/notifications", ({ response }) => response(200).json({ items: [notice(++sequence)] })),
  );
  function Thread() {
    const { thread } = useShares();
    useEffect(() => {
      thread({ id: "s1", recipientId: "bob" });
    }, [thread]);
    return null;
  }
  const { rerender } = render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <Thread />
    </SharesProvider>,
  );
  await tick();
  await tick(5000);
  expect(screen.queryByLabelText("关闭分享通知")).toBeNull();
  rerender(
    <SharesProvider userId="bob" active={false} onOpen={() => undefined}>
      <Thread />
    </SharesProvider>,
  );
  await tick(5000);
  expect(screen.getByText("分享有新回复")).toBeTruthy();
});

it("does not mark replies hidden behind a content preview as read", async () => {
  vi.useFakeTimers();
  let count = 1;
  const reads = readCursors();
  const through = () => reads.sent.map((read) => read.through);
  const value = { ...share, entries: [{ id: "photo", path: "photo.jpg", isDir: false, size: 2 }] };
  server.use(
    ...sharesHandlers({
      received: [value],
      messages: () => Array.from({ length: count }, (_, index) => message(index + 1, `消息 ${index + 1}`)),
    }),
    reads.handler,
  );
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await tick();
  fireEvent.click(screen.getByRole("button", { name: /photo.jpg/ }));
  await tick();
  expect(through()).toContain(1);
  fireEvent.click(within(files()).getByRole("button", { name: "photo.jpg" }));
  expect(screen.getByRole("dialog", { name: "预览 photo.jpg" })).toBeTruthy();
  count = 2;
  await tick(3000);
  expect(through()).not.toContain(2);
  fireEvent.click(screen.getByLabelText("关闭预览"));
  await tick();
  expect(through()).toContain(2);
});

it("keeps older loaded shares visible when the background list refreshes", async () => {
  vi.useFakeTimers();
  server.use(...sharesHandlers());
  server.use(
    // Received shares come in pages of one: a newer one, then an older one.
    http.get("/api/v1/shares", ({ query, response }) => {
      if (query.get("direction") !== "received") return response(200).json({ items: [] });
      const offset = Number(query.get("offset"));
      return response(200).json({
        items: [
          {
            ...share,
            id: `page-${offset}`,
            entries: [{ ...share.entries[0], path: offset === 0 ? "新的分享.txt" : "较早分享.txt" }],
          },
        ],
        ...(offset === 0 ? { nextOffset: 100 } : {}),
      });
    }),
  );
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await tick();
  fireEvent.click(screen.getByRole("button", { name: "加载更多" }));
  await tick();
  expect(screen.getByText("较早分享.txt")).toBeTruthy();
  await tick(5000);
  expect(screen.getByText("较早分享.txt")).toBeTruthy();
});

it("retries a failed read cursor update after the connection recovers", async () => {
  vi.useFakeTimers();
  let reads = 0;
  server.use(
    ...sharesHandlers({ received: [share], messages: () => [message(1, "一条消息")] }),
    http.post("/api/v1/shares/{shareId}/read", ({ response }) => {
      // The first update loses its connection.
      if (++reads === 1) return HttpResponse.error();
      return response(204).empty();
    }),
  );
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await tick();
  fireEvent.click(screen.getByRole("button", { name: /计划.txt/ }));
  await tick();
  expect(reads).toBe(1);
  await tick(3000);
  expect(reads).toBe(2);
});

it("marks a newly received share unread even when it has no initial message", async () => {
  const value = { ...share, recipients: [{ ...share.recipients[0], lastMessage: 0 }] };
  server.use(...sharesHandlers({ received: [value] }));
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  const record = await screen.findByRole("button", { name: /计划.txt/ });
  expect(within(record).getByText("未读")).toBeTruthy();
});

it("records a visible share view even if loading saved jobs fails", async () => {
  let viewed = false;
  server.use(...sharesHandlers({ received: [share] }));
  server.use(
    http.get("/api/v1/shares/{shareId}/jobs", ({ response }) =>
      response("default").json(apiError("shares_unavailable", "jobs unavailable"), { status: 503 }),
    ),
    http.post("/api/v1/shares/{shareId}/view", ({ response }) => {
      viewed = true;
      return response(200).json(share);
    }),
  );
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await userEvent.click(await screen.findByRole("button", { name: /计划.txt/ }));
  expect(await screen.findByText("分享服务暂不可用，请稍后重试")).toBeTruthy();
  await waitFor(() => expect(viewed).toBe(true));
});

it("finishes previewing an empty text file instead of showing a permanent loading state", async () => {
  server.use(
    ...sharesHandlers({ received: [share] }),
    // The contract sends content as application/octet-stream, which the typed
    // helpers only answer as JSON or text media types.
    http.get("/api/v1/shares/{shareId}/entries/{entryId}/content", ({ response }) =>
      response.untyped(new Response("")),
    ),
  );
  const user = userEvent.setup();
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await user.click(await screen.findByRole("button", { name: /计划.txt/ }));
  await user.click(await within(files()).findByRole("button", { name: "计划.txt" }));
  const preview = screen.getByRole("dialog", { name: "预览 计划.txt" });
  await waitFor(() => expect(within(preview).queryByText("正在读取…")).toBeNull());
  expect(preview.querySelector("pre")?.textContent).toBe("");
});

it("shows uncertain save results separately from confirmed saved file counts", async () => {
  const value = { ...share, recipients: [{ ...share.recipients[0], saveUncertain: true, saved: true, savedFiles: 1 }] };
  server.use(...sharesHandlers({ sent: [value] }));
  const user = userEvent.setup();
  render(
    <SharesProvider userId="alice" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await user.click(screen.getByRole("button", { name: "我发出的分享" }));
  await user.click(await screen.findByRole("button", { name: /计划.txt/ }));
  // The count says what was confirmed; the uncertain rest is named apart from it.
  expect((await screen.findAllByText("已保存 1 个文件，另有结果待确认")).length).toBeGreaterThan(0);
});

it("sends a private reply over LAN HTTP without randomUUID", async () => {
  const replies: { recipientId: string; text: string; requestId: string }[] = [];
  server.use(
    ...sharesHandlers({ received: [share] }),
    http.post("/api/v1/shares/{shareId}/messages", async ({ request, response }) => {
      const body = await request.json();
      replies.push(body);
      return response(201).json(message(2, body.text, bob));
    }),
  );
  const user = userEvent.setup();
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await user.click(await screen.findByRole("button", { name: /计划.txt/ }));
  await user.type(await screen.findByLabelText("回复"), "收到 👍");
  await user.click(screen.getByRole("button", { name: "发送回复" }));
  await waitFor(() => expect(replies).toHaveLength(1));
  expect(replies[0]).toMatchObject({ recipientId: "bob", text: "收到 👍" });
  expect(replies[0].requestId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  expect((screen.getByLabelText("回复") as HTMLTextAreaElement).value).toBe("");
});

it("shows request-key failures instead of silently ignoring Send", async () => {
  vi.stubGlobal("crypto", {});
  const created = shareCreations();
  server.use(...sharesHandlers({ members: [bob] }), created.handler);
  const user = userEvent.setup();
  render(
    <SharesProvider userId="alice" active={false} onOpen={() => undefined}>
      <Launcher />
    </SharesProvider>,
  );
  await user.click(screen.getByText("新分享"));
  await user.click(await screen.findByLabelText("Bob"));
  await user.click(screen.getByRole("button", { name: "发送分享" }));
  expect((await screen.findByRole("alert")).textContent).toContain("浏览器无法生成请求标识");
  expect(created.sent).toEqual([]);
  expect(screen.getByRole("button", { name: "发送分享" }).hasAttribute("disabled")).toBe(false);
});

it("keeps selected recipients when searching by nickname or account", async () => {
  const created = shareCreations();
  server.use(
    ...sharesHandlers({
      members: [
        { id: "bob", username: "bob", nickname: "小明" },
        { id: "carol", username: "carol", nickname: "小红" },
      ],
    }),
    created.handler,
  );
  const user = userEvent.setup();
  render(
    <SharesProvider userId="alice" active={false} onOpen={() => undefined}>
      <Launcher />
    </SharesProvider>,
  );
  await user.click(screen.getByText("新分享"));
  await user.click(await screen.findByLabelText("小明"));
  await user.type(screen.getByRole("searchbox", { name: "搜索成员" }), "carol");
  expect(screen.queryByLabelText("小明")).toBeNull();
  await user.click(screen.getByLabelText("小红"));
  await user.click(screen.getByRole("button", { name: "发送分享" }));
  await waitFor(() => expect(created.sent).toHaveLength(1));
  expect(created.sent[0].recipientIds).toEqual(["bob", "carol"]);
});

it("keeps a reply draft while chat is collapsed and marks new messages read only after reopening", async () => {
  vi.useFakeTimers();
  let sequence = 1;
  const reads = readCursors();
  const through = () => reads.sent.map((read) => read.through);
  server.use(
    ...sharesHandlers({ received: [share], messages: () => [message(sequence, `消息${sequence}`)] }),
    reads.handler,
  );
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await tick();
  fireEvent.click(screen.getByRole("button", { name: /计划.txt/ }));
  await tick();
  fireEvent.change(screen.getByLabelText("回复"), { target: { value: "还没发出的草稿" } });
  fireEvent.click(screen.getByRole("button", { name: "收起私聊" }));
  sequence = 2;
  await tick(3000);
  expect(through()).not.toContain(2);
  expect(screen.queryByRole("textbox", { name: "回复" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "私聊" }));
  await tick();
  expect((screen.getByLabelText("回复") as HTMLTextAreaElement).value).toBe("还没发出的草稿");
  expect(through()).toContain(2);
});

it("saves all top-level entries without requiring manual selection", async () => {
  const saves = shareSaves([]);
  const tree = {
    ...share,
    entries: [
      { id: "dir", path: "资料", isDir: true, size: 0 },
      { id: "child", path: "资料/计划.txt", isDir: false, size: 2 },
      { id: "other", path: "其他.txt", isDir: false, size: 5 },
    ],
  };
  server.use(...sharesHandlers({ received: [tree] }), ...filesHandlers({ spaces: [mine] }), saves.handler);
  const user = userEvent.setup();
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await user.click(await screen.findByRole("button", { name: /资料 等 2 项/ }));
  await user.click(await screen.findByRole("button", { name: "保存全部" }));
  await user.click(await screen.findByRole("button", { name: "保存到此文件夹" }));
  await waitFor(() => expect(saves.sent).toHaveLength(1));
  expect(saves.sent[0].entryIds).toEqual(["dir", "other"]);
});
