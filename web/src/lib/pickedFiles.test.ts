import { describe, expect, it } from "vitest";

import { pickedFromDrop } from "./pickedFiles";

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
