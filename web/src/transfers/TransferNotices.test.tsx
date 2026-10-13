import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const tus = vi.hoisted(() => {
  class FakeUpload {
    url: string | null = null;
    constructor(readonly file: File, readonly options: Record<string, any>) { uploads.push(this); }
    start() {}
    abort() { return Promise.resolve(); }
    fail(status: number, code: string) {
      this.options.onError(Object.assign(new Error(code), {
        originalRequest: { getMethod: () => "POST" },
        originalResponse: { getStatus: () => status, getBody: () => JSON.stringify({ error: { code, message: code } }) },
      }));
    }
  }
  const uploads: FakeUpload[] = [];
  return { FakeUpload, uploads };
});
vi.mock("tus-js-client", () => ({ Upload: tus.FakeUpload }));

import { transferStore, type UploadTarget } from "./store";
import { TransferIndicator, TransferToasts } from "./TransferNotices";

const target: UploadTarget = { spaceId: "space:shared", spaceKind: "shared", parentId: "", folders: [] };

beforeEach(() => {
  tus.uploads.length = 0;
  vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ items: [] }) }) as Response));
  transferStore.attach("user:alice");
});

afterEach(() => {
  transferStore.reset();
  vi.unstubAllGlobals();
});

describe("transfer notices on the desktop", () => {
  it("asks about taken names once for the batch and says when the batch finished", async () => {
    const onOpen = vi.fn();
    render(<><TransferIndicator onOpen={onOpen} /><TransferToasts hidden={false} onOpen={onOpen} /></>);
    const user = userEvent.setup();
    act(() => { transferStore.enqueue([{ file: new File(["a"], "a.jpg") }, { file: new File(["b"], "b.jpg") }], target); });
    expect(screen.getByRole("button", { name: /传输中心：2 个任务，已完成 0%/ })).toBeTruthy();
    act(() => { tus.uploads[0].fail(409, "name_conflict"); });
    expect(screen.getByRole("alertdialog", { name: "同名文件" }).textContent).toContain("共享空间 中已有 a.jpg");
    expect(screen.getByRole("button", { name: /有任务需要处理/ })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: /跳过/ }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    act(() => { tus.uploads[1].options.onSuccess(); });
    expect(screen.getByText("已上传 1 个文件")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /传输中心/ })).toBeNull();
    await user.click(screen.getByRole("button", { name: "查看传输" }));
    expect(onOpen).toHaveBeenCalled();
  });

  it("raises a held queue and stays quiet while the transfer center is in front", () => {
    const { rerender } = render(<TransferToasts hidden={false} onOpen={() => undefined} />);
    act(() => { transferStore.enqueue([{ file: new File(["a"], "a.jpg") }], target); });
    act(() => { tus.uploads[0].fail(423, "volume_unavailable"); });
    expect(screen.getByText("数据卷离线或只读")).toBeTruthy();
    rerender(<TransferToasts hidden onOpen={() => undefined} />);
    expect(screen.queryByText("数据卷离线或只读")).toBeNull();
  });
});
