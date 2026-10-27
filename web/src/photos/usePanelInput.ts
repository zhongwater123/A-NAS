import { type DragEvent, type KeyboardEvent, type RefObject, useRef, useState } from "react";

import type { useDialogs } from "../ui/feedback";
import type { PhotoAlbum, PhotoAsset } from "./api";
import type { Place } from "./model";
import type { useSelection } from "./selection";
import type { UploadTarget, useUploads } from "./uploads";

export const carriesFiles = (event: DragEvent) => Array.from(event.dataTransfer.types).includes("Files");

interface Options {
  viewer?: { id: string; origin?: DOMRect };
  dialogs: ReturnType<typeof useDialogs>;
  picker?: PhotoAsset[];
  photoPicker?: PhotoAlbum;
  selection: ReturnType<typeof useSelection>;
  place?: Place;
  viewAssets: PhotoAsset[];
  searchInput: RefObject<HTMLInputElement | null>;
  uploadTarget?: UploadTarget;
  uploads: ReturnType<typeof useUploads>;
}

// usePanelInput is what the window does with keys pressed in it and files
// dragged over it: keys select and search, and files dropped anywhere upload
// to the place shown. keyDown and dropHandlers go on the window's root.
export function usePanelInput({
  viewer,
  dialogs,
  picker,
  photoPicker,
  selection,
  place,
  viewAssets,
  searchInput,
  uploadTarget,
  uploads,
}: Options) {
  const [dragging, setDragging] = useState(false);
  // Which search box a dragged file is over; dropping it there searches by
  // it instead of uploading it.
  const [imageOver, setImageOver] = useState("");
  const dragDepth = useRef(0);

  const keyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (viewer || dialogs.open || picker || photoPicker) return;
    const typing = (event.target as HTMLElement).closest("input, textarea");
    if (event.key === "Escape" && selection.selected.size && !typing) {
      event.preventDefault();
      selection.clear();
    } else if (
      (event.ctrlKey || event.metaKey) &&
      event.key.toLowerCase() === "a" &&
      !typing &&
      place?.kind !== "albums" &&
      !(place?.kind === "search" && !place.query)
    ) {
      event.preventDefault();
      selection.replace(viewAssets.map((asset) => asset.id));
    } else if (event.key === "/" && !typing) {
      event.preventDefault();
      searchInput.current?.focus();
    }
  };
  const endDrag = () => {
    dragDepth.current = 0;
    setDragging(false);
    setImageOver("");
  };
  const dropHandlers = {
    onDragEnter: (event: DragEvent) => {
      if (carriesFiles(event)) {
        dragDepth.current++;
        setDragging(true);
      }
    },
    onDragLeave: (event: DragEvent) => {
      if (carriesFiles(event) && --dragDepth.current <= 0) endDrag();
    },
    onDragOver: (event: DragEvent) => {
      if (!carriesFiles(event)) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = uploadTarget ? "copy" : "none";
      setImageOver("");
    },
    onDrop: (event: DragEvent) => {
      if (!carriesFiles(event)) return;
      event.preventDefault();
      endDrag();
      if (uploadTarget) uploads.add(Array.from(event.dataTransfer.files), uploadTarget);
    },
  };

  return { keyDown, dropHandlers, dragging, imageOver, setImageOver, endDrag };
}
