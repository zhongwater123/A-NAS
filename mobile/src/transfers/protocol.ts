import type { HttpStack, HttpRequest, HttpResponse } from "tus-js-client";
import { NasClient, NasError } from "../nas/client";

/** tus owns retries/offset negotiation; this adapter supplies Expo's native fetch. */
export class NasTusStack implements HttpStack {
  constructor(private nas: NasClient) {}
  getName() {
    return "A-NAS Expo fetch";
  }
  createRequest(method: string, url: string): HttpRequest {
    const target = this.nas.url(url),
      headers = new Headers(),
      aborter = new AbortController();
    return {
      getMethod: () => method,
      getURL: () => target,
      setHeader: (key, value) => headers.set(key, value),
      getHeader: (key) => headers.get(key) ?? undefined,
      setProgressHandler: () => {}, // Show acknowledged chunks, not optimistic socket bytes.
      getUnderlyingObject: () => aborter,
      abort: async () => {
        aborter.abort();
      },
      send: async (body?: BodyInit): Promise<HttpResponse> => {
        const response = await this.nas.request(target, {
          method,
          headers,
          body,
          signal: AbortSignal.any([
            aborter.signal,
            AbortSignal.timeout(60_000),
          ]),
        });
        const text = method === "HEAD" ? "" : await response.text();
        return {
          getStatus: () => response.status,
          getHeader: (name) => response.headers.get(name) ?? undefined,
          getBody: () => text,
          getUnderlyingObject: () => response,
        };
      },
    };
  }
}

export interface DownloadSink {
  size(): number;
  open(append: boolean): void;
  write(bytes: Uint8Array): Promise<void>;
  close(): void;
}
/** Stream to disk with bounded memory. Resume metadata never contains credentials. */
export async function downloadFile(
  nas: NasClient,
  options: {
    url: string;
    total: number;
    etag?: string;
    signal: AbortSignal;
    sink: DownloadSink;
    progress(bytes: number): void;
    version(etag: string): void;
  },
) {
  const { sink } = options;
  let offset = sink.size();
  if (offset > options.total || !options.etag) offset = 0;
  const headers: Record<string, string> = {};
  if (offset > 0) {
    headers.Range = `bytes=${offset}-`;
    headers["If-Range"] = options.etag!;
  }
  const timeout = new AbortController();
  let timer = setTimeout(() => timeout.abort(), 45_000);
  const signal = AbortSignal.any([options.signal, timeout.signal]);
  let opened = false;
  try {
    // A previous process can have written the last byte before persisting completion.
    // Re-fetch to validate the content instead of treating an unverified partial as done.
    if (offset === options.total) {
      offset = 0;
      delete headers.Range;
      delete headers["If-Range"];
    }
    const response = await nas.request(options.url, { headers, signal });
    if (!response.ok)
      throw new NasError(`http_${response.status}`, response.status);
    if (response.status === 206) {
      if (
        !offset ||
        !response.headers.get("content-range")?.startsWith(`bytes ${offset}-`)
      )
        throw new NasError("invalid_download_range");
      if (response.headers.get("etag") !== options.etag)
        throw new NasError("file_changed");
    } else if (response.status === 200) offset = 0;
    else throw new NasError("invalid_download_response");
    const etag = response.headers.get("etag");
    if (etag) options.version(etag);
    if (!response.body) throw new NasError("missing_download_body");
    sink.open(offset > 0);
    opened = true;
    const reader = response.body.getReader();
    try {
      while (true) {
        const part = await reader.read();
        if (signal.aborted) throw new Error("Download interrupted");
        if (part.done) break;
        clearTimeout(timer);
        timer = setTimeout(() => timeout.abort(), 45_000);
        if (offset + part.value.byteLength > options.total)
          throw new NasError("file_changed");
        await sink.write(part.value);
        offset += part.value.byteLength;
        options.progress(offset);
      }
    } finally {
      await reader.cancel().catch(() => {});
    }
    if (offset !== options.total) throw new NasError("incomplete_download");
  } finally {
    clearTimeout(timer);
    if (opened) sink.close();
  }
}
