import { describe, expect, it, vi } from "vitest";

import { chooseFiles, pickedFromDrop } from "./pickedFiles";

// The file system entries a browser gives for a drop; a folder reads its
// contents in the pieces given.
const fileEntry = (file: File) =>
  ({ isFile: true, isDirectory: false, name: file.name, file: (done: (file: File) => void) => done(file) }) as never;
function folderEntry(name: string, ...pieces: unknown[][]) {
  return {
    isFile: false,
    isDirectory: true,
    name,
    createReader: () => {
      const left = [...pieces];
      return { readEntries: (done: (entries: unknown[]) => void) => done(left.shift() ?? []) };
    },
  } as never;
}
const item = (entry: unknown, file: File | null = null) =>
  ({ kind: "file", webkitGetAsEntry: () => entry, getAsFile: () => file }) as unknown as DataTransferItem;
const drop = (items: DataTransferItem[], files: File[] = []) => ({ items, files }) as unknown as DataTransfer;
const text = (name: string) => new File([name], name);

describe("pickedFromDrop", () => {
  it("reads folders with the folders above each file, in the order dropped", async () => {
    const holiday = folderEntry(
      "假期",
      [fileEntry(text("a.jpg")), folderEntry("第二天", [fileEntry(text("b.jpg"))])],
      [fileEntry(text("c.jpg"))],
    );
    const picked = await pickedFromDrop(drop([item(holiday), item(fileEntry(text("d.txt")))]));
    expect(picked.map(({ file, directories }) => [...directories, file.name].join("/"))).toEqual([
      "假期/a.jpg",
      "假期/第二天/b.jpg",
      "假期/c.jpg",
      "d.txt",
    ]);
  });

  it("keeps a dropped file the browser has no entry for", async () => {
    const picked = await pickedFromDrop(
      drop([item(folderEntry("假期", [fileEntry(text("a.jpg"))])), item(null, text("附件.pdf"))]),
    );
    expect(picked.map(({ file, directories }) => [...directories, file.name].join("/"))).toEqual([
      "假期/a.jpg",
      "附件.pdf",
    ]);
  });

  it("takes the plain file list when the drop has no items", async () => {
    const picked = await pickedFromDrop(drop([], [text("a.txt")]));
    expect(picked).toEqual([{ file: expect.objectContaining({ name: "a.txt" }), directories: [] }]);
  });
});

describe("choosing files", () => {
  it("keeps the input in the page while the picker is open and removes it after", () => {
    const click = vi.spyOn(HTMLInputElement.prototype, "click").mockImplementation(() => {});
    const chosen = vi.fn();
    chooseFiles({ accept: "image/*" }, chosen);
    const first = document.querySelector<HTMLInputElement>("body > input[type=file]")!;
    expect(first.hidden).toBe(true);
    // A browser that never said the first pick was cancelled: the next pick removes it.
    chooseFiles({ accept: "image/*", multiple: true }, chosen);
    expect(document.querySelectorAll("body > input[type=file]")).toHaveLength(1);
    const second = document.querySelector<HTMLInputElement>("body > input[type=file]")!;
    Object.defineProperty(second, "files", { value: [new File(["x"], "a.jpg", { type: "image/jpeg" })] });
    second.dispatchEvent(new Event("change"));
    expect(chosen).toHaveBeenCalledWith([expect.objectContaining({ name: "a.jpg" })]);
    expect(document.querySelector("body > input[type=file]")).toBeNull();
    expect(click).toHaveBeenCalledTimes(2);
    click.mockRestore();
  });

  it("removes the input and hands over nothing when the pick is cancelled or empty", () => {
    const click = vi.spyOn(HTMLInputElement.prototype, "click").mockImplementation(() => {});
    const chosen = vi.fn();
    chooseFiles({ accept: "image/*" }, chosen);
    document.querySelector("body > input[type=file]")!.dispatchEvent(new Event("cancel"));
    expect(document.querySelector("body > input[type=file]")).toBeNull();

    chooseFiles({ accept: "image/*" }, chosen);
    const input = document.querySelector<HTMLInputElement>("body > input[type=file]")!;
    Object.defineProperty(input, "files", { value: [] });
    input.dispatchEvent(new Event("change"));
    expect(document.querySelector("body > input[type=file]")).toBeNull();
    expect(chosen).not.toHaveBeenCalled();
    click.mockRestore();
  });
});
