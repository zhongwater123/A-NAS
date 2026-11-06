import { act, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { reportAwaiting, reportTurn, resetAssistantActivity } from "../assistant/assistantActivity";
import { http, server } from "../test/server";
import { Companion } from "./Companion";

const origin = window.location.origin;
const served = (names: string[]) =>
  Object.fromEntries(names.map((name) => [name, { webm: `/companion/animations/${encodeURIComponent(name)}.webm` }]));

beforeEach(() => {
  // jsdom plays no video and makes no object URLs; the animation chain only needs them to exist.
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockReturnValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "load").mockReturnValue(undefined);
  URL.createObjectURL ??= () => "blob:animation";
  URL.revokeObjectURL ??= () => undefined;
  server.use(http.untyped.get(`${origin}/companion/animations/:name`, () => new HttpResponse(new Blob(["webm"]))));
});
afterEach(() => {
  resetAssistantActivity();
  localStorage.clear();
});

describe("the companion on the desktop", () => {
  it("shows nothing while no animations are installed", async () => {
    let asked = false;
    server.use(
      http.untyped.get(`${origin}/companion/animations`, () => {
        asked = true;
        return HttpResponse.json({ animations: {} });
      }),
    );
    render(<Companion />);
    await waitFor(() => expect(asked).toBe(true));
    await act(() => Promise.resolve());
    expect(screen.queryByRole("button", { name: "大肥鱼" })).toBeNull();
  });

  it("plays 大肥鱼 with dsh-pet's animations, credits them and follows what VNAS is doing", async () => {
    server.use(
      http.untyped.get(`${origin}/companion/animations`, () =>
        HttpResponse.json({
          animations: served([
            "待机呼吸休闲",
            "东张西望",
            "工作状态-忙碌点按",
            "工作状态-原地踱步张望",
            "工作状态-雀跃庆祝",
          ]),
        }),
      ),
    );
    render(<Companion />);
    const pet = await screen.findByRole("button", { name: "大肥鱼" });
    expect(pet.title).toContain("github.com/PC2005-cloud/dsh-pet");
    const root = pet.closest("[data-anim]") as HTMLElement;
    // Only installed animations play: the first is always the idle one.
    expect(root.dataset.anim).toBe("待机呼吸休闲");

    act(() => reportTurn("live0001", "working"));
    await waitFor(() => expect(root.dataset.anim).toBe("工作状态-忙碌点按"));
    expect(root.dataset.work).toBe("working");
    act(() => reportAwaiting(1));
    await waitFor(() => expect(root.dataset.anim).toBe("工作状态-原地踱步张望"));
    act(() => reportAwaiting(0));
    act(() => reportTurn("live0001", "success"));
    await waitFor(() => expect(root.dataset.anim).toBe("工作状态-雀跃庆祝"));
    // A stage without its animation installed stays with an idle one.
    act(() => reportTurn("live0001", "error"));
    await waitFor(() => expect(root.dataset.work).toBe("error"));
    expect(root.dataset.anim).toBe("待机呼吸休闲");
  });

  it("is not shown in a browser that needs animations this NAS does not have", async () => {
    vi.spyOn(navigator, "userAgent", "get").mockReturnValue(
      "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 Version/26.0 Mobile/15E148 Safari/604.1",
    );
    let asked = false;
    server.use(
      http.untyped.get(`${origin}/companion/animations`, () => {
        asked = true;
        return HttpResponse.json({ animations: served(["待机呼吸休闲"]) });
      }),
    );
    render(<Companion />);
    await waitFor(() => expect(asked).toBe(true));
    await act(() => new Promise((resolve) => setTimeout(resolve, 50)));
    expect(screen.queryByRole("button", { name: "大肥鱼" })).toBeNull();
  });
});
