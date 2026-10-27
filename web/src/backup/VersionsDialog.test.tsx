import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { FileEntry, Space } from "../api.generated";
import { stubAPI } from "../test/api";
import type { SnapshotMatch } from "./api";
import { VersionsDialog } from "./VersionsDialog";

const personal: Space = {
  id: "space:personal",
  kind: "private",
  name: "owner",
  ownerUserId: "user:owner",
  createdAt: "2026-10-01T00:00:00Z",
};
const hoursAgo = (hours: number) => new Date(Date.now() - hours * 3_600_000).toISOString();
const plan: FileEntry = {
  id: "file:plan",
  spaceId: personal.id,
  parentId: "file:docs",
  name: "计划.docx",
  kind: "file",
  sizeBytes: 2048,
  modifiedAt: hoursAgo(2),
};
const versions = (fileId: string): SnapshotMatch => ({
  path: "文档/计划.docx",
  name: "计划.docx",
  kind: "file",
  inSpace: true,
  versions: [
    {
      snapshotId: "snapshot:nightly",
      snapshotName: "定时快照 2026-10-14 03:00",
      origin: "scheduled",
      createdAt: hoursAgo(1),
      entryId: "entry:now",
      sizeBytes: 2048,
      modifiedAt: plan.modifiedAt,
      snapshots: 1,
      oldestAt: hoursAgo(1),
      current: fileId === plan.id,
    },
    {
      snapshotId: "snapshot:manual",
      snapshotName: "交付前",
      origin: "manual",
      createdAt: hoursAgo(30),
      entryId: "entry:before",
      sizeBytes: 1024,
      modifiedAt: hoursAgo(40),
      snapshots: 3,
      oldestAt: hoursAgo(90),
      current: fileId !== plan.id,
    },
  ],
});

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

afterEach(() => vi.unstubAllGlobals());

describe("VersionsDialog", () => {
  it("puts an older version back while the current file goes to the recycle bin", async () => {
    const calls: { url: string; body?: unknown }[] = [];
    stubAPI(
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
        const asked = /\/api\/v1\/files\/([^/]+)\/versions$/.exec(url);
        if (asked) return json(versions(decodeURIComponent(asked[1])));
        if (url.endsWith(`/entries/${encodeURIComponent("entry:before")}/restore`)) {
          return json({ ...plan, id: "file:plan-restored", sizeBytes: 1024, modifiedAt: hoursAgo(0) }, 201);
        }
        return json({ error: { code: "not_found", message: "not found" } }, 404);
      }),
    );
    const onChanged = vi.fn();
    const user = userEvent.setup();
    render(
      <VersionsDialog
        file={plan}
        space={personal}
        folders={["文档"]}
        onClose={() => undefined}
        onChanged={onChanged}
      />,
    );

    const dialog = await screen.findByRole("dialog", { name: "历史版本" });
    expect(within(dialog).getByText("个人空间 / 文档 · 现在 2.0 KiB，修改于", { exact: false })).toBeTruthy();
    const list = await within(dialog).findByRole("list", { name: "版本" });
    const [now, before] = within(list).getAllByRole("listitem");
    expect(within(now).getByText("当前版本")).toBeTruthy();
    expect(within(now).queryByRole("button", { name: /^还原到/ })).toBeNull();
    expect(within(before).getByText(/保存在 3 份快照中/)).toBeTruthy();

    await user.click(within(before).getByRole("button", { name: /^还原到/ }));
    const confirm = await screen.findByRole("dialog", { name: "还原到这个版本？" });
    expect(within(confirm).getByText(/现在的文件会移到回收站/)).toBeTruthy();
    await user.click(within(confirm).getByRole("button", { name: "还原" }));

    expect(await within(dialog).findByText(/原来的文件已移到回收站/)).toBeTruthy();
    expect(screen.queryByRole("dialog", { name: "还原到这个版本？" })).toBeNull();
    expect(calls.find((call) => call.url.endsWith("/restore"))?.body).toEqual({
      parentId: "file:docs",
      name: "计划.docx",
      replace: true,
    });
    expect(onChanged).toHaveBeenCalledTimes(1);
    // The list now follows the restored file, which holds the older version.
    await waitFor(() =>
      expect(calls.some((call) => call.url === "/api/v1/files/file%3Aplan-restored/versions")).toBe(true),
    );
  });

  it("copies a version beside the file under a new name", async () => {
    const calls: { url: string; body?: unknown }[] = [];
    stubAPI(
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
        if (url.endsWith("/versions")) return json(versions(plan.id));
        if (url === `/api/v1/spaces/${encodeURIComponent(personal.id)}/entries?parentId=`)
          return json({
            items: [
              {
                id: "file:docs",
                spaceId: personal.id,
                name: "文档",
                kind: "directory",
                sizeBytes: 0,
                modifiedAt: hoursAgo(1),
              },
            ],
          });
        if (url.endsWith("/restore"))
          return json({ ...plan, id: "file:copy", name: JSON.parse(String(init?.body)).name }, 201);
        return json({ items: [] });
      }),
    );
    const onChanged = vi.fn();
    const user = userEvent.setup();
    render(
      <VersionsDialog
        file={plan}
        space={personal}
        folders={["文档"]}
        onClose={() => undefined}
        onChanged={onChanged}
      />,
    );

    const list = await screen.findByRole("list", { name: "版本" });
    await user.click(within(within(list).getAllByRole("listitem")[1]).getByRole("button", { name: /另存一份$/ }));
    const restore = await screen.findByRole("dialog", { name: "恢复文件" });
    const name = within(restore).getByRole("textbox", { name: "恢复后的名称" }) as HTMLInputElement;
    expect(name.value).toMatch(/^计划 \(恢复 \d\d-\d\d \d\d\.\d\d\)\.docx$/);
    await waitFor(() => expect(within(restore).getByText("个人空间 / 文档")).toBeTruthy());
    await user.click(within(restore).getByRole("button", { name: "恢复" }));

    expect(await screen.findByText(`已另存为“${name.value}”。`)).toBeTruthy();
    expect(calls.find((call) => call.url.endsWith("/restore"))?.body).toEqual({
      parentId: "file:docs",
      name: name.value,
    });
    expect(onChanged).toHaveBeenCalledTimes(1);
  });
});
