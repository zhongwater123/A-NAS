import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const tus = vi.hoisted(() => {
  // FakeUpload stands in for tus-js-client: tests answer its requests.
  class FakeUpload {
    url: string | null;
    started = 0;
    aborted = 0;
    constructor(readonly file: File, readonly options: Record<string, any>) {
      this.url = options.uploadUrl ?? null;
      uploads.push(this);
    }
    start() { this.started += 1; }
    abort() { this.aborted += 1; return Promise.resolve(); }
    created(id: string) { this.url = `http://nas/api/v1/uploads/${id}`; this.options.onUploadUrlAvailable(); }
    fail(method: string, status: number, code = "") {
      const error = Object.assign(new Error(`${method} ${status}`), {
        originalRequest: { getMethod: () => method },
        originalResponse: status ? { getStatus: () => status, getBody: () => (code ? JSON.stringify({ error: { code, message: code } }) : "") } : null,
      });
      this.options.onError(error);
    }
  }
  const uploads: FakeUpload[] = [];
  return { FakeUpload, uploads };
});
vi.mock("tus-js-client", () => ({ Upload: tus.FakeUpload }));

const api = vi.hoisted(() => ({ sessionEnded: vi.fn() }));
vi.mock("../api", async (original) => ({ ...(await original<typeof import("../api")>()), sessionEnded: api.sessionEnded }));

import { classifyUploadError, TransferStore, type UploadTarget } from "./store";
import type { Transfer } from "./transfersApi";

const target: UploadTarget = { spaceId: "space:shared", spaceKind: "shared", parentId: "file:photos", folders: [{ id: "file:photos", name: "照片" }] };
const file = (name: string, size = 10, lastModified = 1_760_000_000_000) => new File(["x".repeat(size)], name, { lastModified });

let server: Transfer[];
let calls: Array<{ path: string; method: string }>;
let store: TransferStore;

beforeEach(() => {
  vi.useFakeTimers();
  tus.uploads.length = 0;
  server = [];
  calls = [];
  api.sessionEnded.mockReset();
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    calls.push({ path, method: init?.method ?? "GET" });
    const body = path === "/api/v1/transfers" ? { items: server } : { id: path.split("/")[4], state: path.endsWith("/cancel") ? "canceled" : "paused" };
    return { ok: true, status: 200, json: async () => body } as Response;
  }));
  store = new TransferStore();
  store.attach("user:alice");
});

afterEach(() => {
  store.reset();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const phases = () => store.getSnapshot().uploads.map((upload) => upload.phase);

describe("the upload queue", () => {
  it("sends three files at a time and continues when one finishes", async () => {
    const uploaded = vi.fn();
    store.onUploaded(uploaded);
    store.enqueue(["a.jpg", "b.jpg", "c.jpg", "d.jpg"].map((name) => ({ file: file(name) })), target);
    expect(tus.uploads).toHaveLength(3);
    expect(phases()).toEqual(["running", "running", "running", "queued"]);
    expect(tus.uploads[0].options.endpoint).toBe("/api/v1/uploads");
    expect(tus.uploads[0].options.metadata).toMatchObject({ filename: "a.jpg", spaceId: "space:shared", parentId: "file:photos", conflict: "ask", relativePath: "" });

    tus.uploads[0].created("upload-a");
    tus.uploads[0].options.onProgress(5, 10);
    await vi.advanceTimersByTimeAsync(200);
    expect(store.getSnapshot().uploads[0]).toMatchObject({ transferId: "upload-a", transferred: 5 });
    tus.uploads[0].options.onSuccess();
    expect(phases()).toEqual(["completed", "running", "running", "running"]);
    expect(uploaded).toHaveBeenCalledWith(target);
    expect(store.unfinished()).toBe(3);
  });

  it("asks once about taken names and remembers the answer for the batch", () => {
    const batch = store.enqueue([{ file: file("a.jpg") }, { file: file("b.jpg") }], target);
    tus.uploads[0].fail("POST", 409, "name_conflict");
    expect(phases()).toEqual(["conflict", "running"]);
    store.resolveConflicts(batch, "rename", true);
    expect(tus.uploads[2].options.metadata.conflict).toBe("rename");
    // A later conflict of the same batch follows the remembered answer.
    tus.uploads[1].fail("POST", 409, "name_conflict");
    expect(tus.uploads[3].options.metadata.conflict).toBe("rename");
    const other = store.enqueue([{ file: file("c.jpg") }], target);
    tus.uploads[4].fail("POST", 409, "name_conflict");
    store.resolveConflicts(other, "skip", false);
    expect(store.getSnapshot().uploads.at(-1)?.phase).toBe("skipped");
  });

  it("waits for the network and resumes every upload where it stopped", async () => {
    store.enqueue([{ file: file("a.jpg") }, { file: file("b.jpg") }], target);
    tus.uploads[0].created("upload-a");
    tus.uploads[1].created("upload-b");
    tus.uploads[0].fail("PATCH", 0);
    expect(store.getSnapshot().holds).toEqual(["offline"]);
    expect(phases()).toEqual(["waiting", "waiting"]);
    expect(tus.uploads[1].aborted).toBe(1);
    window.dispatchEvent(new Event("online"));
    expect(phases()).toEqual(["running", "running"]);
    // Known uploads resume by address and never start a second copy.
    expect(tus.uploads[2].options).toMatchObject({ endpoint: null, uploadUrl: "/api/v1/uploads/upload-a" });
    expect(tus.uploads[3].options).toMatchObject({ endpoint: null, uploadUrl: "/api/v1/uploads/upload-b" });
  });

  it("holds the queue while the volume is full until the user continues", () => {
    store.enqueue([{ file: file("a.jpg") }, { file: file("b.jpg") }, { file: file("c.jpg") }, { file: file("d.jpg") }], target);
    tus.uploads[0].fail("PATCH", 507, "insufficient_storage");
    expect(store.getSnapshot().holds).toEqual(["space"]);
    expect(phases()).toEqual(["waiting", "waiting", "waiting", "queued"]);
    expect(tus.uploads).toHaveLength(3);
    store.release("space");
    expect(phases()).toEqual(["running", "running", "running", "queued"]);
  });

  it("pauses on an expired session and continues after the same account signs in", () => {
    store.enqueue([{ file: file("a.jpg") }], target);
    tus.uploads[0].created("upload-a");
    tus.uploads[0].fail("PATCH", 401, "authentication_required");
    expect(api.sessionEnded).toHaveBeenCalled();
    expect(store.getSnapshot().holds).toEqual(["session"]);
    store.attach("user:alice");
    expect(phases()).toEqual(["running"]);
    expect(tus.uploads[1].options.uploadUrl).toBe("/api/v1/uploads/upload-a");
    store.attach("user:bob");
    expect(store.getSnapshot().uploads).toEqual([]);
  });

  it("starts a lost upload again once, and fails the folder's uploads when it is gone", () => {
    store.enqueue([{ file: file("a.jpg") }], target);
    tus.uploads[0].created("upload-a");
    tus.uploads[0].fail("PATCH", 404);
    expect(store.getSnapshot().uploads[0]).toMatchObject({ phase: "running", restarted: true, transferred: 0 });
    expect(tus.uploads[1].options.endpoint).toBe("/api/v1/uploads");
    tus.uploads[1].created("upload-a2");
    tus.uploads[1].fail("HEAD", 404);
    expect(store.getSnapshot().uploads[0]).toMatchObject({ phase: "failed", error: { code: "upload_lost" } });

    store.enqueue(["b", "c", "d", "e"].map((name) => ({ file: file(name) })), target);
    tus.uploads[2].fail("POST", 404, "not_found");
    expect(phases().slice(1)).toEqual(["failed", "running", "running", "failed"]);
  });

  it("pauses, resumes and cancels on the NAS as well", async () => {
    store.enqueue([{ file: file("a.jpg") }], target);
    tus.uploads[0].created("upload-a");
    store.pause(store.getSnapshot().uploads[0].key);
    expect(tus.uploads[0].aborted).toBe(1);
    expect(phases()).toEqual(["paused"]);
    await vi.advanceTimersByTimeAsync(0);
    expect(calls).toContainEqual({ path: "/api/v1/transfers/upload-a/pause", method: "POST" });
    store.resume(store.getSnapshot().uploads[0].key);
    expect(tus.uploads[1].options.uploadUrl).toBe("/api/v1/uploads/upload-a");
    store.cancel(store.getSnapshot().uploads[0].key);
    await vi.advanceTimersByTimeAsync(0);
    expect(phases()).toEqual(["canceled"]);
    expect(calls).toContainEqual({ path: "/api/v1/transfers/upload-a/cancel", method: "POST" });
  });

  it("matches files picked again to uploads a closed page left behind", async () => {
    const left = (id: string, name: string, size: number, lastModified: number): Transfer => ({
      id, direction: "upload", kind: "file", name, location: { spaceId: "space:shared", spaceKind: "shared", folders: [{ id: "file:photos", name: "照片" }] },
      totalBytes: size, transferredBytes: 4, bytesPerSecond: 0, state: "interrupted", batch: "b-old", lastModified, createdAt: "2026-10-12T08:00:00Z", updatedAt: "2026-10-12T08:00:00Z",
    });
    server = [left("upload-x", "x.mov", 10, 1), left("upload-y", "y.mov", 10, 2)];
    await store.refresh();
    const result = store.reattach([file("x.mov", 10, 1), file("y.mov", 10, 99), file("z.mov", 10, 1)]);
    expect(result).toEqual({ matched: 1, unmatched: 2 });
    expect(tus.uploads[0].options).toMatchObject({ uploadUrl: "/api/v1/uploads/upload-x", endpoint: null });
    expect(store.getSnapshot().uploads[0]).toMatchObject({ transferred: 4, target: { parentId: "file:photos" } });
  });

  it("asks before the page closes while uploads are unfinished", () => {
    const event = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
    store.enqueue([{ file: file("a.jpg") }], target);
    const leaving = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(leaving);
    expect(leaving.defaultPrevented).toBe(true);
  });
});

describe("classifyUploadError", () => {
  const failure = (method: string, status: number, body = "") => Object.assign(new Error("failed"), {
    originalRequest: { getMethod: () => method },
    originalResponse: { getStatus: () => status, getBody: () => body },
  });
  it("tells what the queue should do", () => {
    expect(classifyUploadError(new Error("NotReadableError")).kind).toBe("file");
    expect(classifyUploadError(failure("PATCH", 409, "ERR_MISMATCHED_OFFSET: mismatched offset")).kind).toBe("server");
    expect(classifyUploadError(failure("POST", 409, JSON.stringify({ error: { code: "folder_blocked" } }))).kind).toBe("invalid");
    expect(classifyUploadError(failure("PATCH", 423, "ERR_UPLOAD_LOCKED")).kind).toBe("busy");
    expect(classifyUploadError(failure("POST", 423, JSON.stringify({ error: { code: "volume_unavailable" } }))).kind).toBe("volume");
    expect(classifyUploadError(failure("POST", 403, JSON.stringify({ error: { code: "forbidden" } }))).kind).toBe("denied");
    expect(classifyUploadError(failure("PATCH", 410, JSON.stringify({ error: { code: "canceled" } }))).kind).toBe("canceled");
  });
});
