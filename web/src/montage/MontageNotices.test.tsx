import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { http, server } from "../test/server";
import { MontageNotices, summarize } from "./MontageNotices";

const notices = [
  {
    kind: "waiting_for_planning",
    jobId: "job:1",
    projectId: "p1",
    project: "海边",
    reason: "vnas",
    at: "2026-11-18T10:00:00Z",
  },
  {
    kind: "saved",
    jobId: "job:2",
    projectId: "p2",
    project: "雪山",
    filmAssetId: "photo:f",
    at: "2026-11-18T10:00:00Z",
  },
] as const;

afterEach(() => localStorage.clear());

describe("the montage center's notices on the desktop", () => {
  it("says each job in a line", () => {
    expect(summarize([...notices])).toEqual(["「海边」在等 VNAS 规划", "「雪山」的成片已存进相册"]);
  });

  it("shows them once on this device and opens the montage center", async () => {
    server.use(http.get("/api/v1/montage/notices", ({ response }) => response(200).json({ items: [...notices] })));
    const opened = vi.fn();
    const { unmount } = render(<MontageNotices onOpen={opened} />);
    expect(await screen.findByText("「海边」在等 VNAS 规划")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /去看看/ }));
    expect(opened).toHaveBeenCalledOnce();
    expect(screen.queryByRole("status", { name: "剪辑中心提醒" })).toBeNull();
    unmount();
    // Told once: the same jobs in the same state show no more.
    render(<MontageNotices onOpen={opened} />);
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
    expect(screen.queryByRole("status", { name: "剪辑中心提醒" })).toBeNull();
  });
});
