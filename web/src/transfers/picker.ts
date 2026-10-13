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

// pickedFromDrop reads dropped files and folders. The entries must be taken
// while the drop event runs; their contents are read afterwards. Empty
// folders carry no file and are not uploaded.
export function pickedFromDrop(transfer: DataTransfer): Promise<PickedFile[]> {
  const entries = Array.from(transfer.items)
    .map((item) => (item.kind === "file" ? item.webkitGetAsEntry?.() ?? null : null))
    .filter((entry): entry is FileSystemEntry => entry !== null);
  if (!entries.length) return Promise.resolve(Array.from(transfer.files).map((file) => ({ file, directories: [] })));
  return (async () => {
    const picked: PickedFile[] = [];
    const walk = async (entry: FileSystemEntry, directories: string[]) => {
      if (entry.isFile) {
        const file = await new Promise<File>((resolve, reject) => (entry as FileSystemFileEntry).file(resolve, reject));
        picked.push({ file, directories });
        return;
      }
      if (!entry.isDirectory) return;
      const reader = (entry as FileSystemDirectoryEntry).createReader();
      // readEntries returns a directory in pieces until it returns none.
      for (;;) {
        const children = await new Promise<FileSystemEntry[]>((resolve, reject) => reader.readEntries(resolve, reject));
        if (!children.length) break;
        for (const child of children) await walk(child, [...directories, entry.name]);
      }
    };
    for (const entry of entries) await walk(entry, []);
    return picked;
  })();
}
