import { APIError } from "../api";
import { messageOf } from "./Browse";

// A browser plays a converted stream progressively and reads only about two
// megabytes ahead of the picture, because the stream has no length and
// cannot be fetched by range: a skip forward almost always restarted it, and
// a short hiccup stalled playback. StreamFeed reads the stream itself into
// Media Source Extensions instead, so the player decides how far ahead to
// buffer. When enough is buffered it stops reading; the connection then
// waits, and so does FFmpeg on the device.

export interface FeedOptions {
  // Read until this many seconds are buffered ahead of the picture, and
  // start again once fewer than resume are.
  ahead?: number;
  resume?: number;
  // Keep this many seconds behind the picture, for skips back.
  behind?: number;
  // Seconds the stream lasts, when known, so the element knows its length.
  duration?: number;
  onError: (message: string) => void;
  // The browser cannot take the stream this way; play it directly instead.
  onFallback: () => void;
}

export function canFeed(mime?: string): mime is string {
  if (!mime || typeof MediaSource === "undefined" || typeof URL.createObjectURL !== "function") return false;
  try { return MediaSource.isTypeSupported(mime); } catch { return false; }
}

const sleep = (milliseconds: number) => new Promise((resolve) => setTimeout(resolve, milliseconds));

function settled(buffer: SourceBuffer): Promise<void> {
  if (!buffer.updating) return Promise.resolve();
  return new Promise((resolve, reject) => {
    const done = () => { buffer.removeEventListener("error", failed); resolve(); };
    const failed = () => { buffer.removeEventListener("updateend", done); reject(new Error("source buffer error")); };
    buffer.addEventListener("updateend", done, { once: true });
    buffer.addEventListener("error", failed, { once: true });
  });
}

// aheadOf is how many seconds play on from at without a gap.
export function aheadOf(ranges: TimeRanges, at: number): number {
  for (let index = 0; index < ranges.length; index++) {
    if (ranges.start(index) - 0.5 <= at && at < ranges.end(index)) return ranges.end(index) - at;
  }
  return 0;
}

export class StreamFeed {
  readonly url: string;
  private readonly source: MediaSource;
  private readonly controller = new AbortController();
  private closed = false;
  private appended = false;
  private readonly ahead: number;
  private readonly resume: number;
  private readonly behind: number;

  constructor(private readonly stream: string, private readonly mime: string, private readonly media: () => HTMLVideoElement | null, private readonly options: FeedOptions) {
    this.ahead = options.ahead ?? 30;
    this.resume = options.resume ?? 15;
    this.behind = options.behind ?? 90;
    this.source = new MediaSource();
    this.url = URL.createObjectURL(this.source);
    this.source.addEventListener("sourceopen", () => void this.run(), { once: true });
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    this.controller.abort();
    URL.revokeObjectURL(this.url);
  }

  private async run() {
    if (this.closed) return;
    let buffer: SourceBuffer;
    try {
      buffer = this.source.addSourceBuffer(this.mime);
      if (this.options.duration && this.options.duration > 0) this.source.duration = this.options.duration;
    } catch {
      if (!this.closed) this.options.onFallback();
      return;
    }
    let response: Response;
    try {
      response = await fetch(this.stream, { credentials: "same-origin", signal: this.controller.signal, headers: { Accept: "video/mp4" } });
    } catch {
      if (!this.closed) this.options.onError("网络连接中断，请重试");
      return;
    }
    if (!response.ok || !response.body) {
      const body = await response.json().catch(() => undefined) as { error?: { code?: string; message?: string } } | undefined;
      if (!this.closed) this.options.onError(messageOf(new APIError(response.status, body?.error?.code ?? "request_failed", body?.error?.message ?? "转码失败，请稍后重试")));
      return;
    }
    const reader = response.body.getReader();
    try {
      for (;;) {
        await this.room(buffer);
        if (this.closed) return;
        const { value, done } = await reader.read();
        if (done) break;
        await this.append(buffer, value);
      }
      await settled(buffer);
      if (!this.closed && this.source.readyState === "open") this.source.endOfStream();
    } catch {
      if (this.closed) return;
      // Nothing played yet: the browser does not take this stream here.
      if (!this.appended) this.options.onFallback();
      else this.options.onError("播放中断，请重试");
    } finally {
      if (this.closed) void reader.cancel().catch(() => undefined);
    }
  }

  // room waits while enough is buffered ahead, and drops what is long
  // played meanwhile.
  private async room(buffer: SourceBuffer) {
    const video = this.media();
    if (!video || aheadOf(buffer.buffered, video.currentTime) < this.ahead) return;
    while (!this.closed) {
      await this.trim(buffer, this.behind);
      const current = this.media();
      if (!current || aheadOf(buffer.buffered, current.currentTime) < this.resume) return;
      await sleep(400);
    }
  }

  private async append(buffer: SourceBuffer, chunk: Uint8Array) {
    for (;;) {
      try {
        await settled(buffer);
        buffer.appendBuffer(chunk as BufferSource);
        await settled(buffer);
        this.appended = true;
        return;
      } catch (caught) {
        // The browser's buffer is full, which a high-bitrate stream reaches
        // before thirty seconds: like a full read-ahead, wait for playback
        // to use some, however long it is paused, dropping what has played.
        if (!(caught instanceof DOMException && caught.name === "QuotaExceededError") || this.closed) throw caught;
        await this.trim(buffer, 10);
        await sleep(500);
      }
    }
  }

  private async trim(buffer: SourceBuffer, keep: number) {
    const video = this.media();
    if (!video || !buffer.buffered.length || buffer.updating) return;
    const end = video.currentTime - keep;
    if (end <= buffer.buffered.start(0) + 1) return;
    try {
      buffer.remove(buffer.buffered.start(0), end);
      await settled(buffer);
    } catch { /* trimming is housekeeping */ }
  }
}
