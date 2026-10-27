import { type DragEvent, type RefObject, useEffect, useRef } from "react";

import { type ImageDropProps, type ImageQuery, imageFile, pickImage, prepareImageQuery } from "./imageSearch";
import { type Caller, type Place, placeViewing } from "./model";
import { carriesFiles } from "./usePanelInput";

interface Options {
  caller: Caller;
  placeRef: RefObject<Place | undefined>;
  setPlace: (place: Place) => void;
  setImageOver: (name: string) => void;
  endDrag: () => void;
  reportError: (message: string) => void;
}

// useImageSearch searches the library by an image chosen, dropped onto a
// search box or pasted into one.
export function useImageSearch({ caller, placeRef, setPlace, setImageOver, endDrag, reportError }: Options) {
  // The images searched by stay viewable while their places can come back.
  const imageURLs = useRef<string[]>([]);
  useEffect(() => () => imageURLs.current.forEach((url) => URL.revokeObjectURL(url)), []);

  // The browser reads the image; local AI on the NAS encodes it once and the
  // photos like it come back as similar photos do. Another image takes the
  // place of the last one, so going back leaves the image search.
  const searchByImage = async (file: File, pasted = false) => {
    let query: ImageQuery;
    try {
      query = await prepareImageQuery(file, pasted ? "粘贴的图片" : file.name || "图片");
    } catch {
      reportError("无法读取这张图片，换一张试试");
      return;
    }
    imageURLs.current.push(query.url);
    const from = placeRef.current;
    if (from)
      setPlace({
        kind: "image",
        viewing: placeViewing(caller, from),
        query,
        from: from.kind === "image" ? from.from : from,
      });
  };
  const pickSearchImage = () => pickImage((file) => void searchByImage(file));
  // A search box takes an image dropped onto it or pasted into it.
  const imageDrop: Omit<ImageDropProps, "onDragOver"> = {
    onDragLeave: (event) => {
      if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setImageOver("");
    },
    onDrop: (event) => {
      if (!carriesFiles(event)) return;
      event.preventDefault();
      event.stopPropagation();
      endDrag();
      const file = imageFile(event.dataTransfer.files);
      if (file) void searchByImage(file);
      else reportError("以图搜图需要一张图片");
    },
    onPaste: (event) => {
      const file = imageFile(event.clipboardData.files);
      if (!file) return;
      event.preventDefault();
      void searchByImage(file, true);
    },
  };
  // imageDragOver marks the search box a file is over by its name.
  const imageDragOver = (name: string) => (event: DragEvent) => {
    if (!carriesFiles(event)) return;
    event.preventDefault();
    event.stopPropagation();
    event.dataTransfer.dropEffect = "copy";
    setImageOver(name);
  };

  return { pickSearchImage, imageDrop, imageDragOver };
}
