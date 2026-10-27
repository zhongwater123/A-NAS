import { act, fireEvent, render, renderHook, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { apiError, json, stubAPI } from "../test/api";
import { useThread } from "./Conversation";
import { SharesPanel } from "./SharesPanel";
import { SharesProvider } from "./SharesProvider";
import type { Share } from "./api";

afterEach(() => {
  vi.useRealTimers();
  localStorage.clear();
});

const tick = async (milliseconds = 0) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(milliseconds);
  });
};
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
  stubAPI(
    vi.fn(async (input) => {
      const url = new URL(String(input), "http://nas");
      const after = Number(url.searchParams.get("after"));
      const recipient = url.searchParams.get("recipientId") ?? "bob";
      requests.push({ recipient, after });
      return json({
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
  stubAPI(
    vi.fn(async (input) => {
      const url = String(input);
      requests.push(url);
      if (url.endsWith("/s1") || url.endsWith("/view")) return json(share);
      if (url.includes("/messages?")) return json({ items: [message(1)] });
      if (url.includes("direction=received")) return json({ items: [share] });
      return json({ items: [] });
    }),
  );
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
  expect(requests.filter((url) => !url.includes("/notifications"))).toEqual([]);
  rerender(panel(true));
  await tick();
  expect(requests.some((url) => url.endsWith("/s1"))).toBe(true);
  expect(requests.some((url) => url.includes("/messages?") && url.includes("after=1"))).toBe(true);
});

it("retries a failed page without dropping history or skipping messages", async () => {
  vi.useFakeTimers();
  const cursors: number[] = [];
  let fail = true;
  stubAPI(
    vi.fn(async (input) => {
      const after = Number(new URL(String(input), "http://nas").searchParams.get("after"));
      cursors.push(after);
      if (after === 100 && fail) return apiError(503, "unavailable");
      return json({
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
