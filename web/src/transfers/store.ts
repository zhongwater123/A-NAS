import { type DetailedError, Upload } from "tus-js-client";
import { useSyncExternalStore } from "react";

import { APIError, csrfHeader, passWriteGate, sessionEnded } from "../api";
import { RateMeter } from "./meter";
import {
  cancelTransfer,
  clearFinishedTransfers,
  createDownload,
  getTransfer,
  listTransfers,
  pauseTransfer,
  removeTransfer,
  type Transfer,
  type TransferFailure,
  type TransferLocation,
  uploadEndpoint,
  uploadURL,
} from "./transfersApi";

export type ConflictPolicy = "ask" | "rename" | "replace";
export type ConflictChoice = "skip" | "rename" | "replace";
export type UploadPhase =
  | "queued"
  | "running"
  | "paused"
  | "waiting"
  | "conflict"
  | "completed"
  | "failed"
  | "canceled"
  | "skipped";
// A hold stops the whole queue until its cause clears: uploads wait instead
// of failing one by one.
export type Hold = "offline" | "session" | "volume" | "space";

export interface UploadTarget extends TransferLocation {
  parentId: string;
  // label names the target where its folders are not known, such as a media
  // center library ("影视中心 · 家庭影院 / 剧集").
  label?: string;
}

export interface LocalUpload {
  key: string;
  batch: string;
  file: File;
  name: string;
  // Folders a folder upload creates below the target.
  directories: string[];
  size: number;
  target: UploadTarget;
  conflict: ConflictPolicy;
  phase: UploadPhase;
  transferred: number;
  speed: number;
  transferId?: string;
  error?: TransferFailure;
  hold?: Hold;
  savedName?: string;
  replaced?: boolean;
  // restarted is set when the NAS lost the upload and it began again;
  // superseded holds the transfers it replaced, which belong to it.
  restarted?: boolean;
  superseded?: string[];
  createdAt: number;
  finishedAt?: number;
}

export interface UploadBatch {
  id: string;
  target: UploadTarget;
  createdAt: number;
  // choice answers every later name conflict of the batch.
  choice?: ConflictChoice;
}

// ExternalUpload is an upload another app runs and reports here: photos sent
// to the photo library, or videos the media center sends into a library.
export interface ExternalUpload {
  key: string;
  app: "photos" | "media";
  name: string;
  size: number;
  label: string;
  // batch groups the files one action added, as for this browser's uploads.
  batch?: string;
  progress: number;
  phase: "queued" | "running" | "completed" | "failed" | "canceled";
  error?: string;
  createdAt: number;
  finishedAt?: number;
}

// TransferRates are the account's speeds right now, in bytes per second,
// measured over all bytes moved rather than per file.
export interface TransferRates {
  upload: number;
  download: number;
  // batches holds the speed of each batch this browser uploads.
  batches: Record<string, number>;
}

export interface TransferSnapshot {
  uploads: LocalUpload[];
  batches: UploadBatch[];
  external: ExternalUpload[];
  server: Transfer[];
  holds: Hold[];
  rates: TransferRates;
  loaded: boolean;
}

const noRates: TransferRates = { upload: 0, download: 0, batches: {} };

export const parallelUploads = 3;
const chunkSize = 8 * 1024 * 1024;
const retryDelays = [0, 1000, 3000, 6000, 10000];
const unfinishedPhases: UploadPhase[] = ["queued", "running", "paused", "waiting", "conflict"];

export type FailureKind =
  | "network"
  | "file"
  | "session"
  | "denied"
  | "conflict"
  | "missing"
  | "lost"
  | "invalid"
  | "volume"
  | "space"
  | "toolarge"
  | "busy"
  | "canceled"
  | "server";
export interface ClassifiedFailure {
  kind: FailureKind;
  code: string;
  message: string;
}

// classifyUploadError says what an upload failure means for the queue. The
// product API answers JSON errors; tusd's own protocol errors are text.
export function classifyUploadError(error: Error): ClassifiedFailure {
  const detailed = error as Partial<DetailedError>;
  // The member did not allow the VNAS step that started it; a retry must not
  // outlast the step and go without asking.
  const cause = detailed.causingError;
  if (cause instanceof APIError && cause.code === "not_allowed_for_vnas")
    return { kind: "denied", code: cause.code, message: cause.message };
  if (!detailed.originalRequest) return { kind: "file", code: "file_unreadable", message: error.message };
  const response = detailed.originalResponse;
  const status = response?.getStatus() ?? 0;
  if (!response || status === 0) return { kind: "network", code: "network_error", message: error.message };
  let code = "";
  let message = "";
  try {
    const body = JSON.parse(response.getBody() || "{}") as { error?: { code?: string; message?: string } };
    code = body.error?.code ?? "";
    message = body.error?.message ?? "";
  } catch {
    message = response.getBody();
  }
  const failure = (kind: FailureKind, fallback: string): ClassifiedFailure => ({
    kind,
    code: code || fallback,
    message,
  });
  switch (status) {
    case 401:
      return failure("session", "authentication_required");
    case 403:
      return code === "csrf_failed" ? failure("session", code) : failure("denied", "forbidden");
    // The product API's not_found is the target folder; tusd's own 404 is
    // an upload the NAS no longer knows.
    case 404:
      return code === "not_found" ? failure("missing", code) : failure("lost", "upload_lost");
    case 409:
      return code === "name_conflict"
        ? failure("conflict", code)
        : code === "folder_blocked"
          ? failure("invalid", code)
          : failure("server", "upload_offset");
    case 410:
      return failure("canceled", "canceled");
    case 423:
      return code === "volume_unavailable" ? failure("volume", code) : failure("busy", "upload_locked");
    case 429:
      return failure("busy", "too_many_uploads");
    // Refused before any byte: this file alone does not fit.
    case 507:
      return detailed.originalRequest.getMethod() === "POST"
        ? { kind: "toolarge", code: "file_too_large", message }
        : failure("space", "insufficient_storage");
  }
  if (status >= 400 && status < 500) return failure("invalid", "validation_failed");
  return failure("server", "internal_error");
}

const retryable = (failure: ClassifiedFailure) =>
  failure.kind === "server" || failure.kind === "busy" || (failure.kind === "network" && navigator.onLine !== false);

// TransferStore runs this browser's uploads and follows every transfer of the
// signed-in account. It lives outside React, so uploads survive closing the
// transfer center or the file manager, and even an expired session: they
// wait and continue after the same account signs in again.
export class TransferStore {
  private snapshot: TransferSnapshot = {
    uploads: [],
    batches: [],
    external: [],
    server: [],
    holds: [],
    rates: noRates,
    loaded: false,
  };
  private listeners = new Set<() => void>();
  private uploadedListeners = new Set<(target: UploadTarget) => void>();
  private running = new Map<string, Upload>();
  // Speeds count the bytes every transfer moved: this browser's uploads as
  // they are sent, and the NAS's other transfers by what each poll adds.
  private moved = { upload: 0, download: 0, batches: new Map<string, number>() };
  private meters = {
    upload: new RateMeter(),
    download: new RateMeter(),
    batches: new Map<string, RateMeter>(),
    items: new Map<string, RateMeter>(),
  };
  // sent is how far each upload of this browser had come; seen is what each
  // NAS transfer had moved at the previous poll, once the first poll ran.
  private sent = new Map<string, number>();
  private seen?: Map<string, number>;
  private rateTimer?: ReturnType<typeof setInterval>;
  private userID?: string;
  private counter = 0;
  private watchers = 0;
  private pollTimer?: ReturnType<typeof setTimeout>;
  private emitTimer?: ReturnType<typeof setTimeout>;
  private holdTimers = new Map<Hold, ReturnType<typeof setInterval>>();
  private listening = false;

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  getSnapshot = () => this.snapshot;

  // onUploaded tells a view that a file arrived in a folder, so it can reload.
  onUploaded(listener: (target: UploadTarget) => void) {
    this.uploadedListeners.add(listener);
    return () => {
      this.uploadedListeners.delete(listener);
    };
  }

  // attach starts following the signed-in account. Another account's
  // uploads are dropped; the same account's continue where they stopped.
  attach(userID: string) {
    if (this.userID && this.userID !== userID) this.reset();
    this.userID = userID;
    this.listen();
    this.meters.upload.add(Date.now(), this.moved.upload);
    this.release("session");
    void this.refresh();
  }

  // reset stops everything, as when signing out.
  reset() {
    for (const upload of this.running.values()) void upload.abort(false);
    this.running.clear();
    this.moved = { upload: 0, download: 0, batches: new Map() };
    this.meters = { upload: new RateMeter(), download: new RateMeter(), batches: new Map(), items: new Map() };
    this.sent.clear();
    this.seen = undefined;
    clearInterval(this.rateTimer);
    this.rateTimer = undefined;
    for (const timer of this.holdTimers.values()) clearInterval(timer);
    this.holdTimers.clear();
    clearTimeout(this.pollTimer);
    this.pollTimer = undefined;
    this.userID = undefined;
    this.set({ uploads: [], batches: [], external: [], server: [], holds: [], rates: noRates, loaded: false });
  }

  // watch polls quickly while someone looks at the transfers.
  watch() {
    this.watchers += 1;
    void this.refresh();
    return () => {
      this.watchers = Math.max(0, this.watchers - 1);
    };
  }

  // unfinished counts this browser's uploads that closing the page would stop.
  unfinished() {
    return (
      this.snapshot.uploads.filter((upload) => unfinishedPhases.includes(upload.phase)).length +
      this.snapshot.external.filter((upload) => upload.phase === "running").length
    );
  }

  // enqueue queues files as one batch; choice answers its name conflicts in
  // advance, for callers that never want to be asked.
  enqueue(
    files: Array<{ file: File; directories?: string[] }>,
    target: UploadTarget,
    options: { choice?: ConflictChoice } = {},
  ): string {
    const now = Date.now();
    const batch = `b-${now.toString(36)}-${((this.counter += 1)).toString(36)}`;
    const uploads = files.map(
      ({ file, directories = [] }): LocalUpload => ({
        key: `u${(this.counter += 1)}`,
        batch,
        file,
        name: file.name,
        directories,
        size: file.size,
        target,
        conflict: "ask",
        phase: "queued",
        transferred: 0,
        speed: 0,
        createdAt: now,
      }),
    );
    this.set({
      uploads: [...this.snapshot.uploads, ...uploads],
      batches: [...this.snapshot.batches, { id: batch, target, createdAt: now, choice: options.choice }],
    });
    this.schedule();
    return batch;
  }

  pause(key: string) {
    const upload = this.find(key);
    if (!upload || !["queued", "running", "waiting"].includes(upload.phase)) return;
    this.stop(key);
    this.update(key, { phase: "paused", speed: 0, hold: undefined });
    if (upload.transferId) void pauseTransfer(upload.transferId).catch(() => undefined);
    this.schedule();
  }

  resume(key: string) {
    const upload = this.find(key);
    if (!upload || !["paused", "failed", "waiting"].includes(upload.phase)) return;
    this.update(key, { phase: "queued", error: undefined, hold: undefined });
    this.schedule();
  }

  // retry sends a failed or canceled upload again; a canceled one starts
  // over, since the NAS discarded what it had.
  retry(key: string) {
    const upload = this.find(key);
    if (upload?.phase === "canceled") {
      this.update(key, {
        phase: "queued",
        transferId: undefined,
        transferred: 0,
        error: undefined,
        finishedAt: undefined,
      });
      this.schedule();
    } else this.resume(key);
  }

  cancel(key: string) {
    const upload = this.find(key);
    if (!upload || !unfinishedPhases.concat("failed").includes(upload.phase)) return;
    this.stop(key);
    this.update(key, { phase: "canceled", speed: 0, hold: undefined, finishedAt: Date.now() });
    if (upload.transferId)
      void cancelTransfer(upload.transferId)
        .then((transfer) => this.merge(transfer))
        .catch(() => undefined);
    this.schedule();
  }

  pauseBatch(batch: string) {
    for (const upload of this.batchUploads(batch)) this.pause(upload.key);
  }
  resumeBatch(batch: string) {
    for (const upload of this.batchUploads(batch)) this.resume(upload.key);
  }
  cancelBatch(batch: string) {
    for (const upload of this.batchUploads(batch)) this.cancel(upload.key);
  }
  pauseAll() {
    for (const upload of this.snapshot.uploads) this.pause(upload.key);
  }
  resumeAll() {
    for (const upload of this.snapshot.uploads) if (upload.phase === "paused") this.resume(upload.key);
  }

  // resolveConflicts answers the name conflicts of a batch; with remember
  // the answer also covers conflicts the batch meets later.
  resolveConflicts(batch: string, choice: ConflictChoice, remember: boolean) {
    if (remember)
      this.set({ batches: this.snapshot.batches.map((item) => (item.id === batch ? { ...item, choice } : item)) });
    for (const upload of this.batchUploads(batch)) if (upload.phase === "conflict") this.answer(upload, choice);
    this.schedule();
  }

  resolveConflict(key: string, choice: ConflictChoice) {
    const upload = this.find(key);
    if (upload?.phase === "conflict") this.answer(upload, choice);
    this.schedule();
  }

  // release lifts a hold so the waiting uploads try again.
  release(hold: Hold) {
    if (!this.snapshot.holds.includes(hold)) return;
    clearInterval(this.holdTimers.get(hold));
    this.holdTimers.delete(hold);
    this.set({
      holds: this.snapshot.holds.filter((item) => item !== hold),
      uploads: this.snapshot.uploads.map((upload) =>
        upload.phase === "waiting" && upload.hold === hold ? { ...upload, phase: "queued", hold: undefined } : upload,
      ),
    });
    this.schedule();
  }

  // dismiss removes a finished transfer from the lists.
  dismiss(id: string) {
    const local = this.snapshot.uploads.find((upload) => upload.key === id || upload.transferId === id);
    const transferId =
      local?.transferId ?? (this.snapshot.server.some((transfer) => transfer.id === id) ? id : undefined);
    this.set({
      uploads: this.snapshot.uploads.filter((upload) => upload !== local),
      external: this.snapshot.external.filter((upload) => upload.key !== id),
      server: this.snapshot.server.filter((transfer) => transfer.id !== transferId),
    });
    this.pruneBatches();
    if (transferId) void removeTransfer(transferId).catch(() => undefined);
  }

  async clearFinished() {
    const finished = (phase: UploadPhase) => !unfinishedPhases.includes(phase) && phase !== "failed";
    this.set({
      uploads: this.snapshot.uploads.filter((upload) => !finished(upload.phase)),
      external: this.snapshot.external.filter((upload) => upload.phase === "running"),
      server: this.snapshot.server.filter((transfer) => !transfer.finishedAt),
    });
    this.pruneBatches();
    await clearFinishedTransfers();
    await this.refresh();
  }

  // reattach resumes uploads a closed or reloaded page left on the NAS: the
  // browser may only read files the user picks again, so the picked files are
  // matched by name, size and modification time.
  reattach(files: File[]) {
    const owned = new Set(this.snapshot.uploads.map((upload) => upload.transferId));
    const candidates = this.snapshot.server.filter(
      (transfer) =>
        transfer.direction === "upload" &&
        (transfer.state === "paused" || transfer.state === "interrupted") &&
        !owned.has(transfer.id),
    );
    const now = Date.now();
    const uploads: LocalUpload[] = [];
    const batches = new Map<string, UploadBatch>();
    for (const file of files) {
      const index = candidates.findIndex(
        (transfer) =>
          transfer.name === file.name &&
          transfer.totalBytes === file.size &&
          (!transfer.lastModified || transfer.lastModified === file.lastModified),
      );
      if (index < 0) continue;
      const [transfer] = candidates.splice(index, 1);
      const target: UploadTarget = { ...transfer.location, parentId: transfer.location.folders.at(-1)?.id ?? "" };
      const batch = transfer.batch || `r-${transfer.id}`;
      if (!this.snapshot.batches.some((item) => item.id === batch))
        batches.set(batch, { id: batch, target, createdAt: Date.parse(transfer.createdAt) || now });
      uploads.push({
        key: `u${(this.counter += 1)}`,
        batch,
        file,
        name: transfer.name,
        directories: [],
        size: file.size,
        target,
        conflict: "ask",
        phase: "queued",
        transferred: transfer.transferredBytes,
        speed: 0,
        transferId: transfer.id,
        createdAt: Date.parse(transfer.createdAt) || now,
      });
    }
    if (uploads.length) {
      this.set({
        uploads: [...this.snapshot.uploads, ...uploads],
        batches: [...this.snapshot.batches, ...batches.values()],
      });
      this.schedule();
    }
    return { matched: uploads.length, unmatched: files.length - uploads.length };
  }

  // abandon cancels uploads a closed page left behind.
  abandon(transferIds: string[]) {
    for (const id of transferIds)
      void cancelTransfer(id)
        .then((transfer) => this.merge(transfer))
        .catch(() => undefined);
  }

  // download asks the NAS to prepare a download and lets the browser fetch
  // it; the NAS counts what it sends, so progress shows here.
  async download(entryIds: string[]) {
    const transfer = await createDownload(entryIds);
    this.merge(transfer);
    fetchDownload(transfer);
    this.poll(500);
    return transfer;
  }

  // retryDownload fetches an interrupted or failed download again; a single
  // file resumes in the browser where its connection dropped.
  retryDownload(transfer: Transfer) {
    this.merge({ ...transfer, state: "pending", error: undefined });
    fetchDownload(transfer);
    this.poll(500);
  }

  async cancelServer(id: string) {
    this.merge(await cancelTransfer(id));
  }

  // trackExternal shows another app's upload here and returns how to report it.
  // An app that keeps its own queue reports each file as soon as it is
  // added (queued), so the transfer center lists the whole queue at once.
  trackExternal(
    upload: Pick<ExternalUpload, "key" | "app" | "name" | "size" | "label" | "batch">,
    options: { queued?: boolean } = {},
  ) {
    const entry: ExternalUpload = {
      ...upload,
      progress: 0,
      phase: options.queued ? "queued" : "running",
      createdAt: Date.now(),
    };
    this.set({ external: [...this.snapshot.external.filter((item) => item.key !== upload.key), entry] });
    const change = (value: Partial<ExternalUpload>, immediate = true) =>
      this.set(
        { external: this.snapshot.external.map((item) => (item.key === upload.key ? { ...item, ...value } : item)) },
        immediate,
      );
    this.sent.set(upload.key, 0);
    return {
      start: () => {
        this.sent.set(upload.key, 0);
        change({ phase: "running", progress: 0, error: undefined, finishedAt: undefined });
      },
      progress: (fraction: number) => {
        this.count(upload.key, upload.batch, Math.round(fraction * upload.size));
        change({ progress: fraction }, false);
      },
      done: () => {
        this.count(upload.key, upload.batch, upload.size);
        change({ phase: "completed", progress: 1, finishedAt: Date.now() });
      },
      fail: (error: string) => change({ phase: "failed", error, finishedAt: Date.now() }),
      cancel: (reason?: string) => change({ phase: "canceled", error: reason, finishedAt: Date.now() }),
    };
  }

  async refresh() {
    if (!this.userID || this.snapshot.holds.includes("session")) return;
    try {
      const server = await listTransfers();
      this.countServer(server);
      this.set({ server, loaded: true });
      this.measure();
    } catch {
      // A failed poll keeps what is shown; the next one tries again.
      if (!this.snapshot.loaded) this.set({ loaded: true });
    } finally {
      this.poll();
    }
  }

  private poll(delay?: number) {
    clearTimeout(this.pollTimer);
    if (!this.userID) return;
    const serverActive = this.snapshot.server.some(
      (transfer) => transfer.state === "pending" || transfer.state === "running",
    );
    const localActive = this.snapshot.uploads.some((upload) => upload.phase === "running");
    const next = delay ?? (this.watchers > 0 || serverActive ? 1500 : localActive ? 4000 : 30000);
    this.pollTimer = setTimeout(() => void this.refresh(), next);
  }

  private schedule() {
    if (this.snapshot.holds.length || !this.userID) return;
    const active = this.snapshot.uploads.filter((upload) => upload.phase === "running").length;
    const ready = this.snapshot.uploads
      .filter((upload) => upload.phase === "queued")
      .slice(0, Math.max(0, parallelUploads - active));
    for (const upload of ready) this.start(upload);
  }

  private start(task: LocalUpload) {
    this.update(task.key, { phase: "running", error: undefined, hold: undefined });
    // A resumed upload counts only the bytes it sends from now on.
    this.sent.set(task.key, task.transferred);
    this.meters.items.set(task.key, new RateMeter());
    // A known upload resumes without an endpoint, so that tus-js-client
    // reports a lost upload instead of quietly starting a second one.
    const resuming = Boolean(task.transferId);
    const upload: Upload = new Upload(task.file, {
      endpoint: resuming ? null : uploadEndpoint,
      uploadUrl: resuming ? uploadURL(task.transferId!) : null,
      chunkSize,
      retryDelays,
      storeFingerprintForResuming: false,
      metadata: {
        filename: task.name,
        spaceId: task.target.spaceId,
        parentId: task.target.parentId,
        relativePath: task.directories.join("/"),
        conflict: task.conflict,
        lastModified: String(task.file.lastModified || 0),
        batch: task.batch,
      },
      // The token can change when the same account signs in again. Creating
      // the upload is the write a VNAS step may have to ask the member about
      // (a conflict answered with 替换); its parts follow from it.
      onBeforeRequest: async (request) => {
        for (const [name, value] of Object.entries(csrfHeader(request.getMethod()))) request.setHeader(name, value);
        if (request.getMethod() === "POST") await passWriteGate("POST", request.getURL());
      },
      onUploadUrlAvailable: () => {
        const id = upload.url ? decodeURIComponent(upload.url.slice(upload.url.lastIndexOf("/") + 1)) : "";
        if (id && id !== this.find(task.key)?.transferId) this.update(task.key, { transferId: id });
      },
      onProgress: (sent) => this.progress(task.key, sent),
      onShouldRetry: (error) => retryable(classifyUploadError(error)),
      onError: (error) => this.failed(task.key, error),
      onSuccess: () => this.succeeded(task.key),
    });
    this.running.set(task.key, upload);
    upload.start();
  }

  private progress(key: string, sent: number) {
    const current = this.find(key);
    if (!current || current.phase !== "running") return;
    this.count(key, current.batch, sent);
    const meter = this.meters.items.get(key);
    meter?.add(Date.now(), sent);
    this.update(key, { transferred: sent, speed: meter?.rate(Date.now()) ?? 0 }, false);
  }

  // count adds what an upload of this browser sent since it last reported.
  private count(key: string, batch: string | undefined, sent: number) {
    const delta = sent - (this.sent.get(key) ?? sent);
    this.sent.set(key, sent);
    if (delta <= 0) return;
    const now = Date.now();
    this.moved.upload += delta;
    this.meters.upload.add(now, this.moved.upload);
    if (batch) {
      const meter = this.batchMeter(batch, now - 1);
      const total = (this.moved.batches.get(batch) ?? 0) + delta;
      this.moved.batches.set(batch, total);
      meter.add(now, total);
    }
    this.measure(false);
  }

  // batchMeter returns a batch's meter, starting it from what the batch has
  // moved so far, so its first bytes count too.
  private batchMeter(batch: string, now: number) {
    let meter = this.meters.batches.get(batch);
    if (!meter) {
      meter = new RateMeter();
      meter.add(now, this.moved.batches.get(batch) ?? 0);
      this.meters.batches.set(batch, meter);
    }
    return meter;
  }

  // countServer adds what the NAS's other transfers moved since the previous
  // poll: downloads, and uploads another browser sends. The first poll only
  // notes where each transfer stood.
  private countServer(server: Transfer[]) {
    const owned = new Set(this.snapshot.uploads.flatMap((upload) => [upload.transferId, ...(upload.superseded ?? [])]));
    const now = Date.now();
    const first = !this.seen;
    const seen = new Map<string, number>();
    for (const transfer of server) {
      seen.set(transfer.id, transfer.transferredBytes);
      if (first || owned.has(transfer.id)) continue;
      const before = this.seen!.get(transfer.id);
      // A transfer that started again counts from its new beginning.
      const delta =
        before === undefined || transfer.transferredBytes < before
          ? transfer.transferredBytes
          : transfer.transferredBytes - before;
      if (delta > 0) this.moved[transfer.direction] += delta;
    }
    this.seen = seen;
    this.meters.upload.add(now, this.moved.upload);
    this.meters.download.add(now, this.moved.download);
  }

  // tick samples the meters of whatever still runs each second. A file
  // waiting for the NAS to take a part then counts as moving slowly, not as
  // a pause after which its next part would show as a burst of speed.
  private tick() {
    const now = Date.now();
    const running = [...this.snapshot.uploads, ...this.snapshot.external].filter(
      (upload) => upload.phase === "running",
    );
    if (running.length) this.meters.upload.add(now, this.moved.upload);
    for (const upload of this.snapshot.uploads)
      if (upload.phase === "running") this.meters.items.get(upload.key)?.add(now, upload.transferred);
    const batches = new Set(running.map((upload) => upload.batch));
    for (const [batch, meter] of this.meters.batches)
      if (batches.has(batch)) meter.add(now, this.moved.batches.get(batch) ?? 0);
    this.measure();
  }

  // measure publishes the speeds and keeps them fresh while bytes move, so a
  // stall shows as a falling speed rather than the last value.
  private measure(immediate = true) {
    const now = Date.now();
    const batches: Record<string, number> = {};
    const runningBatches = new Set(
      [...this.snapshot.uploads, ...this.snapshot.external]
        .filter((upload) => upload.phase === "running")
        .map((upload) => upload.batch),
    );
    for (const [batch, meter] of this.meters.batches) {
      const rate = meter.rate(now);
      if (rate > 0) batches[batch] = rate;
      else if (!runningBatches.has(batch)) this.meters.batches.delete(batch);
    }
    const rates: TransferRates = {
      upload: this.meters.upload.rate(now),
      download: this.meters.download.rate(now),
      batches,
    };
    const uploads = this.snapshot.uploads.map((upload) => {
      if (upload.phase !== "running") return upload;
      const speed = this.meters.items.get(upload.key)?.rate(now) ?? 0;
      return speed === upload.speed ? upload : { ...upload, speed };
    });
    const moving = rates.upload > 0 || rates.download > 0 || this.running.size > 0 || runningBatches.size > 0;
    const previous = this.snapshot.rates;
    const changed =
      Math.round(previous.upload) !== Math.round(rates.upload) ||
      Math.round(previous.download) !== Math.round(rates.download) ||
      Object.keys(previous.batches).length !== Object.keys(batches).length ||
      Object.entries(batches).some(([batch, rate]) => Math.round(previous.batches[batch] ?? 0) !== Math.round(rate)) ||
      uploads.some((upload, index) => upload !== this.snapshot.uploads[index]);
    if (changed) this.set({ rates, uploads }, immediate);
    if (moving && !this.rateTimer) this.rateTimer = setInterval(() => this.tick(), 1000);
    if (!moving && this.rateTimer) {
      clearInterval(this.rateTimer);
      this.rateTimer = undefined;
    }
  }

  private succeeded(key: string) {
    this.running.delete(key);
    const upload = this.find(key);
    if (!upload) return;
    // The last part may finish without a progress report.
    this.count(key, upload.batch, upload.size);
    this.meters.items.delete(key);
    this.update(key, { phase: "completed", transferred: upload.size, speed: 0, finishedAt: Date.now() });
    for (const listener of this.uploadedListeners) listener(upload.target);
    if (upload.transferId) {
      void getTransfer(upload.transferId)
        .then((transfer) => this.update(key, { savedName: transfer.savedName, replaced: transfer.replaced }))
        .catch(() => undefined);
    }
    this.schedule();
    this.poll(1000);
  }

  private failed(key: string, error: Error) {
    this.running.delete(key);
    const upload = this.find(key);
    // A paused or canceled upload stopped on purpose.
    if (!upload || upload.phase !== "running") return;
    const failure = classifyUploadError(error);
    const reason: TransferFailure = { code: failure.code, message: failure.message };
    switch (failure.kind) {
      case "network":
        this.hold("offline", key);
        break;
      case "session":
        this.hold("session", key);
        sessionEnded();
        break;
      case "volume":
        this.hold("volume", key);
        break;
      case "space":
        this.hold("space", key);
        break;
      case "conflict":
        this.answer(upload, this.batch(upload.batch)?.choice);
        break;
      case "lost":
        // The NAS restarted or forgot the upload: start it again from the
        // beginning, once.
        if (!upload.restarted) {
          const superseded = upload.transferId ? [...(upload.superseded ?? []), upload.transferId] : upload.superseded;
          this.update(key, { phase: "queued", transferId: undefined, transferred: 0, restarted: true, superseded });
          // The NAS's record of the lost upload would only repeat this one.
          if (upload.transferId) void removeTransfer(upload.transferId).catch(() => undefined);
        } else this.update(key, { phase: "failed", error: { code: "upload_lost", message: reason.message }, speed: 0 });
        break;
      case "missing":
        // The target folder is gone for every upload still bound for it.
        this.set({
          uploads: this.snapshot.uploads.map((item) =>
            item.key === key ||
            (item.phase === "queued" && sameTarget(item.target, upload.target) && item.directories.length === 0)
              ? { ...item, phase: "failed", error: { code: "not_found", message: reason.message }, speed: 0 }
              : item,
          ),
        });
        break;
      case "canceled":
        this.update(key, { phase: "canceled", speed: 0, finishedAt: Date.now() });
        break;
      case "busy":
        this.update(key, { phase: "queued", speed: 0 });
        setTimeout(() => this.schedule(), 5000);
        return;
      default:
        this.update(key, { phase: "failed", error: reason, speed: 0 });
    }
    this.schedule();
  }

  // hold pauses the queue for a cause every upload shares and retries when
  // it may have cleared; only a new sign-in clears an expired session.
  private hold(hold: Hold, key: string) {
    for (const [other, upload] of this.running) {
      if (other === key) continue;
      this.running.delete(other);
      void upload.abort(false);
    }
    const holds = this.snapshot.holds.includes(hold) ? this.snapshot.holds : [...this.snapshot.holds, hold];
    this.set({
      holds,
      uploads: this.snapshot.uploads.map((upload) =>
        upload.key === key || upload.phase === "running" ? { ...upload, phase: "waiting", hold, speed: 0 } : upload,
      ),
    });
    if (hold === "session" || this.holdTimers.has(hold)) return;
    const every = { offline: 5000, volume: 30000, space: 60000 }[hold];
    this.holdTimers.set(
      hold,
      setInterval(() => void this.probe(hold), every),
    );
  }

  private async probe(hold: Hold) {
    if (hold !== "offline") {
      this.release(hold);
      return;
    }
    try {
      const response = await fetch("/api/v1/session", {
        credentials: "same-origin",
        headers: { Accept: "application/json" },
      });
      if (response.status === 401) {
        this.release("offline");
        this.hold("session", "");
        sessionEnded();
        return;
      }
      this.release("offline");
    } catch {
      // Still unreachable.
    }
  }

  private answer(upload: LocalUpload, choice?: ConflictChoice) {
    if (!choice) this.update(upload.key, { phase: "conflict", speed: 0 });
    else if (choice === "skip") this.update(upload.key, { phase: "skipped", speed: 0, finishedAt: Date.now() });
    else this.update(upload.key, { phase: "queued", conflict: choice, transferId: undefined, transferred: 0 });
  }

  private stop(key: string) {
    const upload = this.running.get(key);
    this.running.delete(key);
    if (upload) void upload.abort(false);
  }

  private listen() {
    if (this.listening || typeof window === "undefined") return;
    this.listening = true;
    window.addEventListener("online", () => this.release("offline"));
    // Closing or reloading the page stops this browser's uploads.
    window.addEventListener("beforeunload", (event) => {
      if (this.unfinished() > 0) {
        event.preventDefault();
        event.returnValue = "";
      }
    });
  }

  private merge(transfer: Transfer) {
    const exists = this.snapshot.server.some((item) => item.id === transfer.id);
    this.set({
      server: exists
        ? this.snapshot.server.map((item) => (item.id === transfer.id ? transfer : item))
        : [transfer, ...this.snapshot.server],
    });
  }

  private pruneBatches() {
    const used = new Set(this.snapshot.uploads.map((upload) => upload.batch));
    this.set({ batches: this.snapshot.batches.filter((batch) => used.has(batch.id)) });
  }

  private find(key: string) {
    return this.snapshot.uploads.find((upload) => upload.key === key);
  }
  private batch(id: string) {
    return this.snapshot.batches.find((batch) => batch.id === id);
  }
  private batchUploads(batch: string) {
    return this.snapshot.uploads.filter((upload) => upload.batch === batch);
  }

  private update(key: string, change: Partial<LocalUpload>, immediate = true) {
    this.set(
      { uploads: this.snapshot.uploads.map((upload) => (upload.key === key ? { ...upload, ...change } : upload)) },
      immediate,
    );
  }

  // set replaces the snapshot; progress is announced at most every 150 ms.
  private set(change: Partial<TransferSnapshot>, immediate = true) {
    this.snapshot = { ...this.snapshot, ...change };
    if (immediate) {
      clearTimeout(this.emitTimer);
      this.emitTimer = undefined;
      for (const listener of this.listeners) listener();
    } else if (!this.emitTimer) {
      this.emitTimer = setTimeout(() => {
        this.emitTimer = undefined;
        for (const listener of this.listeners) listener();
      }, 150);
    }
  }
}

function sameTarget(left: UploadTarget, right: UploadTarget) {
  return left.spaceId === right.spaceId && left.parentId === right.parentId;
}

// fetchDownload lets the browser save a download as it saves any file.
function fetchDownload(transfer: Transfer) {
  if (!transfer.contentUrl) return;
  const link = document.createElement("a");
  link.href = transfer.contentUrl;
  link.download = transfer.name;
  link.hidden = true;
  document.body.appendChild(link);
  link.click();
  link.remove();
}

export const transferStore = new TransferStore();

export function useTransfers() {
  return useSyncExternalStore(transferStore.subscribe, transferStore.getSnapshot);
}
