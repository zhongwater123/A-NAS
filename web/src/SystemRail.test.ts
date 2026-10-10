import { describe, expect, it } from "vitest";

import { describeSession } from "./SystemRail";

describe("describeSession", () => {
  const morning = new Date(2026, 9, 10, 9, 0);

  it("says a local console session lasts until sign-out", () => {
    expect(describeSession("9999-12-31T23:59:59Z", morning)).toEqual({ localConsole: true, text: "本机屏幕 · 退出前保持登录" });
  });

  it("gives the expiry time of a browser session", () => {
    expect(describeSession(new Date(2026, 9, 10, 21, 0).toISOString(), morning).text).toBe("浏览器登录 · 有效至 今天 21:00");
    expect(describeSession(new Date(2026, 9, 11, 4, 51).toISOString(), morning).text).toBe("浏览器登录 · 有效至 10月11日 04:51");
  });
});
