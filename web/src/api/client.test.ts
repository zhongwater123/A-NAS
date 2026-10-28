import { HttpResponse } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Session } from "../api.generated";
import { readApps } from "../appcenter/api";
import { readHostState } from "../desktop/api";
import { readContainers } from "../docker/api";
import { deleteFile, listSpaces } from "../files/api";
import { apiError, http, pathOf, server } from "../test/server";
import { APIError, csrfHeader, onSessionEnded, setSession, uploadForm } from "./client";

const session: Session = {
  csrfToken: "csrf-test",
  expiresAt: "2026-10-23T22:00:00Z",
  user: {
    id: "user:owner",
    username: "owner",
    role: "admin",
    status: "active",
    createdAt: "2026-10-23T00:00:00Z",
    mustChangePassword: false,
  },
};

afterEach(() => {
  setSession();
  vi.unstubAllGlobals();
});

describe("csrfHeader", () => {
  it("carries the session's token on every method but GET and HEAD", () => {
    setSession(session);
    for (const method of ["POST", "PUT", "PATCH", "DELETE", "options"])
      expect(csrfHeader(method)).toEqual({ "X-CSRF-Token": "csrf-test" });
    for (const method of ["GET", "head"]) expect(csrfHeader(method)).toEqual({});
  });

  it("has nothing to send without a session", () => {
    expect(csrfHeader("POST")).toEqual({});
  });
});

describe("client", () => {
  it("normalizes network failures across domain clients without swallowing cancellation", async () => {
    server.use(
      http.get("/api/v1/spaces", () => HttpResponse.error()),
      http.get("/api/v1/apps", () => HttpResponse.error()),
      http.get("/api/v1/containers", () => HttpResponse.error()),
      http.get("/api/v1/host-state", () => HttpResponse.error()),
    );
    for (const request of [listSpaces, readApps, readContainers, readHostState]) {
      await expect(request()).rejects.toMatchObject({ status: 0, code: "network_error" });
    }

    // A cancelled request rejects with its AbortError, not as a network failure. A
    // real abort cannot show this here: undici rejects with Node's DOMException,
    // not the page's (jsdom's), so fetch itself rejects as a browser's would.
    const abort = new DOMException("aborted", "AbortError");
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(abort));
    for (const request of [listSpaces, readApps, readContainers, readHostState]) {
      await expect(request()).rejects.toBe(abort);
    }
  });

  it("retains status and product code for app, container and host failures", async () => {
    const busy = apiError("service_busy", "private server detail");
    server.use(
      http.get("/api/v1/apps", ({ response }) => response(503).json(busy)),
      http.get("/api/v1/containers", ({ response }) => response(503).json(busy)),
      http.get("/api/v1/host-state", ({ response }) => response(503).json(busy)),
    );
    for (const request of [readApps, readContainers, readHostState]) {
      const error = await request().catch((error: unknown) => error);
      expect(error).toBeInstanceOf(APIError);
      expect(error).toMatchObject({ status: 503, code: "service_busy", message: "private server detail" });
    }
  });

  it("classifies an unparseable successful response", async () => {
    server.use(
      http.get("/api/v1/spaces", ({ response }) =>
        // A broken body, so not one the contract can type.
        response.untyped(new HttpResponse("not json", { headers: { "Content-Type": "application/json" } })),
      ),
    );
    await expect(listSpaces()).rejects.toMatchObject({ code: "malformed_response" });
  });

  it("sends the CSRF token on writes, whatever the caller passed", async () => {
    setSession(session);
    const sent: Array<{ method: string; path: string; csrf: string | null }> = [];
    const record = (request: Request) =>
      sent.push({ method: request.method, path: pathOf(request), csrf: request.headers.get("X-CSRF-Token") });
    server.use(
      http.get("/api/v1/spaces", ({ request, response }) => {
        record(request);
        return response(200).json({ items: [] });
      }),
      http.delete("/api/v1/files/{fileId}", ({ request, response }) => {
        record(request);
        return response(200).json({
          id: "trash:1",
          entryId: "file:1",
          spaceId: "space:1",
          name: "a.txt",
          kind: "file",
          sizeBytes: 1,
          originalPath: "",
          originalGone: false,
          deletedBy: "owner",
          deletedAt: "2026-10-23T00:00:00Z",
          purgeAt: "2026-11-22T00:00:00Z",
        });
      }),
    );
    await listSpaces();
    await deleteFile("file:1");
    expect(sent).toEqual([
      { method: "GET", path: "/api/v1/spaces", csrf: null },
      { method: "DELETE", path: "/api/v1/files/file%3A1", csrf: "csrf-test" },
    ]);
  });

  it("turns an error body into an APIError, and a 401 ends the session", async () => {
    const ended = vi.fn();
    const stop = onSessionEnded(ended);
    server.use(
      http.get("/api/v1/spaces", ({ response }) =>
        // Every signed-in route answers 401 once the session ends; the contract lists it for few of them.
        response.untyped(
          HttpResponse.json(apiError("authentication_required", "authentication is required"), { status: 401 }),
        ),
      ),
    );
    const failure = await listSpaces().catch((error: unknown) => error);
    stop();
    expect(failure).toBeInstanceOf(APIError);
    expect(failure).toMatchObject({ status: 401, code: "authentication_required" });
    expect(ended).toHaveBeenCalledTimes(1);
  });
});

describe("multipart uploads", () => {
  class Upload {
    static last: Upload;
    upload = { onprogress: undefined as unknown };
    status = 200;
    responseText = "{}";
    onload = () => {};
    onerror = () => {};
    ontimeout = () => {};
    onabort = () => {};
    constructor() {
      Upload.last = this;
    }
    open() {}
    setRequestHeader() {}
    send() {}
    abort() {
      this.onabort();
    }
  }
  const begin = async (signal?: AbortSignal) => {
    const pending = uploadForm("/api/v1/photos/import", new FormData(), vi.fn(), signal);
    const result = pending.catch((error: unknown) => error);
    await Promise.resolve();
    return { result, xhr: Upload.last };
  };

  it("shares API errors and the session-ended notification", async () => {
    vi.stubGlobal("XMLHttpRequest", Upload);
    const ended = vi.fn();
    const stop = onSessionEnded(ended);
    try {
      const { result, xhr } = await begin();
      xhr.status = 401;
      xhr.responseText = JSON.stringify({ error: { code: "authentication_required", message: "private" } });
      xhr.onload();
      expect(await result).toBeInstanceOf(APIError);
      expect(await result).toMatchObject({ status: 401, code: "authentication_required" });
      expect(ended).toHaveBeenCalledOnce();
      const network = await begin();
      network.xhr.onerror();
      expect(await network.result).toMatchObject({ status: 0, code: "network_error" });
      const malformed = await begin();
      malformed.xhr.responseText = "not json";
      malformed.xhr.onload();
      expect(await malformed.result).toMatchObject({ status: 200, code: "malformed_response" });
    } finally {
      stop();
    }
  });

  it("does not start cancelled uploads and preserves cancellation while sending", async () => {
    const factory = vi.fn(function () {
      return new Upload();
    });
    vi.stubGlobal("XMLHttpRequest", factory);
    const controller = new AbortController();
    controller.abort();
    await expect(uploadForm("/upload", new FormData(), vi.fn(), controller.signal)).rejects.toMatchObject({
      name: "AbortError",
    });
    expect(factory).not.toHaveBeenCalled();
    const active = new AbortController();
    const { result } = await begin(active.signal);
    active.abort();
    expect(await result).toMatchObject({ name: "AbortError" });
  });
});
