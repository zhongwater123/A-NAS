import { HttpResponse } from "msw";
import { describe, expect, it } from "vitest";

import { APIError } from "../api/client";
import { errorText } from "../api/errorMessages";
import { http, server } from "../test/server";
import { assistantStatus, failureText, getMemory, listTasks, sessionMessages } from "./api";

describe("assistant API error messages", () => {
  it("keeps the network explanation after the shared client normalizes fetch failures", async () => {
    // The connection drops on every request.
    server.use(
      http.get("/api/v1/assistant/status", () => HttpResponse.error()),
      http.get("/api/v1/assistant/sessions/{sessionId}/messages", () => HttpResponse.error()),
      http.get("/api/v1/assistant/memory", () => HttpResponse.error()),
      http.get("/api/v1/assistant/tasks", () => HttpResponse.error()),
    );
    for (const request of [assistantStatus, () => sessionMessages("session:1"), getMemory, listTasks]) {
      const error = await request().catch((error: unknown) => error);
      expect(error).toBeInstanceOf(APIError);
      expect(error).toMatchObject({ status: 0, code: "network_error" });
      expect(failureText(error)).toBe("网络连接中断，请检查网络后重试");
    }
  });

  it("preserves assistant guidance while sharing unknown errors and cancellation", () => {
    expect(failureText(new APIError(503, "assistant_busy"))).toBe("VNAS 正忙，请稍后再试");
    expect(failureText(new APIError(404, "session_not_found"))).toBe("这个对话已经不存在");
    for (const error of [
      new APIError(503, "unknown", "private detail"),
      new TypeError("Failed to fetch"),
      new DOMException("aborted", "AbortError"),
    ]) {
      expect(failureText(error)).toBe(errorText(error));
    }
  });
});
