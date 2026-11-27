import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { setSession } from "../api/client";
import { session } from "../test/fixtures";
import { einkHandlers, einkScreen } from "../test/handlers/eink";
import { server } from "../test/server";
import { EinkSection } from "./EinkSection";

function serve(initial = einkHandlers().screens) {
  const eink = einkHandlers(initial);
  server.use(...eink.handlers);
  return eink.screens;
}

describe("Settings → 墨水屏", () => {
  beforeEach(() => setSession(session()));
  afterEach(() => setSession());

  it("pairs a screen and shows its token once", async () => {
    const screens = serve([]);
    const user = userEvent.setup();
    render(<EinkSection />);

    expect(await screen.findByText("还没有配对的墨水屏")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "添加墨水屏" }));
    await user.type(screen.getByRole("textbox", { name: "名字" }), "客厅");
    await user.click(screen.getByRole("button", { name: "添加" }));

    const token = await screen.findByRole("textbox", { name: "设备令牌" });
    expect(token).toHaveValue("TOKENTOKENTOKENTOKENTOKENTOKENTOKENTOKENTOKEN");
    expect(screen.getByText(/关掉这个窗口后再也看不到它/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "已经写好了" }));

    expect(screen.queryByRole("textbox", { name: "设备令牌" })).not.toBeInTheDocument();
    expect(await screen.findByText("客厅")).toBeInTheDocument();
    expect(screen.getByText("还没来过")).toBeInTheDocument();
    expect(screens.map((item) => item.name)).toEqual(["客厅"]);
  });

  it("says when a name is taken", async () => {
    serve([einkScreen("客厅")]);
    const user = userEvent.setup();
    render(<EinkSection />);

    await screen.findByText("客厅");
    await user.click(screen.getByRole("button", { name: "添加墨水屏" }));
    await user.type(screen.getByRole("textbox", { name: "名字" }), "客厅");
    await user.click(screen.getByRole("button", { name: "添加" }));
    expect(await screen.findByText("已经有一块屏叫这个名字")).toBeInTheDocument();
  });

  it("shows what a screen last said, when it is overdue and when to charge it", async () => {
    const asked = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString();
    const due = new Date(Date.now() - 60 * 60 * 1000).toISOString();
    serve([
      einkScreen("客厅", {
        seenAt: asked,
        nextAt: due,
        report: { battery: 8, firmware: "0.2.5-s3", fail: "wifi", history: "0715k-wifi-11" },
      }),
    ]);
    render(<EinkSection />);

    const row = (await screen.findByText("客厅")).closest("[class]") as HTMLElement;
    await waitFor(() => expect(screen.getByText(/电量 8%/)).toBeInTheDocument());
    expect(screen.getByText(/固件 0.2.5-s3/)).toBeInTheDocument();
    expect(screen.getByText(/最近醒来：0715k-wifi-11/)).toBeInTheDocument();
    expect(within(row.parentElement ?? row).getByText("该来没来")).toBeInTheDocument();
    expect(screen.getByText("该充电了")).toBeInTheDocument();
  });

  it("renames and removes a screen", async () => {
    const screens = serve([einkScreen("客厅")]);
    const user = userEvent.setup();
    render(<EinkSection />);

    await user.click(await screen.findByRole("button", { name: "更多操作：客厅" }));
    await user.click(await screen.findByRole("menuitem", { name: "改名" }));
    const name = screen.getByRole("textbox", { name: "名字" });
    await user.clear(name);
    await user.type(name, "玄关");
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByText("玄关")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "更多操作：玄关" }));
    await user.click(await screen.findByRole("menuitem", { name: "移除" }));
    await user.click(screen.getByRole("button", { name: "移除" }));
    expect(await screen.findByText("还没有配对的墨水屏")).toBeInTheDocument();
    expect(screens).toEqual([]);
  });
});
