import type { FileEntry } from "../api.generated";
import { fileRenderedPreviewURL, filePreviewURL } from "./api";
import { isHEIFImage, isVideoFile } from "../lib/fileTypes";

export function fileVisualURL(entry: FileEntry, size: "thumbnail" | "preview" = "preview"): string {
  if (!isHEIFImage(entry.name) && !isVideoFile(entry.name)) return filePreviewURL(entry.id);
  const base = fileRenderedPreviewURL(entry.id, size);
  return base + "&v=" + encodeURIComponent(entry.modifiedAt + ":" + entry.sizeBytes);
}
