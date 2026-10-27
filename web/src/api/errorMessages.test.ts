import { describe, expect, it } from "vitest";
import { APIError } from "./client";
import { errorCodeText, errorText, type ErrorDomain } from "./errorMessages";
import { messageOf as fileError } from "../files/model";
import { messageOf as photoError } from "../photos/model";
import { errorText as shareError } from "../shares/format";
import { describeError as appError } from "../appcenter/model";

describe("product error copy", () => {
  it("uses the same volume/network messages in every domain", () => {
    const domains: ErrorDomain[] = [
      "imageSearch",
      "photos",
      "shares",
      "transfers",
      "backup",
      "trash",
      "media",
      "apps",
      "music",
      "storage",
    ];
    for (const code of ["volume_unavailable", "network_error"]) {
      for (const domain of domains)
        expect(errorText(new APIError(503, code, "internal detail"), domain)).toBe(errorCodeText(code));
    }
  });
  it("never falls back to server or browser prose, including unknown codes", () => {
    for (const render of [errorText, fileError, photoError, shareError]) {
      expect(render(new APIError(400, "unknown", "secret server detail"))).toBe("请求失败，请稍后重试");
      expect(render(new APIError(503, "unknown", "secret server detail"))).toBe("服务暂时不可用，请稍后重试");
      expect(render(new Error("secret server detail"))).toBe("请求失败，请稍后重试");
      expect(render(new TypeError("Failed to fetch"))).toBe(errorCodeText("network_error"));
    }
    expect(errorText(new APIError(400, "toString"))).toBe("请求失败，请稍后重试");
  });
  it("keeps known domain guidance and treats cancellation as silent", () => {
    expect(photoError(new APIError(404, "not_found"))).toBe("照片或相册已不存在");
    expect(errorText(new APIError(413, "too_large", "private"), "imageSearch")).toBe("图片太大，换一张小一些的试试");
    expect(shareError(new APIError(404, "share_not_found"))).toContain("分享");
    expect(errorText(new DOMException("aborted", "AbortError"))).toBe("");
    expect(appError(new APIError(409, "port_in_use", "port in use: 8080/tcp"))).toBe("端口已被占用（8080/tcp）");
    expect(appError(new APIError(409, "port_in_use", "unexpected private diagnostic"))).toBe("端口已被占用");
  });
});
