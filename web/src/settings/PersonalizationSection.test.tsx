import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api";
import { PersonalizationSection } from "./PersonalizationSection";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

function installDisplay(options: { sleepAfterMinutes?: number; refuse?: boolean } = {}) {
  let stored = options.sleepAfterMinutes ?? 30;
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) !== "/api/v1/local-display") return json({ error: { code: "not_found", message: "not found" } }, 404);
    if (init?.method === "PUT") {
      if (options.refuse) return json({ error: { code: "forbidden", message: "administrator permission is required" } }, 403);
      stored = (JSON.parse(String(init.body)) as { sleepAfterMinutes: number }).sleepAfterMinutes;
    }
    return json({ sleepAfterMinutes: stored });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("Settings → 个性化", () => {
  beforeEach(() => setSession({ csrfToken: "csrf-token", expiresAt: "2026-10-21T00:00:00Z", user: { id: "user:owner", username: "owner", role: "admin", status: "active", createdAt: "2026-10-07T10:00:00Z" } }));
  afterEach(() => { setSession(); vi.unstubAllGlobals(); });

  it("lets an administrator choose when the local screen goes black", async () => {
    const fetchMock = installDisplay();
    const user = userEvent.setup();
    render(<PersonalizationSection isAdmin />);
    const choices = screen.getByRole("radiogroup", { name: "息屏时间" });
    expect(await within(choices).findByRole("radio", { name: "30 分钟", checked: true })).toBeTruthy();
    expect(screen.getByText(/登录页没有屏保，33 分钟后直接变黑/)).toBeTruthy();

    await user.click(within(choices).getByRole("radio", { name: "10 分钟" }));
    expect(await within(choices).findByRole("radio", { name: "10 分钟", checked: true })).toBeTruthy();
    expect(screen.getByText("13 分钟")).toBeTruthy();
    const [, init] = fetchMock.mock.calls.find(([, call]) => call?.method === "PUT")!;
    expect(JSON.parse(String(init?.body))).toEqual({ sleepAfterMinutes: 10 });
    expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe("csrf-token");

    await user.click(within(choices).getByRole("radio", { name: "从不" }));
    expect(await screen.findByText("屏保会一直播放，登录页一直亮着。")).toBeTruthy();
    expect(screen.queryByText("黑屏")).toBeNull();
  });

  it("puts the previous choice back when saving fails", async () => {
    installDisplay({ refuse: true });
    const user = userEvent.setup();
    render(<PersonalizationSection isAdmin />);
    const choices = screen.getByRole("radiogroup", { name: "息屏时间" });
    await within(choices).findByRole("radio", { name: "30 分钟", checked: true });
    await user.click(within(choices).getByRole("radio", { name: "1 小时" }));
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(within(choices).getByRole("radio", { name: "30 分钟", checked: true })).toBeTruthy();
    expect(within(choices).getByRole("radio", { name: "1 小时", checked: false })).toBeTruthy();
  });

  it("shows members the setting without letting them change it", async () => {
    const fetchMock = installDisplay({ sleepAfterMinutes: 60 });
    render(<PersonalizationSection isAdmin={false} />);
    const choices = screen.getByRole("radiogroup", { name: "息屏时间" });
    expect(await within(choices).findByRole("radio", { name: "1 小时", checked: true })).toBeTruthy();
    expect(screen.getByText("由管理员设置")).toBeTruthy();
    for (const choice of within(choices).getAllByRole("radio")) expect((choice as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("63 分钟")).toBeTruthy();
    expect(fetchMock.mock.calls.every(([, init]) => init?.method === undefined || init.method === "GET")).toBe(true);
  });
});
