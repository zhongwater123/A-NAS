import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const tus = vi.hoisted(() => {
  class FakeUpload {
    url: string | null = null;
    constructor(
      readonly file: File,
      readonly options: Record<string, any>,
    ) {
      uploads.push(this);
    }
    start() {}
    abort() {
      return Promise.resolve();
    }
    fail(status: number, code: string) {
      this.options.onError(
        Object.assign(new Error(code), {
          originalRequest: { getMethod: () => "POST" },
          originalResponse: {
            getStatus: () => status,
            getBody: () => JSON.stringify({ error: { code, message: code } }),
          },
        }),
      );
    }
  }
  const uploads: FakeUpload[] = [];
  return { FakeUpload, uploads };
});
vi.mock("tus-js-client", () => ({ Upload: tus.FakeUpload }));

import { json, stubAPI } from "../test/api";
import { transferStore, type UploadTarget } from "./store";
import { TransferIndicator, TransferToasts } from "./TransferNotices";

const target: UploadTarget = { spaceId: "space:shared", spaceKind: "shared", parentId: "", folders: [] };

beforeEach(() => {
  tus.uploads.length = 0;
  stubAPI(vi.fn(async () => json({ items: [] })));
  transferStore.attach("user:alice");
});

afterEach(() => {
  transferStore.reset();
  vi.unstubAllGlobals();
});

describe("transfer notices on the desktop", () => {
  it("asks about taken names once for the batch and says when the batch finished", async () => {
    const onOpen = vi.fn();
    render(
      <>
        <TransferIndicator onOpen={onOpen} />
        <TransferToasts hidden={false} onOpen={onOpen} />
      </>,
    );
    const user = userEvent.setup();
    act(() => {
      transferStore.enqueue([{ file: new File(["a"], "a.jpg") }, { file: new File(["b"], "b.jpg") }], target);
    });
    expect(screen.getByRole("button", { name: "传输中心：正在上传 a.jpg 等 2 个任务，已完成 0%" })).toBeTruthy();
    act(() => {
      tus.uploads[0].fail(409, "name_conflict");
    });
    expect(screen.getByRole("alertdialog", { name: "同名文件" }).textContent).toContain("共享空间 中已有 a.jpg");
    expect(screen.getByRole("button", { name: /有任务需要处理/ })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: /跳过/ }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    act(() => {
      tus.uploads[1].options.onSuccess();
    });
    expect(screen.getByText("已上传 1 个文件")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /传输中心/ })).toBeNull();
    await user.click(screen.getByRole("button", { name: "查看传输" }));
    expect(onOpen).toHaveBeenCalled();
  });

  it("names the file being sent, how many are left, and lists the queue on hover", async () => {
    const { container } = render(<TransferIndicator onOpen={() => undefined} />);
    act(() => {
      transferStore.enqueue(
        ["第 1 集.mkv", "第 2 集.mkv", "第 3 集.mkv", "第 4 集.mkv", "第 5 集.mkv"].map((name) => ({
          file: new File(["12345678"], name),
        })),
        target,
      );
    });
    act(() => {
      tus.uploads[0].options.onProgress(4, 8);
    });
    const pill = screen.getByRole("button", { name: /^传输中心：正在上传 第 1 集\.mkv 等 5 个任务/ });
    expect(pill.textContent).toContain("第 1 集.mkv");
    expect(pill.textContent).toContain("还剩 5 个");
    const list = container.querySelector(".status-transfers-pop")!;
    expect(Array.from(list.querySelectorAll("li .name")).map((name) => name.textContent)).toEqual([
      "第 1 集.mkv",
      "第 2 集.mkv",
      "第 3 集.mkv",
      "第 4 集.mkv",
      "第 5 集.mkv",
    ]);
    // Progress repaints a moment later, not on every byte.
    await waitFor(() => expect(list.querySelector("li .state")!.textContent).toBe("50%"));
  });

  it("lists every file another app queued, not only the one it sends", () => {
    render(<TransferIndicator onOpen={() => undefined} />);
    let handles: Array<ReturnType<typeof transferStore.trackExternal>> = [];
    act(() => {
      handles = ["a.jpg", "b.jpg", "c.jpg"].map((name) =>
        transferStore.trackExternal(
          { key: name, app: "photos", name, size: 4, label: "相册 · 图库", batch: "photos-1" },
          { queued: true },
        ),
      );
    });
    act(() => {
      handles[0].start();
      handles[0].progress(0.5);
    });
    expect(screen.getByRole("button", { name: /正在上传 a\.jpg 等 3 个任务/ })).toBeTruthy();
    act(() => {
      handles[0].done();
      handles[1].start();
    });
    expect(screen.getByRole("button", { name: /正在上传 b\.jpg 等 2 个任务/ })).toBeTruthy();
  });

  it("raises a held queue and stays quiet while the transfer center is in front", () => {
    const { rerender } = render(<TransferToasts hidden={false} onOpen={() => undefined} />);
    act(() => {
      transferStore.enqueue([{ file: new File(["a"], "a.jpg") }], target);
    });
    act(() => {
      tus.uploads[0].fail(423, "volume_unavailable");
    });
    expect(screen.getByText("数据卷离线或只读")).toBeTruthy();
    rerender(<TransferToasts hidden onOpen={() => undefined} />);
    expect(screen.queryByText("数据卷离线或只读")).toBeNull();
  });
});
