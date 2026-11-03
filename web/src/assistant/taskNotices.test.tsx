import { act, render, screen } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";

import { acknowledgeNotification, listNotifications } from "../desktop/api";
import { taskNotice } from "../test/handlers/assistant";
import { acknowledgeTaskNotice, loadTaskNotices, setTaskNoticeMember, useTaskNotices } from "./taskNotices";

vi.mock("../desktop/api", () => ({ acknowledgeNotification: vi.fn(), listNotifications: vi.fn() }));
const notice = (id: string) =>
  taskNotice(id, { taskId: "aaaaaaaaaaaa", name: id, status: "ok", ranAt: "2026-11-03T10:00:00+08:00" });
function Read({ member }: { member: string }) {
  return (
    <p>
      {useTaskNotices(member)
        .map((item) => item.id)
        .join(",") || "empty"}
    </p>
  );
}
beforeEach(() => {
  vi.resetAllMocks();
  setTaskNoticeMember("initial");
});

it("ignores a previous member's late read, even if it finishes last", async () => {
  let finish!: (items: ReturnType<typeof notice>[]) => void;
  vi.mocked(listNotifications).mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  const page = render(<Read member="a" />);
  const old = loadTaskNotices();
  page.rerender(<Read member="b" />);
  vi.mocked(listNotifications).mockResolvedValueOnce([notice("b-result")]);
  await act(async () => {
    await loadTaskNotices();
    finish([notice("a-private")]);
    await old;
  });
  expect(screen.getByText("b-result")).toBeVisible();
  expect(screen.queryByText("a-private")).toBeNull();
});

it("retains unread results when acknowledgement fails", async () => {
  render(<Read member="a" />);
  vi.mocked(listNotifications).mockResolvedValue([notice("unread")]);
  await act(loadTaskNotices);
  vi.mocked(acknowledgeNotification).mockRejectedValue(new Error("offline"));
  expect(await acknowledgeTaskNotice("unread")).toBe(false);
  expect(screen.getByText("unread")).toBeVisible();
});

it("does not resurrect an acknowledged result from an older in-flight read", async () => {
  render(<Read member="a" />);
  vi.mocked(listNotifications).mockResolvedValueOnce([notice("unread")]);
  await act(loadTaskNotices);
  let finish!: (items: ReturnType<typeof notice>[]) => void;
  vi.mocked(listNotifications).mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  const stale = loadTaskNotices();
  vi.mocked(acknowledgeNotification).mockResolvedValue(undefined);
  await act(async () => {
    expect(await acknowledgeTaskNotice("unread")).toBe(true);
    finish([notice("unread")]);
    await stale;
  });
  expect(screen.getByText("empty")).toBeVisible();
});

it("fences a late receipt across an a-to-b-to-a account change", async () => {
  const page = render(<Read member="a" />);
  vi.mocked(listNotifications).mockResolvedValue([notice("same-id")]);
  await act(loadTaskNotices);
  let finish!: () => void;
  vi.mocked(acknowledgeNotification).mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = () => resolve(undefined);
      }),
  );
  const old = acknowledgeTaskNotice("same-id");
  page.rerender(<Read member="b" />);
  page.rerender(<Read member="a" />);
  await act(loadTaskNotices);
  await act(async () => {
    finish();
    expect(await old).toBe(false);
  });
  expect(screen.getByText("same-id")).toBeVisible();
});
