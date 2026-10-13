import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const tus = vi.hoisted(() => {
  class FakeUpload {
    url: string | null;
    constructor(readonly file: File, readonly options: Record<string, any>) {
      this.url = options.uploadUrl ?? null;
      uploads.push(this);
    }
    start() {}
    abort() { return Promise.resolve(); }
    created(id: string) { this.url = `http://nas/api/v1/uploads/${id}`; this.options.onUploadUrlAvailable(); }
    fail(method: string, status: number, code: string) {
      this.options.onError(Object.assign(new Error(code), {
        originalRequest: { getMethod: () => method },
        originalResponse: { getStatus: () => status, getBody: () => JSON.stringify({ error: { code, message: code } }) },
      }));
    }
  }
  const uploads: FakeUpload[] = [];
  return { FakeUpload, uploads };
});
vi.mock("tus-js-client", () => ({ Upload: tus.FakeUpload }));

import { transferStore, type UploadTarget } from "./store";
import { TransferCenter } from "./TransferCenter";
import type { Transfer } from "./transfersApi";

const target: UploadTarget = { spaceId: "space:shared", spaceKind: "shared", parentId: "file:photos", folders: [{ id: "file:photos", name: "照片" }] };
let server: Transfer[];

function transfer(change: Partial<Transfer>): Transfer {
  const now = new Date().toISOString();
  return {
    id: "download-1", direction: "download", kind: "file", name: "片段.mp4", location: { spaceId: "space:shared", spaceKind: "shared", folders: [] },
    totalBytes: 1024, transferredBytes: 1024, bytesPerSecond: 0, state: "completed", contentUrl: "/api/v1/transfers/download-1/content",
    createdAt: now, updatedAt: now, finishedAt: now, ...change,
  };
}

beforeEach(() => {
  tus.uploads.length = 0;
  server = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const body = String(input) === "/api/v1/transfers" ? { items: server } : {};
    return { ok: true, status: 200, json: async () => body } as Response;
  }));
});

afterEach(() => {
  transferStore.reset();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const open = () => {
  const onOpenLocation = vi.fn();
  render(<TransferCenter onOpenFiles={() => undefined} onOpenLocation={onOpenLocation} />);
  return onOpenLocation;
};

describe("传输中心", () => {
  it("shows an upload batch with its progress and answers name conflicts from the card", async () => {
    transferStore.attach("user:alice");
    transferStore.enqueue([{ file: new File(["12345678"], "a.jpg") }, { file: new File(["1234"], "b.jpg") }], target);
    tus.uploads[0].created("upload-a");
    tus.uploads[0].options.onProgress(4, 8);
    tus.uploads[1].fail("POST", 409, "name_conflict");
    const onOpenLocation = open();
    const user = userEvent.setup();

    const batch = await screen.findByRole("region", { name: "上传 2 个文件" });
    expect(within(batch).getByText("1 个文件与目标位置的文件同名")).toBeTruthy();
    expect(screen.getByRole("navigation").textContent).toContain("上传2");
    await user.click(within(batch).getByRole("button", { name: /保留两者/ }));
    expect(tus.uploads[2].options.metadata.conflict).toBe("rename");
    expect(within(batch).queryByText("1 个文件与目标位置的文件同名")).toBeNull();

    await user.click(within(batch).getByRole("button", { name: /到 共享空间 › 照片/ }));
    expect(onOpenLocation).toHaveBeenCalledWith(expect.objectContaining({ spaceId: "space:shared" }));
    await user.click(within(batch).getByRole("button", { name: "展开文件" }));
    expect(within(batch).getByRole("article", { name: "上传 a.jpg" })).toBeTruthy();
  });

  it("explains a full volume and continues when asked", async () => {
    transferStore.attach("user:alice");
    transferStore.enqueue([{ file: new File(["1234"], "big.iso") }], target);
    tus.uploads[0].fail("PATCH", 507, "insufficient_storage");
    open();
    const user = userEvent.setup();
    expect(await screen.findByText("数据卷空间不足", { selector: ".tc-banner strong" })).toBeTruthy();
    expect(screen.getByRole("article", { name: "上传 big.iso" }).textContent).toContain("等待恢复");
    await user.click(screen.getByRole("button", { name: /继续上传/ }));
    expect(tus.uploads).toHaveLength(2);
    expect(screen.queryByText("数据卷空间不足")).toBeNull();
  });

  it("lists finished transfers by day and fetches a download again", async () => {
    const yesterday = new Date(Date.now() - 86_400_000).toISOString();
    server = [
      transfer({}),
      transfer({ id: "upload-1", direction: "upload", name: "报告.pdf", state: "failed", error: { code: "service_restarted", message: "" }, contentUrl: undefined, createdAt: yesterday, finishedAt: yesterday }),
    ];
    transferStore.attach("user:alice");
    open();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /传输记录/ }));
    const today = await screen.findByRole("region", { name: "今天" });
    expect(screen.getByRole("region", { name: "昨天" }).textContent).toContain("NAS 服务重启，上传需要重新开始");
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    await user.click(within(today).getByRole("button", { name: "重新下载" }));
    expect(click.mock.contexts[0]).toHaveProperty("href", expect.stringContaining("/api/v1/transfers/download-1/content"));
    await user.click(screen.getByRole("radio", { name: "失败与取消" }));
    expect(screen.queryByRole("region", { name: "今天" })).toBeNull();
  });

  it("continues uploads a closed page left behind once the same files are picked", async () => {
    server = [transfer({
      id: "upload-x", direction: "upload", name: "x.mov", totalBytes: 4, transferredBytes: 2, state: "interrupted",
      lastModified: 7, contentUrl: undefined, finishedAt: undefined,
    })];
    transferStore.attach("user:alice");
    const { container } = render(<TransferCenter onOpenFiles={() => undefined} onOpenLocation={() => undefined} />);
    const user = userEvent.setup();
    expect(await screen.findByText("1 个上传在上次中断")).toBeTruthy();
    const input = container.querySelector<HTMLInputElement>(".tc-banner input[type=file]")!;
    await user.upload(input, new File(["abcd"], "x.mov", { lastModified: 7 }));
    await waitFor(() => expect(tus.uploads).toHaveLength(1));
    expect(tus.uploads[0].options.uploadUrl).toBe("/api/v1/uploads/upload-x");
    expect(screen.getByText(/已继续 1 个上传/)).toBeTruthy();
  });

  it("shows where to start when nothing moves", async () => {
    transferStore.attach("user:alice");
    open();
    expect(await screen.findByText("没有正在进行的传输")).toBeTruthy();
    expect(screen.getByRole("button", { name: /打开文件管理/ })).toBeTruthy();
  });
});
