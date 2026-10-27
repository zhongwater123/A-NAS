import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { aheadOf, StreamFeed } from "./streamFeed";
import { onSessionEnded } from "../api";

// Each chunk of the fake stream holds five seconds of video.
const chunkSeconds = 5;

function ranges(list: [number, number][]): TimeRanges {
  return {
    length: list.length,
    start: (index: number) => list[index][0],
    end: (index: number) => list[index][1],
  } as TimeRanges;
}

class FakeBuffer extends EventTarget {
  updating = false;
  end = 0;
  start = 0;
  fail?: Error;
  // quota is how many seconds the browser holds at most.
  quota = Infinity;
  get buffered() {
    return ranges(this.end > this.start ? [[this.start, this.end]] : []);
  }
  appendBuffer() {
    if (this.fail) throw this.fail;
    if (this.end - this.start >= this.quota) throw new DOMException("full", "QuotaExceededError");
    this.updating = true;
    queueMicrotask(() => {
      this.end += chunkSeconds;
      this.updating = false;
      this.dispatchEvent(new Event("updateend"));
    });
  }
  remove(_: number, end: number) {
    this.updating = true;
    queueMicrotask(() => {
      this.start = end;
      this.updating = false;
      this.dispatchEvent(new Event("updateend"));
    });
  }
}

class FakeSource extends EventTarget {
  static last: FakeSource;
  static failAdd = false;
  readyState = "closed";
  duration = NaN;
  ended = false;
  buffer = new FakeBuffer();
  constructor() {
    super();
    FakeSource.last = this;
  }
  static isTypeSupported() {
    return true;
  }
  addSourceBuffer() {
    if (FakeSource.failAdd) throw new DOMException("unsupported", "NotSupportedError");
    return this.buffer;
  }
  endOfStream() {
    this.ended = true;
    this.readyState = "ended";
  }
  open() {
    this.readyState = "open";
    this.dispatchEvent(new Event("sourceopen"));
  }
}

function stream(total: number) {
  let pulled = 0;
  const body = new ReadableStream<Uint8Array>(
    {
      pull(controller) {
        pulled++;
        controller.enqueue(new Uint8Array([pulled]));
        if (pulled === total) controller.close();
      },
    },
    { highWaterMark: 0 },
  );
  return { body, pulled: () => pulled };
}

const flush = async (times = 20) => {
  for (let index = 0; index < times; index++) await Promise.resolve();
};

beforeEach(() => {
  FakeSource.failAdd = false;
  vi.stubGlobal("MediaSource", FakeSource);
  vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:feed");
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("StreamFeed", () => {
  it("ends an expired session and never shows server prose on a failed stream", async () => {
    const ended = vi.fn();
    const stop = onSessionEnded(ended);
    const onError = vi.fn();
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(JSON.stringify({ error: { code: "unknown", message: "private server detail" } }), {
            status: 401,
          }),
      ),
    );
    const feed = new StreamFeed("/stream", "video/mp4", () => null, { onError, onFallback: vi.fn() });
    try {
      FakeSource.last.open();
      await vi.waitFor(() => expect(onError).toHaveBeenCalledWith("登录已失效或账号不可用，请重新登录"));
      expect(ended).toHaveBeenCalledOnce();
    } finally {
      feed.close();
      stop();
    }
  });
  it("reads until enough is buffered ahead, then waits for playback", async () => {
    vi.useFakeTimers();
    const video = { currentTime: 0 } as HTMLVideoElement;
    const source = stream(100);
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, status: 200, body: source.body })),
    );
    const feed = new StreamFeed("/stream", 'video/mp4; codecs="avc1.640033"', () => video, {
      ahead: 30,
      resume: 15,
      onError: vi.fn(),
      onFallback: vi.fn(),
    });
    expect(feed.url).toBe("blob:feed");
    FakeSource.last.open();
    await vi.advanceTimersByTimeAsync(2000);
    // Six chunks make thirty seconds; the feed stops reading there.
    expect(FakeSource.last.buffer.end).toBe(30);
    const read = source.pulled();
    await vi.advanceTimersByTimeAsync(5000);
    expect(source.pulled()).toBe(read);

    // Playback uses some; below fifteen seconds ahead it reads again, up
    // to thirty seconds ahead of the picture.
    video.currentTime = 16;
    await vi.advanceTimersByTimeAsync(2000);
    expect(FakeSource.last.buffer.end).toBe(50);
    feed.close();
  });

  it("waits while the browser's buffer is full, however long playback is paused", async () => {
    vi.useFakeTimers();
    const video = { currentTime: 0 } as HTMLVideoElement;
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, status: 200, body: stream(100).body })),
    );
    const onError = vi.fn();
    const feed = new StreamFeed("/stream", "video/mp4", () => video, { ahead: 30, onError, onFallback: vi.fn() });
    FakeSource.last.buffer.quota = 20;
    FakeSource.last.open();
    await vi.advanceTimersByTimeAsync(40_000);
    expect(FakeSource.last.buffer.end).toBe(20);
    expect(onError).not.toHaveBeenCalled();
    // Playing on frees what is ten seconds behind, and the feed reads again.
    video.currentTime = 15;
    await vi.advanceTimersByTimeAsync(2000);
    expect(FakeSource.last.buffer.end).toBeGreaterThan(20);
    expect(onError).not.toHaveBeenCalled();
    feed.close();
  });

  it("ends the stream when the device has sent all of it", async () => {
    const video = { currentTime: 0 } as HTMLVideoElement;
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, status: 200, body: stream(3).body })),
    );
    new StreamFeed("/stream", "video/mp4", () => video, { onError: vi.fn(), onFallback: vi.fn() });
    FakeSource.last.open();
    await vi.waitFor(() => expect(FakeSource.last.ended).toBe(true));
    expect(FakeSource.last.buffer.end).toBe(15);
  });

  it("falls back when the browser does not take the stream, and reports a busy device", async () => {
    FakeSource.failAdd = true;
    const onFallback = vi.fn();
    new StreamFeed("/stream", "video/mp4", () => null, { onError: vi.fn(), onFallback });
    FakeSource.last.open();
    await flush();
    expect(onFallback).toHaveBeenCalledTimes(1);

    FakeSource.failAdd = false;
    const onError = vi.fn();
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: false,
        status: 503,
        body: null,
        json: async () => ({ error: { code: "media_busy", message: "busy" } }),
      })),
    );
    new StreamFeed("/stream", "video/mp4", () => null, { onError, onFallback: vi.fn() });
    FakeSource.last.open();
    await vi.waitFor(() => expect(onError).toHaveBeenCalledWith("设备正在为其他人转码，请稍后再试。"));
  });

  it("measures what plays on without a gap", () => {
    const held = ranges([
      [0, 20],
      [40, 60],
    ]);
    expect(aheadOf(held, 5)).toBe(15);
    expect(aheadOf(held, 30)).toBe(0);
    expect(aheadOf(held, 39.8)).toBeCloseTo(20.2);
  });
});
