import {
  type Dispatch,
  type RefObject,
  type SetStateAction,
  useCallback,
  useEffect,
  useEffectEvent,
  useRef,
  useState,
} from "react";

import type { useDialogs, useToasts } from "../ui/feedback";
import {
  attachLiveVideo,
  getPhoto,
  listAlbums,
  listPeople,
  type PhotoAlbum,
  type PhotoAsset,
  renamePhoto,
} from "./api";
import { type Caller, messageOf, type Place, placeViewing } from "./model";
import type { PhotoActions } from "./usePhotoActions";
import type { ViewerActions } from "./Viewer";

interface Options {
  /** A photo to show on its own, asked for by another window; onIntentHandled hands it back once shown. */
  intent?: { assetId: string };
  onIntentHandled?(handled: { assetId: string }): void;
  viewAssets: PhotoAsset[];
  place?: Place;
  setPlace: (place: Place) => void;
  caller: Caller;
  actions: PhotoActions;
  dialogs: ReturnType<typeof useDialogs>;
  toast: ReturnType<typeof useToasts>["toast"];
  reportError: (message: string) => void;
  setRefreshKey: Dispatch<SetStateAction<number>>;
  root: RefObject<HTMLDivElement | null>;
}

// useViewer is the photo open over the window: one of the view's photos, the
// one another window asked to show, or a slideshow. viewerActions is what the
// viewer does with them, mostly the window's own actions.
export function useViewer({
  intent,
  onIntentHandled,
  viewAssets,
  place,
  setPlace,
  caller,
  actions,
  dialogs,
  toast,
  reportError,
  setRefreshKey,
  root,
}: Options) {
  const { trash, download, copyTo, setPicker } = actions;
  const [viewer, setViewer] = useState<{ id: string; origin?: DOMRect }>();
  // One photo another window asked to show, viewed on its own over the current place.
  const [focused, setFocused] = useState<PhotoAsset>();
  // The photo a full-screen slideshow started from.
  const [slideshow, setSlideshow] = useState<string>();
  const renamed = useRef(false);

  const openPersonById = async (personId: string) => {
    if (!place) return;
    const viewing = placeViewing(caller, place);
    const person = (await listPeople(viewing, true).catch(() => [])).find((item) => item.id === personId);
    setViewer(undefined);
    if (person) setPlace({ kind: "person", viewing, person });
  };
  const renameOne = async (asset: PhotoAsset) => {
    const name = await dialogs.prompt({
      title: "重命名照片",
      label: "名称",
      value: asset.name,
      confirm: "重命名",
      maxLength: 255,
    });
    if (!name || name === asset.name) return undefined;
    try {
      const value = await renamePhoto(asset.id, name);
      renamed.current = true;
      return value;
    } catch (caught) {
      reportError(messageOf(caught));
      return undefined;
    }
  };
  const openAlbumById = async (album: Pick<PhotoAlbum, "id" | "name" | "libraryId">) => {
    const found = (await listAlbums(album.libraryId).catch(() => [])).find((item) => item.id === album.id);
    setViewer(undefined);
    setPlace({
      kind: "album",
      libraryId: album.libraryId,
      album: found ?? { ...album, createdBy: "", createdAt: "", photos: 0 },
    });
  };

  const openViewer = useCallback(
    (asset: PhotoAsset, image: HTMLImageElement | null) =>
      setViewer({ id: asset.id, origin: image?.getBoundingClientRect() }),
    [],
  );

  const viewerIndex = viewer ? viewAssets.findIndex((asset) => asset.id === viewer.id) : -1;
  const viewerActions: ViewerActions = {
    trash: (asset) => {
      const position = viewAssets.findIndex((item) => item.id === asset.id);
      const next = viewAssets[position + 1] ?? viewAssets[position - 1];
      setViewer(next ? { id: next.id } : undefined);
      void trash([asset]);
    },
    addToAlbum: (assets) => setPicker(assets),
    download: (assets) => void download(assets),
    rename: renameOne,
    copy: (asset, library) => void copyTo(library, [asset]),
    openAlbum: (album) => void openAlbumById(album),
    openTag: (tag) => {
      setViewer(undefined);
      if (place)
        setPlace({ kind: "tag", viewing: placeViewing(caller, place), tag: { name: tag, photos: 0, coverId: "" } });
    },
    openPerson: (personId) => void openPersonById(personId),
    viewing: place ? placeViewing(caller, place) : "",
    findSimilar: (asset) => {
      setViewer(undefined);
      if (place) setPlace({ kind: "similar", viewing: placeViewing(caller, place), asset, from: place });
    },
    slideshow: (asset) => setSlideshow(asset.id),
    attachLiveVideo: async (asset, file) => {
      try {
        const completed = await attachLiveVideo(asset.libraryId, file);
        setRefreshKey((value) => value + 1);
        toast({ text: completed.length > 1 ? `已为 ${completed.length} 张照片补上实况视频` : "已补上实况视频" });
      } catch (caught) {
        reportError(messageOf(caught));
      }
    },
    error: reportError,
  };
  // Runs once per intent; the callbacks it uses do not decide what to show.
  // An intent replaced before its photo arrived is left to its successor,
  // which hands back its own.
  const focusFailed = useEffectEvent(reportError);
  const intentHandled = useEffectEvent((handled: { assetId: string }) => onIntentHandled?.(handled));
  useEffect(() => {
    if (!intent) return;
    let current = true;
    getPhoto(intent.assetId)
      .then((asset) => {
        if (current) {
          setViewer(undefined);
          setFocused(asset);
        }
      })
      .catch((reason) => {
        if (current) focusFailed(messageOf(reason));
      })
      .finally(() => {
        if (current) intentHandled(intent);
      });
    return () => {
      current = false;
    };
  }, [intent]);
  const focusedActions: ViewerActions = {
    ...viewerActions,
    trash: (asset) => {
      setFocused(undefined);
      void trash([asset]);
    },
    openTag: (tag) => {
      setFocused(undefined);
      viewerActions.openTag(tag);
    },
    findSimilar: (asset) => {
      setFocused(undefined);
      viewerActions.findSimilar(asset);
    },
  };
  const closeViewer = useCallback(() => {
    setViewer(undefined);
    if (renamed.current) {
      renamed.current = false;
      setRefreshKey((value) => value + 1);
    }
    root.current?.focus();
  }, [root, setRefreshKey]);

  return {
    viewer,
    setViewer,
    focused,
    setFocused,
    slideshow,
    setSlideshow,
    openViewer,
    viewerIndex,
    viewerActions,
    focusedActions,
    closeViewer,
  };
}
