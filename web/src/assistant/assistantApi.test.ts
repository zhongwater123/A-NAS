import { afterEach, describe, expect, it, vi } from "vitest";
import { APIError } from "../api";
import { errorText } from "../errorMessages";
import { assistantStatus, failureText, getMemory, listTasks, sessionMessages } from "./assistantApi";

afterEach(() => vi.unstubAllGlobals());

describe("assistant API error messages", () => {
  it("keeps the network explanation after the shared client normalizes fetch failures", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")));
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
