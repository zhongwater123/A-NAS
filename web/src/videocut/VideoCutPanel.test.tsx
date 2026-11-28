import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it } from "vitest";

import { http, server } from "../test/server";
import { getTimeline } from "./api/client";
import { CutProvider, createCutStore } from "./app/cutStore";
import { useEditor } from "./app/editor";
import { keys } from "./app/works";
import { VenusFake } from "./mocks/fake";
import { videocutHandlers } from "./mocks/handlers";
import { ToastProvider } from "./ui/Toast";
import { VideoCutPanel } from "./VideoCutPanel";

const file = (name: string) => ({ name, size: 1000, modifiedAt: "2026-11-20T08:00:00Z", read: true, duration: 8 });

let fake: VenusFake;
beforeEach(() => {
  fake = new VenusFake();
  server.use(...videocutHandlers(fake, http));
});

describe("Venus's shell", () => {
  it("goes from the start page to the prepare page, and to the film page as the film is made", async () => {
    const { project } = fake.create("k1", [file("a.mp4"), file("b.mp4")]);
    render(<VideoCutPanel poll={30} />);
    await userEvent.click(await screen.findByRole("button", { name: project.name }));

    // Pending: the prepare page, with its slots.
    expect(await screen.findByRole("region", { name: "M4-5" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "M3-3" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "开始生成" }));

    // Being made: the film page's 生成中, the parts drawn from the song and no shots yet.
    expect(await screen.findByText("生成中")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "M5-1" })).toBeInTheDocument();
    expect(screen.getByText("生成完成后出现")).toBeInTheDocument();
    expect(
      await within(screen.getByRole("region", { name: "M6-3" })).findByRole("button", { name: "副歌" }),
    ).toBeDisabled();

    // Made: the player, the assistant and the shots; the label goes.
    expect(await screen.findByRole("button", { name: "镜头 1" }, { timeout: 5_000 })).toBeInTheDocument();
    expect(screen.queryByText("生成中")).toBeNull();
    expect(screen.getByRole("region", { name: "M6-1" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "M8" })).toBeInTheDocument();
    expect(await screen.findByText("成片已生成")).toBeInTheDocument();
  });

  it("turns the inspector to the shot clicked, and moves the playhead to it", async () => {
    render(<VideoCutPanel poll={30} />);
    await userEvent.click(await screen.findByRole("button", { name: "示例" }));
    await userEvent.click(await screen.findByRole("button", { name: "镜头 3" }));
    expect(screen.getByRole("button", { name: "镜头 3" })).toHaveAttribute("aria-pressed", "true");
    expect(within(screen.getByRole("region", { name: "M7" })).getByText("镜头 3")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "M8" })).toBeNull();
    // Slot 3 starts 4 seconds in: two 2-second slots of 前奏 before it.
    expect(screen.getByRole("status", { name: "播放头" })).toHaveTextContent("0:04");
  });
});

describe("useEditor", () => {
  async function readyWork() {
    const { project } = fake.create("k2", [file("c.mp4")]);
    fake.generate(project.id, "g");
    while (fake.step(project.id).state !== "ready");
    const client = new QueryClient();
    client.setQueryData(keys.timeline(project.id), await getTimeline(project.id));
    return { id: project.id, client };
  }

  function editorFor(id: string, client: QueryClient, mode: "ready" | "exporting") {
    const store = createCutStore({ mode });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>
        <ToastProvider>
          <CutProvider store={store}>{children}</CutProvider>
        </ToastProvider>
      </QueryClientProvider>
    );
    return renderHook(() => useEditor(id), { wrapper }).result;
  }

  // edits counts the edits that reach VideoCut.
  function countEdits() {
    let edits = 0;
    server.events.on("request:start", ({ request }) => {
      if (request.method === "POST" && request.url.includes("/timeline/")) edits++;
    });
    return () => edits;
  }

  it("refuses a second edit while one is being made", async () => {
    const { id, client } = await readyWork();
    const editor = editorFor(id, client, "ready");
    const edits = countEdits();
    let made: boolean[] = [];
    await act(async () => {
      made = await Promise.all([editor.current.remove(1), editor.current.remove(2)]);
    });
    expect(made).toEqual([true, false]);
    expect(edits()).toBe(1);
    await waitFor(() => expect(editor.current.canUndo).toBe(true));
  });

  it("refuses every edit while the film exports", async () => {
    const { id, client } = await readyWork();
    const editor = editorFor(id, client, "exporting");
    const edits = countEdits();
    let made: boolean[] = [];
    await act(async () => {
      made = [
        await editor.current.replace(1, "x/1"),
        await editor.current.undo(),
        await editor.current.reorderParts(1, 2),
      ];
    });
    expect(made).toEqual([false, false, false]);
    expect(edits()).toBe(0);
  });
});
