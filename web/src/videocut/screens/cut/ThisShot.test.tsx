import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse } from "msw";
import { beforeEach, describe, expect, it } from "vitest";

import { http, server } from "../../../test/server";
import { VenusFake } from "../../mocks/fake";
import { videocutHandlers } from "../../mocks/handlers";
import { VideoCutPanel } from "../../VideoCutPanel";

// The sample chain of M7-1 (#699), on the stand-ins: select a shot, replace
// it with a candidate, undo it, and find the candidates locked while the
// film exports.
let fake: VenusFake;
let workId: string;

beforeEach(() => {
  fake = new VenusFake();
  server.use(...videocutHandlers(fake, http));
  workId = fake.made("k", [
    { name: "a.mp4", size: 1000, modifiedAt: "2026-11-20T08:00:00Z", read: true, duration: 8 },
  ]).id;
});

async function openShot(slot: number) {
  render(<VideoCutPanel poll={30} />);
  await userEvent.click(await screen.findByRole("button", { name: fake.project(workId).name }));
  await userEvent.click(await screen.findByRole("button", { name: `镜头 ${slot}` }));
  return screen.getByRole("region", { name: "这一镜" });
}

const stillOf = (panel: HTMLElement) => panel.querySelector("img")?.getAttribute("src");
const candidatesIn = (panel: HTMLElement) => within(panel).getAllByRole("button", { name: /^候选 \d$/ });

describe("这一镜: replacing a shot with a candidate (M7-1)", () => {
  it("shows the shot, replaces it, puts the beat taken out first, and undoes it", async () => {
    const panel = await openShot(3);
    const facts = within(panel).getByText("0:04–0:06").parentElement;
    expect(facts).toHaveTextContent("2秒");
    expect(facts).toHaveTextContent("2 拍");
    await waitFor(() => expect(candidatesIn(panel)).toHaveLength(6));
    const before = stillOf(panel);
    const undo = screen.getByRole("button", { name: "撤销" });
    expect(undo).toBeDisabled();

    await userEvent.click(candidatesIn(panel)[0]);
    expect(await screen.findByText("已替换镜头 3")).toBeInTheDocument();
    await waitFor(() => expect(stillOf(panel)).not.toBe(before));
    // The beat taken out is the first candidate now.
    await waitFor(() => expect(candidatesIn(panel)[0].querySelector("img")?.getAttribute("src")).toBe(before));
    // The player plays again from the shot changed.
    expect(screen.getByRole("status", { name: "播放头" })).toHaveTextContent("0:04");

    await waitFor(() => expect(undo).toBeEnabled());
    await userEvent.click(undo);
    expect(await screen.findByText("已撤销")).toBeInTheDocument();
    await waitFor(() => expect(stillOf(panel)).toBe(before));
    expect(undo).toBeDisabled();
  });

  it("undoes with Ctrl+Z from the page", async () => {
    const panel = await openShot(5);
    await waitFor(() => expect(candidatesIn(panel)).toHaveLength(6));
    const before = stillOf(panel);
    await userEvent.click(candidatesIn(panel)[1]);
    expect(await screen.findByText("已替换镜头 5")).toBeInTheDocument();
    await userEvent.keyboard("{Control>}z{/Control}");
    expect(await screen.findByText("已撤销")).toBeInTheDocument();
    await waitFor(() => expect(stillOf(panel)).toBe(before));
  });

  it("locks the candidates while the film exports, and keeps the player", async () => {
    const panel = await openShot(2);
    await waitFor(() => expect(candidatesIn(panel)).toHaveLength(6));
    act(() => {
      fake.options.advanceOnRead = false;
      fake.export(workId, "e");
    });
    expect(await screen.findByText("导出中")).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "这一镜" })).getByText("导出中，暂不能修改")).toBeInTheDocument();
    for (const candidate of candidatesIn(panel)) expect(candidate).toBeDisabled();
    expect(screen.getByRole("region", { name: "M6-1" })).toBeInTheDocument();
  });

  it("shows the sample's candidates, locked", async () => {
    render(<VideoCutPanel poll={30} />);
    await userEvent.click(await screen.findByRole("button", { name: "示例" }));
    await userEvent.click(await screen.findByRole("button", { name: "镜头 2" }));
    const panel = screen.getByRole("region", { name: "这一镜" });
    await waitFor(() => expect(candidatesIn(panel)).toHaveLength(6));
    for (const candidate of candidatesIn(panel)) expect(candidate).toBeDisabled();
  });

  it("says so when the shot has no candidate left", async () => {
    server.use(
      http.get("/api/v1/videocut/projects/{projectId}/slots/{slot}/candidates", ({ params, response }) =>
        response(200).json({ slot: Number(params.slot), items: [] }),
      ),
    );
    const panel = await openShot(4);
    expect(await within(panel).findByText("这一镜没有可替换的片段")).toBeInTheDocument();
  });

  it("sends nothing on Ctrl+Z with nothing to undo, or while a dialog is open", async () => {
    let undos = 0;
    server.events.on("request:start", ({ request }) => {
      if (request.url.endsWith("/timeline/undo")) undos++;
    });
    const panel = await openShot(3);
    await waitFor(() => expect(candidatesIn(panel)).toHaveLength(6));
    await userEvent.keyboard("{Control>}z{/Control}");
    expect(undos).toBe(0);
    await userEvent.click(candidatesIn(panel)[0]);
    expect(await screen.findByText("已替换镜头 3")).toBeInTheDocument();
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    document.body.append(dialog);
    try {
      await userEvent.keyboard("{Control>}z{/Control}");
      await userEvent.keyboard("{Control>}{Shift>}z{/Shift}{/Control}");
    } finally {
      dialog.remove();
    }
    expect(undos).toBe(0);
  });

  it("says when the candidates could not be read, and reads them again", async () => {
    let fail = true;
    server.use(
      http.get("/api/v1/videocut/projects/{projectId}/slots/{slot}/candidates", ({ params, response }) =>
        fail ? response.untyped(HttpResponse.error()) : response(200).json({ slot: Number(params.slot), items: [] }),
      ),
    );
    const panel = await openShot(4);
    expect(await within(panel).findByText("没能读取候选片段", {}, { timeout: 5_000 })).toBeInTheDocument();
    fail = false;
    await userEvent.click(within(panel).getByRole("button", { name: "重试" }));
    expect(await within(panel).findByText("这一镜没有可替换的片段")).toBeInTheDocument();
  });
});
