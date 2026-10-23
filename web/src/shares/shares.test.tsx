import { useEffect } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { SharesProvider, useShares } from "./SharesProvider";
import { SharesPanel } from "./SharesPanel";

const file = {
  id: "source",
  spaceId: "space",
  name: "计划.txt",
  kind: "file" as const,
  sizeBytes: 2,
  modifiedAt: "2026-10-15T00:00:00Z",
};
const share = {
  id: "s1",
  sender: { id: "alice", username: "Alice" },
  status: "active",
  createdAt: "2026-10-15T00:00:00Z",
  expiresAt: "2099-01-01T00:00:00Z",
  entries: [{ id: "e1", path: "计划.txt", isDir: false, size: 2 }],
  recipients: [{ userId: "bob", readThrough: 0, senderReadThrough: 0, lastMessage: 1, saved: false, savedFiles: 0 }],
  preparedBytes: 2,
};
const json = (data: unknown) => new Response(JSON.stringify(data), { headers: { "Content-Type": "application/json" } });
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

it("shares ordinary files with multiple members, Unicode emoji and a stable request key", async () => {
  const sent: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url, init) => {
      if (String(url).endsWith("/members"))
        return json({
          items: [
            { id: "bob", username: "Bob" },
            { id: "carol", username: "Carol" },
          ],
        });
      if (init?.method === "POST") {
        sent.push(JSON.parse(init.body));
        return json({ ...share, status: "preparing" });
      }
      return json({ items: [] });
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
  await user.click(screen.getByLabelText("Carol"));
  await user.type(screen.getByLabelText("留言"), "你好{Enter}计划");
  await user.click(screen.getByRole("button", { name: "选择表情" }));
  await user.click(screen.getByRole("button", { name: "👍" }));
  await user.click(screen.getByRole("button", { name: "发送分享" }));
  await waitFor(() => expect(sent).toHaveLength(1));
  expect(sent[0]).toMatchObject({
    sourceIds: ["source"],
    recipientIds: ["bob", "carol"],
    message: "你好\n计划👍",
    requestId: expect.any(String),
  });
});

it("opens a received private thread and renders markup as text, marking only displayed messages read", async () => {
  const reads: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url, init) => {
      const path = String(url);
      if (path.endsWith("/read")) {
        reads.push(JSON.parse(init.body));
        return new Response(null, { status: 204 });
      }
      if (path.includes("/messages?"))
        return json({
          items: [
            { id: 1, sender: share.sender, text: "<img src=x onerror=alert(1)>\n你好👍", createdAt: share.createdAt },
          ],
        });
      if (path.endsWith("/s1") || path.endsWith("/view")) return json(share);
      if (path.includes("direction=received")) return json({ items: [share] });
      return json({ items: [] });
    }),
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
  await waitFor(() => expect(reads).toContainEqual({ recipientId: "bob", through: 1 }));
  expect(screen.queryByLabelText("选择接收者讨论")).toBeNull();
});

it("reads every page of the notification backlog without replaying it and resumes from the per-account cursor", async () => {
  const offsets: number[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url) => {
      const address = new URL(String(url), "http://nas");
      if (!address.pathname.endsWith("/notifications")) return json({ items: [] });
      const after = Number(address.searchParams.get("after"));
      offsets.push(after);
      return json({
        items:
          after === 0
            ? Array.from({ length: 100 }, (_, index) => ({
                id: index + 1,
                shareId: `s${index}`,
                recipientId: "bob",
                kind: "share",
                createdAt: share.createdAt,
              }))
            : after === 100
              ? [{ id: 101, shareId: "last", recipientId: "bob", kind: "message", createdAt: share.createdAt }]
              : [],
      });
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
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url) => json({ items: String(url).includes("direction=received") ? [fresh] : [] })),
  );
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
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url) => {
      const address = new URL(String(url), "http://nas");
      if (!address.pathname.endsWith("/notifications")) return json({ items: [] });
      polls++;
      const after = Number(address.searchParams.get("after"));
      return json({
        items:
          polls === 1
            ? []
            : [
                {
                  id: after + 1,
                  shareId: "s1",
                  recipientId: "bob",
                  kind: "message",
                  messageId: after + 1,
                  createdAt: share.createdAt,
                },
              ],
      });
    }),
  );
  render(
    <SharesProvider userId="bob" active={false} onOpen={() => undefined}>
      <div />
    </SharesProvider>,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000);
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000);
  });
  expect(screen.getAllByLabelText("关闭分享通知")).toHaveLength(1);
  expect(screen.getByText("2 条")).toBeTruthy();
});

it("retries an uncertain send with the same key and preserves the draft", async () => {
  const requests: { requestId: string }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url, init) => {
      if (String(url).endsWith("/members")) return json({ items: [{ id: "bob", username: "Bob" }] });
      if (init?.method === "POST") {
        requests.push(JSON.parse(init.body));
        if (requests.length === 1) throw new Error("连接中断");
        return json(share);
      }
      return json({ items: [] });
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
  let job: unknown;
  const saves: unknown[] = [];
  const tree = {
    ...share,
    entries: [
      { id: "dir", path: "资料", isDir: true, size: 0 },
      { id: "child", path: "资料/计划.txt", isDir: false, size: 2 },
      { id: "other", path: "其他.txt", isDir: false, size: 5 },
    ],
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url, init) => {
      const path = String(url);
      if (path.endsWith("/save")) {
        saves.push(JSON.parse(init.body));
        job = {
          id: "job1",
          shareId: "s1",
          status: "running",
          bytesDone: 1,
          bytesTotal: 2,
          completedIds: [],
          entryIds: ["child"],
        };
        return json(job);
      }
      if (path.endsWith("/jobs")) return json({ items: job ? [job] : [] });
      if (path === "/api/v1/spaces")
        return json({
          items: [
            { id: "readonly", name: "只读资料", kind: "shared", writable: false },
            { id: "mine", name: "我的空间", kind: "private", writable: true },
          ],
        });
      if (path.includes("/spaces/mine/entries"))
        return json({
          items: path.endsWith("parentId=") ? [{ id: "destination", name: "收件箱", kind: "directory" }] : [],
        });
      if (path.endsWith("/s1") || path.endsWith("/view")) return json(tree);
      if (path.includes("direction=received")) return json({ items: [tree] });
      return json({ items: [] });
    }),
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
  await waitFor(() => expect(saves).toHaveLength(1));
  expect(saves[0]).toMatchObject({ entryIds: ["child"], target: { rootId: "mine", parentId: "destination" } });
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
  const readRequests: { recipientId: string }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url, init) => {
      const path = String(url);
      if (path.endsWith("/read")) {
        readRequests.push(JSON.parse(init.body));
        return new Response(null, { status: 204 });
      }
      if (path.includes("/messages?"))
        return json({
          items: [
            {
              id: 1,
              sender: { id: "recipient", username: "成员" },
              text: path.includes("recipientId=carol") ? "Carol 的私密回复" : "Bob 的私密回复",
              createdAt: share.createdAt,
            },
          ],
        });
      if (path.endsWith("/s1")) return json(sent);
      if (path.includes("direction=sent")) return json({ items: [sent] });
      return json({ items: [] });
    }),
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
  await waitFor(() => expect(readRequests.map((item) => item.recipientId)).toContain("carol"));
});

it("suppresses only the active visible thread and not a minimized thread", async () => {
  vi.useFakeTimers();
  let sequence = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url) =>
      String(url).includes("/notifications")
        ? json({
            items: [{ id: ++sequence, shareId: "s1", recipientId: "bob", kind: "message", createdAt: share.createdAt }],
          })
        : json({ items: [] }),
    ),
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
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000);
  });
  expect(screen.queryByLabelText("关闭分享通知")).toBeNull();
  rerender(
    <SharesProvider userId="bob" active={false} onOpen={() => undefined}>
      <Thread />
    </SharesProvider>,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000);
  });
  expect(screen.getByText("分享有新回复")).toBeTruthy();
});

it("does not mark replies hidden behind a content preview as read", async () => {
  vi.useFakeTimers();
  let count = 1;
  const reads: number[] = [];
  const value = { ...share, entries: [{ id: "photo", path: "photo.jpg", isDir: false, size: 2 }] };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url, init) => {
      const path = String(url);
      if (path.endsWith("/read")) {
        reads.push(JSON.parse(init.body).through);
        return new Response(null, { status: 204 });
      }
      if (path.includes("/messages?"))
        return json({
          items: Array.from({ length: count }, (_, index) => ({
            id: index + 1,
            sender: share.sender,
            text: `消息 ${index + 1}`,
            createdAt: share.createdAt,
          })),
        });
      if (path.endsWith("/s1") || path.endsWith("/view")) return json(value);
      if (path.includes("direction=received")) return json({ items: [value] });
      return json({ items: [] });
    }),
  );
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  fireEvent.click(screen.getByRole("button", { name: /photo.jpg/ }));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(reads).toContain(1);
  fireEvent.click(within(files()).getByRole("button", { name: "photo.jpg" }));
  expect(screen.getByRole("dialog", { name: "预览 photo.jpg" })).toBeTruthy();
  count = 2;
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000);
  });
  expect(reads).not.toContain(2);
  fireEvent.click(screen.getByLabelText("关闭预览"));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(reads).toContain(2);
});

it("keeps older loaded shares visible when the background list refreshes", async () => {
  vi.useFakeTimers();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url) => {
      const path = String(url);
      if (path.includes("direction=received")) {
        const offset = Number(new URL(path, "http://nas").searchParams.get("offset"));
        return json({
          items: [
            {
              ...share,
              id: `page-${offset}`,
              entries: [{ ...share.entries[0], path: offset === 0 ? "新的分享.txt" : "较早分享.txt" }],
            },
          ],
          ...(offset === 0 ? { nextOffset: 100 } : {}),
        });
      }
      return json({ items: [] });
    }),
  );
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  fireEvent.click(screen.getByRole("button", { name: "加载更多" }));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByText("较早分享.txt")).toBeTruthy();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000);
  });
  expect(screen.getByText("较早分享.txt")).toBeTruthy();
});

it("retries a failed read cursor update after the connection recovers", async () => {
  vi.useFakeTimers();
  let reads = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url) => {
      const path = String(url);
      if (path.endsWith("/read")) {
        if (++reads === 1) throw new Error("连接中断");
        return new Response(null, { status: 204 });
      }
      if (path.includes("/messages?"))
        return json({ items: [{ id: 1, sender: share.sender, text: "一条消息", createdAt: share.createdAt }] });
      if (path.endsWith("/s1") || path.endsWith("/view")) return json(share);
      if (path.includes("direction=received")) return json({ items: [share] });
      return json({ items: [] });
    }),
  );
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  fireEvent.click(screen.getByRole("button", { name: /计划.txt/ }));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(reads).toBe(1);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000);
  });
  expect(reads).toBe(2);
});

it("marks a newly received share unread even when it has no initial message", async () => {
  const value = { ...share, recipients: [{ ...share.recipients[0], lastMessage: 0 }] };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url) => json({ items: String(url).includes("direction=received") ? [value] : [] })),
  );
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
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url) => {
      const path = String(url);
      if (path.endsWith("/jobs")) throw new Error("保存任务暂不可用");
      if (path.endsWith("/view")) {
        viewed = true;
        return json(share);
      }
      if (path.endsWith("/s1")) return json(share);
      if (path.includes("direction=received")) return json({ items: [share] });
      return json({ items: [] });
    }),
  );
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await userEvent.click(await screen.findByRole("button", { name: /计划.txt/ }));
  expect(await screen.findByText("保存任务暂不可用")).toBeTruthy();
  await waitFor(() => expect(viewed).toBe(true));
});

it("finishes previewing an empty text file instead of showing a permanent loading state", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url) => {
      const path = String(url);
      if (path.endsWith("/content")) return new Response("");
      if (path.endsWith("/s1") || path.endsWith("/view")) return json(share);
      if (path.includes("direction=received")) return json({ items: [share] });
      return json({ items: [] });
    }),
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
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url) => {
      const path = String(url);
      if (path.endsWith("/s1")) return json(value);
      if (path.includes("direction=sent")) return json({ items: [value] });
      return json({ items: [] });
    }),
  );
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
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url, init) => {
      const path = String(url);
      if (path.endsWith("/messages") && init?.method === "POST") {
        const body = JSON.parse(init.body);
        replies.push(body);
        return json({ id: 2, sender: { id: "bob", username: "Bob" }, text: body.text, createdAt: share.createdAt });
      }
      if (path.includes("/messages?")) return json({ items: [] });
      if (path.endsWith("/s1") || path.endsWith("/view")) return json(share);
      if (path.includes("direction=received")) return json({ items: [share] });
      return json({ items: [] });
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
  const posted = vi.fn();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url, init) => {
      if (String(url).endsWith("/members")) return json({ items: [{ id: "bob", username: "Bob" }] });
      if (init?.method === "POST") posted();
      return json({ items: [] });
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
  await user.click(screen.getByRole("button", { name: "发送分享" }));
  expect((await screen.findByRole("alert")).textContent).toContain("浏览器无法生成请求标识");
  expect(posted).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "发送分享" }).hasAttribute("disabled")).toBe(false);
});

it("keeps selected recipients when searching by nickname or account", async () => {
  const sent: { recipientIds: string[] }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url, init) => {
      if (String(url).endsWith("/members"))
        return json({
          items: [
            { id: "bob", username: "bob", nickname: "小明" },
            { id: "carol", username: "carol", nickname: "小红" },
          ],
        });
      if (init?.method === "POST") {
        sent.push(JSON.parse(init.body));
        return json(share);
      }
      return json({ items: [] });
    }),
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
  await waitFor(() => expect(sent).toHaveLength(1));
  expect(sent[0].recipientIds).toEqual(["bob", "carol"]);
});

it("keeps a reply draft while chat is collapsed and marks new messages read only after reopening", async () => {
  vi.useFakeTimers();
  let sequence = 1;
  const reads: number[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url, init) => {
      const path = String(url);
      if (path.endsWith("/read")) {
        reads.push(JSON.parse(init.body).through);
        return new Response(null, { status: 204 });
      }
      if (path.includes("/messages?"))
        return json({
          items: [{ id: sequence, sender: share.sender, text: `消息${sequence}`, createdAt: share.createdAt }],
        });
      if (path.endsWith("/s1") || path.endsWith("/view")) return json(share);
      if (path.includes("direction=received")) return json({ items: [share] });
      return json({ items: [] });
    }),
  );
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  fireEvent.click(screen.getByRole("button", { name: /计划.txt/ }));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  fireEvent.change(screen.getByLabelText("回复"), { target: { value: "还没发出的草稿" } });
  fireEvent.click(screen.getByRole("button", { name: "收起私聊" }));
  sequence = 2;
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000);
  });
  expect(reads).not.toContain(2);
  expect(screen.queryByRole("textbox", { name: "回复" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "私聊" }));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect((screen.getByLabelText("回复") as HTMLTextAreaElement).value).toBe("还没发出的草稿");
  expect(reads).toContain(2);
});

it("saves all top-level entries without requiring manual selection", async () => {
  const requests: { entryIds: string[] }[] = [];
  const tree = {
    ...share,
    entries: [
      { id: "dir", path: "资料", isDir: true, size: 0 },
      { id: "child", path: "资料/计划.txt", isDir: false, size: 2 },
      { id: "other", path: "其他.txt", isDir: false, size: 5 },
    ],
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url, init) => {
      const path = String(url);
      if (path.endsWith("/save")) {
        requests.push(JSON.parse(init.body));
        return json({ id: "job", status: "running", bytesDone: 0, bytesTotal: 7 });
      }
      if (path === "/api/v1/spaces")
        return json({ items: [{ id: "mine", name: "我的空间", kind: "private", writable: true }] });
      if (path.endsWith("/s1") || path.endsWith("/view")) return json(tree);
      if (path.includes("direction=received")) return json({ items: [tree] });
      return json({ items: [] });
    }),
  );
  const user = userEvent.setup();
  render(
    <SharesProvider userId="bob" active onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>,
  );
  await user.click(await screen.findByRole("button", { name: /资料 等 2 项/ }));
  await user.click(await screen.findByRole("button", { name: "保存全部" }));
  await user.click(await screen.findByRole("button", { name: "保存到此文件夹" }));
  await waitFor(() => expect(requests).toHaveLength(1));
  expect(requests[0].entryIds).toEqual(["dir", "other"]);
});
