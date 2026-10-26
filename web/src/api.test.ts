import { afterEach, describe, expect, it, vi } from "vitest";

import { APIError, csrfHeader, deleteFile, listSpaces, onSessionEnded, type Session, setSession } from "./api";
import { apiError, json, stubAPI } from "./test/api";

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
  it("sends the CSRF token on writes, whatever the caller passed", async () => {
    setSession(session);
    const answer = stubAPI(
      vi.fn(async (path: string, _init?: RequestInit) =>
        path === "/api/v1/spaces"
          ? json({ items: [] })
          : json({
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
            }),
      ),
    );
    await listSpaces();
    await deleteFile("file:1");
    const [[readPath, read], [writePath, write]] = answer.mock.calls;
    expect(readPath).toBe("/api/v1/spaces");
    expect(new Headers(read?.headers).has("X-CSRF-Token")).toBe(false);
    expect(writePath).toBe("/api/v1/files/file%3A1");
    expect(write?.method).toBe("DELETE");
    expect(new Headers(write?.headers).get("X-CSRF-Token")).toBe("csrf-test");
  });

  it("turns an error body into an APIError, and a 401 ends the session", async () => {
    const ended = vi.fn();
    const stop = onSessionEnded(ended);
    stubAPI(vi.fn(async () => apiError(401, "authentication_required", "authentication is required")));
    const failure = await listSpaces().catch((error: unknown) => error);
    stop();
    expect(failure).toBeInstanceOf(APIError);
    expect(failure).toMatchObject({ status: 401, code: "authentication_required" });
    expect(ended).toHaveBeenCalledTimes(1);
  });
});
