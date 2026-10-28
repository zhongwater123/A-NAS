// Files chosen for upload, each with the folders above it inside what the
// user chose; plain files have none.
export interface PickedFile {
  file: File;
  directories: string[];
}

// pickedFromInput reads a file input; a folder input reports each file's
// path below the chosen folder, the folder included.
export function pickedFromInput(files: FileList | null): PickedFile[] {
  return Array.from(files ?? []).map((file) => {
    const path = file.webkitRelativePath || "";
    return { file, directories: path ? path.split("/").slice(0, -1) : [] };
  });
}

// carriesFiles tells whether a drag brings files from outside the page.
export function carriesFiles(transfer: DataTransfer | null): boolean {
  return Boolean(transfer && Array.from(transfer.types).includes("Files"));
}

// pickedFromDrop reads dropped files and folders, in the order they came.
// The entries must be taken while the drop event runs; their contents are
// read afterwards, all at once. A dropped file the browser offers no entry
// for comes as it is, outside any folder. Empty folders carry no file and are
// not uploaded.
export function pickedFromDrop(transfer: DataTransfer): Promise<PickedFile[]> {
  const dropped = Array.from(transfer.items ?? []).flatMap((item): Array<FileSystemEntry | PickedFile> => {
    if (item.kind !== "file") return [];
    const entry = item.webkitGetAsEntry?.() ?? null;
    if (entry) return [entry];
    const file = item.getAsFile();
    return file ? [{ file, directories: [] }] : [];
  });
  if (!dropped.length) return Promise.resolve(Array.from(transfer.files).map((file) => ({ file, directories: [] })));
  return Promise.all(dropped.map((item) => ("directories" in item ? [item] : walk(item, [])))).then((lists) =>
    lists.flat(),
  );
}

async function walk(entry: FileSystemEntry, directories: string[]): Promise<PickedFile[]> {
  if (entry.isFile) {
    const file = await new Promise<File>((resolve, reject) => (entry as FileSystemFileEntry).file(resolve, reject));
    return [{ file, directories }];
  }
  if (!entry.isDirectory) return [];
  const reader = (entry as FileSystemDirectoryEntry).createReader();
  const children: FileSystemEntry[] = [];
  // readEntries returns a directory in pieces until it returns none.
  for (;;) {
    const piece = await new Promise<FileSystemEntry[]>((resolve, reject) => reader.readEntries(resolve, reject));
    if (!piece.length) break;
    children.push(...piece);
  }
  const inside = [...directories, entry.name];
  return (await Promise.all(children.map((child) => walk(child, inside)))).flat();
}
