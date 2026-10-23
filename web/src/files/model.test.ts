import { describe, expect, it } from "vitest";

import { FileEntry, Volume } from "../api";
import { FileOrder } from "../useFileOrder";
import {
  browserItem,
  duplicateFileName,
  FileBrowserItem,
  personalFileResources,
  selectionCommands,
  SelectionState,
  sortedBy,
  spaceLabel,
  volumeUsage,
  withPins,
} from "./model";

const order: FileOrder = { sortBy: "name", direction: "ascending", pinned: [], manual: [] };
const entry = (id: string, kind: FileEntry["kind"] = "file", parentId?: string): FileEntry => ({
  id,
  spaceId: "space:own",
  parentId,
  name: kind === "file" ? `${id}.txt` : id,
  kind,
  sizeBytes: 10,
  modifiedAt: "2026-10-20T10:00:00Z",
});
const report = browserItem(entry("report"));
const notes = browserItem(entry("notes"));
const photos = browserItem(entry("photos", "directory"));
const music = browserItem(entry("music", "directory"));
const state = (items: FileBrowserItem[], extra: Partial<SelectionState> = {}): SelectionState => ({
  items,
  item: items.at(-1),
  order,
  spaceID: "space:own",
  parentID: "",
  viewing: false,
  loading: false,
  busy: false,
  ...extra,
});

describe("file manager selection", () => {
  it("offers every command for one file of one's own", () => {
    expect(selectionCommands(state([report]))).toMatchObject({
      mutate: true,
      share: true,
      rename: true,
      versions: true,
      download: true,
      archive: false,
      pin: false,
    });
  });

  it("renames one item only and downloads several or a folder as one archive", () => {
    const several = selectionCommands(state([report, notes]));
    expect(several).toMatchObject({ mutate: true, rename: false, versions: false, archive: true });
    expect(selectionCommands(state([photos]))).toMatchObject({ rename: true, versions: false, archive: true });
  });

  it("acts on files only, not on the app resources beside them", () => {
    const withResource = selectionCommands(state([report, personalFileResources[0]]));
    expect(withResource).toMatchObject({ mutate: false, share: false, download: false });
    expect(withResource.entries).toEqual([report.entry]);
  });

  it("only downloads while viewing another member's space, and waits while an operation runs", () => {
    expect(selectionCommands(state([report], { viewing: true }))).toMatchObject({
      mutate: false,
      share: false,
      rename: false,
      versions: false,
      download: true,
    });
    expect(selectionCommands(state([report], { busy: true }))).toMatchObject({ mutate: false, download: true });
  });

  it("pins folders only when nothing but folders is selected", () => {
    expect(selectionCommands(state([photos, music]))).toMatchObject({ pin: true, allPinned: false });
    expect(selectionCommands(state([photos, report]))).toMatchObject({ pin: false });
    expect(selectionCommands(state([photos], { loading: true }))).toMatchObject({ pin: false });
    const pinned = { ...order, pinned: [photos.id, music.id] };
    expect(selectionCommands(state([photos, music], { order: pinned }))).toMatchObject({ allPinned: true });
  });

  it("pastes anywhere but where cut items already are", () => {
    const cut = { entries: [report.entry!], mode: "move" as const };
    expect(selectionCommands(state([], { clipboard: cut })).paste).toBe(false);
    expect(selectionCommands(state([], { clipboard: cut, parentID: "folder" })).paste).toBe(true);
    const copied = { entries: [report.entry!], mode: "copy" as const };
    expect(selectionCommands(state([], { clipboard: copied })).paste).toBe(true);
    expect(selectionCommands(state([], { clipboard: copied, viewing: true })).paste).toBe(false);
    expect(selectionCommands(state([])).paste).toBe(false);
  });
});

describe("file manager order", () => {
  it("turns the direction around on the same column and starts dates and sizes with the largest", () => {
    expect(sortedBy(order, "name", [])).toMatchObject({ sortBy: "name", direction: "descending" });
    expect(sortedBy(order, "modifiedAt", [])).toMatchObject({ sortBy: "modifiedAt", direction: "descending" });
    expect(sortedBy(order, "sizeBytes", [])).toMatchObject({ direction: "descending" });
    expect(sortedBy(order, "type", [])).toMatchObject({ direction: "ascending" });
  });

  it("starts a manual order from what is shown and keeps one already made", () => {
    expect(sortedBy(order, "manual", [notes, report]).manual).toEqual([notes.id, report.id]);
    const made = { ...order, manual: [report.id] };
    expect(sortedBy(made, "manual", [notes, report]).manual).toEqual([report.id]);
  });

  it("pins folders, and unpins them once all are pinned", () => {
    const one = withPins(order, [photos]);
    expect(one.pinned).toEqual([photos.id]);
    expect(withPins(one, [photos, music]).pinned).toEqual([photos.id, music.id]);
    expect(withPins({ ...order, pinned: [photos.id, music.id] }, [photos, music]).pinned).toEqual([]);
  });
});

describe("file manager labels", () => {
  it("reports unknown volume usage instead of a full volume", () => {
    const volume: Volume = { id: "volume:data", diskIds: ["disk:a"], layout: "single", state: "available" };
    expect(volumeUsage(undefined)).toMatchObject({ known: false, usedPercent: 0 });
    expect(volumeUsage({ ...volume, capacityBytes: 1000 })).toMatchObject({ known: false, capacityBytes: 1000 });
    expect(volumeUsage({ ...volume, capacityBytes: 1000, availableBytes: 250 })).toMatchObject({
      known: true,
      usedBytes: 750,
      usedPercent: 75,
    });
  });

  it("names a viewed space by its owner", () => {
    const space = { id: "space:other", kind: "private" as const, name: "lin", createdAt: "" };
    expect(spaceLabel(space, "林")).toBe("个人空间");
    expect(spaceLabel({ ...space, viewing: { grantId: "g", expiresAt: "" } }, "林")).toBe("只读查看 · 林");
    expect(spaceLabel({ ...space, kind: "shared" }, "林")).toBe("共享空间");
  });

  it("names a copy before its extension", () => {
    expect(duplicateFileName("report.txt")).toBe("report - 副本.txt");
    expect(duplicateFileName("archive.tar.gz")).toBe("archive.tar - 副本.gz");
    expect(duplicateFileName(".env")).toBe(".env - 副本");
    expect(duplicateFileName("notes")).toBe("notes - 副本");
  });
});
