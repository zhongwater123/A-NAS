import AsyncStorage from "@react-native-async-storage/async-storage";
import {
  Directory,
  File,
  FileMode,
  Paths,
  type FileHandle,
} from "expo-file-system";
import * as Sharing from "expo-sharing";
import { Upload, type DetailedError } from "tus-js-client";
import type { DocumentPickerAsset } from "expo-document-picker";
import { NasClient, unwrap, errorText, type FileEntry } from "../nas/client";
import { NasTusStack, downloadFile } from "./protocol";

export type Job = {
  id: string;
  name: string;
  direction: "upload" | "download";
  size: number;
  done: number;
  state: "queued" | "running" | "paused" | "completed" | "failed" | "canceled";
  uri: string;
  spaceId?: string;
  parentId?: string;
  entryId?: string;
  serverId?: string;
  uploadUrl?: string;
  contentUrl?: string;
  etag?: string;
  error?: string;
};
export class TransferEngine {
  private jobs: Job[] = [];
  private listeners = new Set<() => void>();
  private active = new Map<string, { abort(): void; done: Promise<void> }>();
  private writes: Promise<unknown> = Promise.resolve();
  private disposed = false;
  private suspended = false;
  private scope: string;
  private folder: Directory;
  private key: string;
  constructor(private nas: NasClient) {
    this.scope = nas.scope;
    this.key = `anas-transfers-v1:${this.scope}`;
    this.folder = new Directory(
      Paths.document,
      "transfers",
      encodeURIComponent(this.scope),
    );
    this.folder.create({ intermediates: true, idempotent: true });
  }
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  snapshot = () => this.jobs;
  private publish() {
    this.listeners.forEach((l) => l());
    const text = JSON.stringify(this.jobs);
    this.writes = this.writes
      .catch(() => {})
      .then(() => AsyncStorage.setItem(this.key, text));
  }
  private patch(id: string, value: Partial<Job>) {
    this.jobs = this.jobs.map((j) => (j.id === id ? { ...j, ...value } : j));
    this.publish();
  }
  async restore() {
    const saved = await AsyncStorage.getItem(this.key);
    if (!saved || this.disposed) return;
    const jobs = JSON.parse(saved) as Job[];
    this.jobs = jobs
      .filter((j) => j.uri.startsWith(`${this.folder.uri.replace(/\/$/, "")}/`))
      .map((j) => ({
        ...j,
        state: ["running", "queued"].includes(j.state) ? "paused" : j.state,
      }));
    this.publish();
  }
  private makeId() {
    return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
  }
  async upload(
    files: DocumentPickerAsset[],
    spaceId: string,
    parentId?: string,
  ) {
    for (const asset of files) {
      const id = this.makeId(),
        file = new File(this.folder, `${id}.upload`);
      new File(asset.uri).copy(file);
      this.jobs = [
        {
          id,
          name: asset.name,
          uri: file.uri,
          size: file.size,
          done: 0,
          direction: "upload",
          state: "queued",
          spaceId,
          parentId,
        },
        ...this.jobs,
      ];
    }
    this.publish();
    this.pump();
  }
  async download(entry: FileEntry) {
    const id = this.makeId(),
      file = new File(
        this.folder,
        `${id}-${entry.name.replace(/[\\/\u0000-\u001f]/g, "_")}`,
      );
    this.jobs = [
      {
        id,
        entryId: entry.id,
        name: entry.name,
        uri: file.uri,
        size: entry.sizeBytes,
        done: 0,
        direction: "download",
        state: "queued",
      },
      ...this.jobs,
    ];
    this.publish();
    this.pump();
  }
  private pump() {
    if (this.disposed || this.suspended || this.nas.scope !== this.scope)
      return;
    for (const job of this.jobs) {
      if (this.active.size >= 2) break;
      if (job.state !== "queued" || this.active.has(job.id)) continue;
      const controller = new AbortController();
      const active = {
        abort: () => controller.abort(),
        done: Promise.resolve(),
      };
      this.active.set(job.id, active);
      this.patch(job.id, { state: "running", error: undefined });
      active.done = this.run(job, controller.signal)
        .then(() => {
          if (!controller.signal.aborted)
            this.patch(job.id, {
              state: "completed",
              done: this.jobs.find((j) => j.id === job.id)?.size ?? job.size,
            });
        })
        .catch((error) => {
          if (!controller.signal.aborted)
            this.patch(job.id, { state: "failed", error: errorText(error) });
        })
        .finally(() => {
          this.active.delete(job.id);
          this.pump();
        });
    }
  }
  private async run(job: Job, signal: AbortSignal) {
    if (job.direction === "upload") return this.runUpload(job, signal);
    if (!job.contentUrl) {
      const transfer = await unwrap(
        this.nas.api.POST("/api/v1/transfers/downloads", {
          params: { header: { "X-CSRF-Token": this.nas.csrf } },
          body: { entryIds: [job.entryId!] },
        }),
      );
      job = {
        ...job,
        serverId: transfer.id,
        contentUrl: transfer.contentUrl,
        size: transfer.totalBytes,
      };
      this.patch(job.id, {
        serverId: job.serverId,
        contentUrl: job.contentUrl,
        size: job.size,
      });
    }
    if (signal.aborted) return;
    if (!job.contentUrl) throw new Error("Missing content URL");
    const file = new File(job.uri);
    let handle: FileHandle;
    await downloadFile(this.nas, {
      url: job.contentUrl,
      total: job.size,
      etag: job.etag,
      signal,
      version: (etag) => this.patch(job.id, { etag }),
      progress: (done) => this.patch(job.id, { done }),
      sink: {
        size: () => (file.exists ? file.size : 0),
        open: (append) => {
          if (!file.exists) file.create();
          handle = file.open(append ? FileMode.Append : FileMode.Truncate);
        },
        write: (data) => handle.writeBytes(data),
        close: () => handle.close(),
      },
    });
  }
  private runUpload(job: Job, signal: AbortSignal): Promise<void> {
    return new Promise((resolve, reject) => {
      const file = new File(job.uri);
      if (!file.exists) {
        reject(new Error("Local upload file missing"));
        return;
      }
      const upload = new Upload(file, {
        endpoint: this.nas.url("/api/v1/uploads"),
        uploadUrl: job.uploadUrl ? this.nas.url(job.uploadUrl) : undefined,
        uploadSize: file.size,
        chunkSize: 2 * 1024 * 1024,
        retryDelays: [0, 1000, 3000, 5000],
        storeFingerprintForResuming: false,
        httpStack: new NasTusStack(this.nas),
        metadata: {
          filename: job.name,
          spaceId: job.spaceId!,
          parentId: job.parentId ?? "",
          conflict: "rename",
        },
        fileReader: {
          openFile: async () => ({
            size: file.size,
            slice: async (start, end) => {
              const handle = file.open(FileMode.ReadOnly);
              try {
                handle.offset = start;
                const value = await handle.readBytes(end - start);
                return {
                  // tus validates its final slice using Blob's size contract.
                  value: Object.assign(value, { size: value.byteLength }),
                  done: end >= file.size,
                };
              } finally {
                handle.close();
              }
            },
            close: () => {},
          }),
        },
        onShouldRetry: (error: DetailedError) => {
          const status = error.originalResponse?.getStatus();
          return (
            !signal.aborted &&
            (!status || status === 409 || status === 423 || status >= 500)
          );
        },
        onUploadUrlAvailable: () => {
          if (upload.url) {
            this.nas.url(upload.url);
            this.patch(job.id, {
              uploadUrl: upload.url,
              serverId: new URL(upload.url).pathname.split("/").pop(),
            });
          }
        },
        onChunkComplete: (_chunk, done) => this.patch(job.id, { done }),
        onSuccess: () => {
          signal.removeEventListener("abort", abort);
          resolve();
        },
        onError: (error) => {
          signal.removeEventListener("abort", abort);
          reject(error);
        },
      });
      const abort = () => {
        void upload.abort().then(resolve, reject);
      };
      signal.addEventListener("abort", abort, { once: true });
      if (signal.aborted) abort();
      else upload.start();
    });
  }
  async pause(id: string) {
    this.patch(id, { state: "paused" });
    const active = this.active.get(id);
    active?.abort();
    await active?.done;
    const job = this.jobs.find((j) => j.id === id);
    if (job?.direction === "upload" && job.serverId)
      await this.nas.api
        .POST("/api/v1/transfers/{transferId}/pause", {
          params: {
            path: { transferId: job.serverId },
            header: { "X-CSRF-Token": this.nas.csrf },
          },
        })
        .catch(() => {});
  }
  resume(id: string) {
    this.patch(id, { state: "queued", error: undefined });
    this.pump();
  }
  async cancel(id: string) {
    await this.pause(id);
    const job = this.jobs.find((j) => j.id === id);
    if (job?.serverId)
      await unwrap(
        this.nas.api.POST("/api/v1/transfers/{transferId}/cancel", {
          params: {
            path: { transferId: job.serverId },
            header: { "X-CSRF-Token": this.nas.csrf },
          },
        }),
      );
    if (job) {
      const file = new File(job.uri);
      if (file.exists) file.delete();
    }
    this.patch(id, { state: "canceled" });
  }
  async share(job: Job) {
    if (
      job.state === "completed" &&
      job.direction === "download" &&
      (await Sharing.isAvailableAsync())
    )
      await Sharing.shareAsync(job.uri, { dialogTitle: job.name });
  }
  async suspend() {
    this.suspended = true;
    await Promise.all(
      this.jobs
        .filter((j) => ["running", "queued"].includes(j.state))
        .map((j) => this.pause(j.id)),
    );
    await this.writes;
  }
  foreground() {
    this.suspended = false;
  }
  async dispose() {
    this.disposed = true;
    await this.suspend();
    this.listeners.clear();
  }
}
