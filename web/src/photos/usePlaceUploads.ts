import { type Dispatch, type SetStateAction, useCallback, useEffect, useMemo } from "react";

import type { useToasts } from "../ui/feedback";
import type { PhotoLibrary } from "./api";
import { type Caller, canEditAlbum, libraryName, libraryOf, liveVideoHint, type Place, placeLibrary } from "./model";
import { type UploadSummary, type UploadTarget, useUploads } from "./uploads";

interface Options {
  place?: Place;
  caller: Caller;
  mine?: PhotoLibrary;
  viewingLibrary?: PhotoLibrary;
  toast: ReturnType<typeof useToasts>["toast"];
  setRefreshKey: Dispatch<SetStateAction<number>>;
  setAlbumsKey: Dispatch<SetStateAction<number>>;
}

// usePlaceUploads sends files to the place shown: where they go, the uploads
// themselves, the photos still on their way into the timeline, and a file
// chooser for more.
export function usePlaceUploads({ place, caller, mine, viewingLibrary, toast, setRefreshKey, setAlbumsKey }: Options) {
  // Uploads go to the album or library shown, else to the caller's library.
  const uploadTarget = useMemo((): UploadTarget | undefined => {
    if (!place) return undefined;
    if (place.kind === "album") {
      const library = libraryOf(caller, place.libraryId);
      if (!library || library.viewing) return undefined;
      if (canEditAlbum(caller, place.album))
        return { libraryId: place.libraryId, albumId: place.album.id, label: `相册“${place.album.name}”` };
      return { libraryId: place.libraryId, label: libraryName(caller, library) };
    }
    const library = libraryOf(caller, placeLibrary(place)) ?? (viewingLibrary ? undefined : mine);
    if (!library || library.viewing) return undefined;
    return { libraryId: library.id, label: libraryName(caller, library) };
  }, [place, caller, mine, viewingLibrary]);

  const uploads = useUploads(
    useCallback(
      ({ targets, photos, videos, completed, liveStills }: UploadSummary) => {
        setRefreshKey((value) => value + 1);
        setAlbumsKey((value) => value + 1);
        const kept = photos && videos ? "照片和视频" : videos ? "视频" : "照片";
        const done = [
          photos || videos ? `${kept}已保存到${targets.map((target) => target.label).join("、")}` : "",
          completed ? `已为 ${completed} 张照片补上实况视频` : "",
        ]
          .filter(Boolean)
          .join("；");
        // A Mac often hands over a Live Photo's still alone; say so, and how to
        // send the rest.
        if (liveStills)
          toast({
            text: `${done}；其中 ${liveStills} 张实况照片只收到了静态画面`,
            details: [liveVideoHint],
            detailsLabel: "怎么补上",
          });
        else if (done) toast({ text: done });
      },
      [toast, setRefreshKey, setAlbumsKey],
    ),
  );
  // Uploading photos and videos show in the timeline at once.
  const pending = useMemo(
    () =>
      place?.kind === "timeline"
        ? uploads.items.filter(
            (item) => item.target.libraryId === place.libraryId && !item.target.albumId && !item.rejected,
          )
        : [],
    [uploads.items, place],
  );
  // Previews of finished uploads stay until the timeline shows the photos.
  const { items: uploadItems, prune: pruneUploads } = uploads;
  useEffect(() => {
    if (place?.kind !== "timeline" && uploadItems.some((item) => item.state === "done")) pruneUploads();
  }, [uploadItems, place, pruneUploads]);
  const pickFiles = () => {
    const input = document.createElement("input");
    input.type = "file";
    input.accept =
      "image/jpeg,image/png,image/heic,image/heif,.heic,.heif,video/*,.mov,.mp4,.m4v,.3gp,.mkv,.webm,.avi,.mts,.m2ts";
    input.multiple = true;
    input.onchange = () => {
      if (uploadTarget && input.files?.length) uploads.add(Array.from(input.files), uploadTarget);
    };
    input.click();
  };

  return { uploadTarget, uploads, pending, pickFiles };
}
