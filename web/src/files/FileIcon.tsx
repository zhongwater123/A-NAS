import { FileText, Film, FolderClosed, Image, Music2 } from "lucide-react";

import { isPreviewableImage, isVideoFile } from "../lib/fileTypes";
import { FileBrowserItem } from "./model";
import { fileVisualURL } from "./preview";

export function FileIcon({ item }: { item: FileBrowserItem }) {
  return item.kind === "directory" ? (
    <FolderClosed />
  ) : item.kind === "file" ? (
    <FileText />
  ) : item.resource === "photos" ? (
    <Image />
  ) : item.resource === "music" ? (
    <Music2 />
  ) : (
    <Film />
  );
}

// FileArtwork is an item's icon with the thumbnail of an image or video over it.
export function FileArtwork({ item }: { item: FileBrowserItem }) {
  return (
    <>
      <FileIcon item={item} />
      {item.entry?.kind === "file" && (isPreviewableImage(item.name) || isVideoFile(item.name)) && (
        <img
          key={fileVisualURL(item.entry, "thumbnail")}
          src={fileVisualURL(item.entry, "thumbnail")}
          alt=""
          loading="lazy"
          onError={(event) => {
            event.currentTarget.hidden = true;
          }}
        />
      )}
    </>
  );
}
