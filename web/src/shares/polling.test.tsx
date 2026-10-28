import { act, fireEvent, render, renderHook, screen } from "@testing-library/react";
import { afterEach, expect, it, onTestFinished, vi } from "vitest";

import { readCursors, sharesHandlers } from "../test/handlers/shares";
import { apiError, http, server } from "../test/server";
import type { Share } from "./api";
import { useThread } from "./Conversation";
import { SharesPanel } from "./SharesPanel";
import { SharesProvider, useShares } from "./SharesProvider";

afterEach(() => {
  vi.useRealTimers();
  localStorage.clear();
});

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

const message = (id: number, recipient = "bob") => ({
  id,
  sender: { id: recipient, username: recipient },
  text: `${recipient}-${id}`,
  createdAt: "2026-10-20T00:00:00Z",
});

it("resumes a conversation from its last message across polls, refreshes and visibility changes", async () => {
  vi.useFakeTimers();
  const requests: { recipient: string; after: number }[] = [];
  let newest = 2;
  server.use(
    http.get("/api/v1/shares/{shareId}/messages", ({ request, response }) => {
      const url = new URL(request.url);
      const after = Number(url.searchParams.get("after"));
      const recipient = url.searchParams.get("recipientId") ?? "bob";
      requests.push({ recipient, after });
      return response(200).json({
        items: Array.from({ length: newest }, (_, index) => message(index + 1, recipient)).filter(
          (item) => item.id > after,
        ),
      });
    }),
  );
  const { result, rerender } = renderHook(({ recipient, visible }) => useThread("s1", recipient, visible), {
    initialProps: { recipient: "bob", visible: true },
  });
  await tick();
  expect(result.current.messages.map((item) => item.id)).toEqual([1, 2]);
  newest = 3;
  await tick(3000);
  expect(requests.at(-1)).toEqual({ recipient: "bob", after: 2 });
  expect(result.current.messages.map((item) => item.id)).toEqual([1, 2, 3]);
  act(() => result.current.reload());
  await tick();
  expect(requests.at(-1)).toEqual({ recipient: "bob", after: 3 });
  rerender({ recipient: "bob", visible: false });
  const count = requests.length;
  await tick(9000);
  expect(requests).toHaveLength(count);
  rerender({ recipient: "bob", visible: true });
  await tick();
  expect(requests.at(-1)).toEqual({ recipient: "bob", after: 3 });
  rerender({ recipient: "carol", visible: true });
  expect(result.current.messages).toEqual([]);
  await tick();
  expect(requests.at(-1)).toEqual({ recipient: "carol", after: 0 });
  expect(result.current.messages.every((item) => item.sender.id === "carol")).toBe(true);
});

it("stops mounted share-window requests when minimized and refreshes on restore", async () => {
  vi.useFakeTimers();
  const share: Share = {
    id: "s1",
    sender: { id: "alice", username: "Alice" },
    status: "published",
    createdAt: "2026-10-20T00:00:00Z",
    expiresAt: "2099-01-01T00:00:00Z",
    preparedBytes: 2,
    entries: [{ id: "e1", path: "计划.txt", isDir: false, size: 2 }],
    recipients: [{ userId: "bob", readThrough: 0, senderReadThrough: 0, lastMessage: 1, saved: false, savedFiles: 0 }],
  };
  const requests: string[] = [];
  server.use(...sharesHandlers({ received: [share], messages: () => [message(1)] }), readCursors().handler);
  const track = ({ request }: { request: Request }) => requests.push(request.url);
  server.events.on("request:start", track);
  onTestFinished(() => server.events.removeListener("request:start", track));
  const panel = (visible: boolean) => (
    <SharesProvider userId="bob" active={false} windowVisible={visible} onOpen={() => undefined}>
      <SharesPanel />
    </SharesProvider>
  );
  const { rerender } = render(panel(true));
  await tick();
  fireEvent.click(screen.getByRole("button", { name: /计划.txt/ }));
  await tick();
  expect(requests.some((url) => url.includes("/messages?"))).toBe(true);
  rerender(panel(false));
  await tick();
  requests.length = 0;
  await tick(60_000);
  // Desktop notifications remain available even while the window is closed.
  expect(requests.filter((url) => !url.includes("/notifications") && !url.includes("direction="))).toEqual([]);
  expect(requests.filter((url) => url.includes("direction="))).toHaveLength(4);
  rerender(panel(true));
  await tick();
  expect(requests.some((url) => url.endsWith("/s1"))).toBe(true);
  expect(requests.some((url) => url.includes("/messages?") && url.includes("after=1"))).toBe(true);
});

it("retries a failed page without dropping history or skipping messages", async () => {
  vi.useFakeTimers();
  const cursors: number[] = [];
  let fail = true;
  server.use(
    http.get("/api/v1/shares/{shareId}/messages", ({ query, response }) => {
      const after = Number(query.get("after"));
      cursors.push(after);
      if (after === 100 && fail) return response("default").json(apiError("unavailable"), { status: 503 });
      return response(200).json({
        items: Array.from({ length: 102 }, (_, index) => message(index + 1))
          .filter((item) => item.id > after)
          .slice(0, 100),
      });
    }),
  );
  const { result } = renderHook(() => useThread("s1", "bob"));
  await tick();
  expect(cursors).toEqual([0, 100]);
  expect(result.current.loaded).toBe(false);
  fail = false;
  await tick(3000);
  expect(result.current.messages.map((item) => item.id)).toEqual(Array.from({ length: 102 }, (_, index) => index + 1));
  await tick(3000);
  expect(cursors.at(-1)).toBe(102);
  expect(result.current.messages).toHaveLength(102);
});

it("refreshes desktop unread counts with the share window closed and no new notifications", async () => {
  vi.useFakeTimers();
  let unread = true;
  let reads = 0;
  server.use(...sharesHandlers());
  server.use(
    http.get("/api/v1/shares", ({ query, response }) => {
      reads++;
      return response(200).json({
        items:
          unread && query.get("direction") === "received"
            ? [
                {
                  id: "s1",
                  status: "published",
                  createdAt: "2026-10-20T00:00:00Z",
                  expiresAt: "2099-01-01T00:00:00Z",
                  preparedBytes: 0,
                  sender: { id: "alice", username: "Alice" },
                  recipients: [
                    {
                      userId: "bob",
                      readThrough: 0,
                      senderReadThrough: 0,
                      lastMessage: 1,
                      saved: false,
                      savedFiles: 0,
                    },
                  ],
                  entries: [],
                },
              ]
            : [],
      });
    }),
  );
  function Badge() {
    return <output>{useShares().unread.received}</output>;
  }
  render(
    <SharesProvider userId="bob" active={false} windowVisible={false} onOpen={() => undefined}>
      <Badge />
    </SharesProvider>,
  );
  await tick();
  expect(screen.getByRole("status").textContent).toBe("1");
  unread = false;
  await tick(30_000);
  expect(screen.getByRole("status").textContent).toBe("0");
  const before = reads;
  Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
  try {
    await tick(30_000);
    expect(reads).toBe(before);
  } finally {
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
  }
});
