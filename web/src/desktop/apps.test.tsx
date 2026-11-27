import { describe, expect, it, vi } from "vitest";

import { noScoring } from "../scoring/status";
import { desktopApps, type WindowContext } from "./apps";

// The desktop's context with VNAS waiting for the administrator's key (ADR 0024).
function withoutKey(isAdmin: boolean): WindowContext {
  return {
    isAdmin,
    assistantReady: false,
    assistantNeedsKey: true,
    openSettings: vi.fn(),
    open: vi.fn(),
    webSearchEnabled: false,
    scoring: noScoring,
  } as unknown as WindowContext;
}

const vnas = (context: WindowContext) => desktopApps(context, () => false).find((app) => app.id === "ai");

describe("desktopApps", () => {
  it("takes an administrator from VNAS without a key to where it is set, and the launcher still finds it", () => {
    const context = withoutKey(true);
    const app = vnas(context);
    expect(app).toMatchObject({ unavailable: true, note: "未填写密钥" });
    expect(app?.keywords).toContain("助手");
    app?.onClick?.();
    expect(context.openSettings).toHaveBeenCalledWith("assistant");
    expect(context.open).not.toHaveBeenCalled();
  });

  it("tells a member that the administrator sets VNAS up, with nothing to click", () => {
    const app = vnas(withoutKey(false));
    expect(app).toMatchObject({ unavailable: true, note: "需管理员设置" });
    expect(app?.keywords).toContain("助手");
    expect(app?.onClick).toBeUndefined();
  });

  it("shows 共享工作区 only to accounts that may use it", () => {
    const has = (context: WindowContext) => desktopApps(context, () => false).some((app) => app.id === "workspace");
    expect(has({ ...withoutKey(false), workspace: null })).toBe(false);
    expect(has({ ...withoutKey(false), workspace: { admin: false } })).toBe(true);
  });
});
