import { act, render, screen, waitFor } from "@testing-library/react";
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

afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
});

const settle = () =>
  act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 50));
  });

describe("the montage center's notices on the desktop", () => {
  it("says each job in a line, and why one still waits for VNAS", () => {
    expect(summarize([...notices])).toEqual(["「海边」在等 VNAS 规划", "「雪山」的成片已存进相册"]);
    expect(
      summarize([
        { ...notices[0], detail: "VNAS 没有开启" },
        { ...notices[0], kind: "planning" },
        { ...notices[0], kind: "needs_input", reason: "vnas_unknown" },
      ]),
    ).toEqual(["「海边」在等 VNAS 规划：VNAS 没有开启", "「海边」正在由 VNAS 规划", "「海边」需要你处理一下"]);
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
    await settle();
    expect(screen.queryByRole("status", { name: "剪辑中心提醒" })).toBeNull();
  });

  it("tells again when the reason a job waits changes", async () => {
    let detail: string | undefined;
    server.use(
      http.get("/api/v1/montage/notices", ({ response }) =>
        response(200).json({ items: [detail ? { ...notices[0], detail } : notices[0]] }),
      ),
    );
    const first = render(<MontageNotices onOpen={() => undefined} />);
    await userEvent.click(await screen.findByRole("button", { name: "关闭" }));
    first.unmount();
    detail = "VNAS 还没有接手";
    render(<MontageNotices onOpen={() => undefined} />);
    expect(await screen.findByText("「海边」在等 VNAS 规划：VNAS 还没有接手")).toBeInTheDocument();
  });

  it("asks nothing while the page is hidden, and asks once it shows again", async () => {
    let asked = 0;
    server.use(
      http.get("/api/v1/montage/notices", ({ response }) => {
        asked++;
        return response(200).json({ items: [notices[1]] });
      }),
    );
    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    render(<MontageNotices onOpen={() => undefined} />);
    await settle();
    expect(asked).toBe(0);
    visibility.mockReturnValue("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    expect(await screen.findByText("「雪山」的成片已存进相册")).toBeInTheDocument();
    await waitFor(() => expect(asked).toBe(1));
  });

  it("keeps what the latest request said when an older one answers after it", async () => {
    // The first request is still on its way when the page shows again and asks anew.
    let releaseFirst: () => void = () => undefined;
    const held = new Promise<void>((resolve) => {
      releaseFirst = resolve;
    });
    let asked = 0;
    server.use(
      http.get("/api/v1/montage/notices", async ({ response }) => {
        if (++asked === 1) {
          await held;
          return response(200).json({ items: [notices[0]] });
        }
        return response(200).json({ items: [notices[1]] });
      }),
    );
    render(<MontageNotices onOpen={() => undefined} />);
    await waitFor(() => expect(asked).toBe(1));
    document.dispatchEvent(new Event("visibilitychange"));
    expect(await screen.findByText("「雪山」的成片已存进相册")).toBeInTheDocument();
    releaseFirst();
    await settle();
    expect(screen.getByText("「雪山」的成片已存进相册")).toBeInTheDocument();
    expect(screen.queryByText("「海边」在等 VNAS 规划")).toBeNull();
  });
});
