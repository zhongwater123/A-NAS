import { type Dispatch, type SetStateAction, useMemo, useState } from "react";

import { endViewing } from "../settings/api";
import type { useDialogs, useToasts } from "../ui/feedback";
import {
  addPhotoTag,
  addToAlbum,
  copyPhoto,
  createAlbum,
  deleteAlbum,
  downloadURL,
  emptyPhotoTrash,
  hideAILabel,
  mergePeople,
  type PhotoAlbum,
  type PhotoAsset,
  type PhotoLibrary,
  type PhotoPerson,
  purgePhoto,
  removeFromAlbum,
  removeFromPerson,
  removePhotoTag,
  renameAlbum,
  restorePhoto,
  setLibraryPeople,
  trashPhoto,
  updatePerson,
} from "./api";
import { assetsLabel, type Caller, libraryName, libraryOf, messageOf, type Place, runEach } from "./model";
import { personName } from "./People";
import type { useSelection } from "./selection";

const sleep = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));

interface Options {
  place?: Place;
  setPlace: (place: Place) => void;
  setPlaceState: Dispatch<SetStateAction<Place | undefined>>;
  caller: Caller;
  mine?: PhotoLibrary;
  sideLibrary?: PhotoLibrary;
  viewingLibrary?: PhotoLibrary;
  selection: ReturnType<typeof useSelection>;
  viewAssets: PhotoAsset[];
  setHidden: Dispatch<SetStateAction<ReadonlySet<string>>>;
  setAlbumsKey: Dispatch<SetStateAction<number>>;
  setRefreshKey: Dispatch<SetStateAction<number>>;
  setLibraries: Dispatch<SetStateAction<PhotoLibrary[] | undefined>>;
  loadLibraries: () => Promise<void>;
  dialogs: ReturnType<typeof useDialogs>;
  toast: ReturnType<typeof useToasts>["toast"];
  reportError: (message: string) => void;
}

// usePhotoActions is what the window does to photos and to the album, person,
// tag or trash shown, with the dialogs that choose what to do it with. An
// action on several photos takes them one at a time, shows how far it is in
// busy, says in a toast how it went and takes the photos it moved out of the
// view.
export function usePhotoActions({
  place,
  setPlace,
  setPlaceState,
  caller,
  mine,
  sideLibrary,
  viewingLibrary,
  selection,
  viewAssets,
  setHidden,
  setAlbumsKey,
  setRefreshKey,
  setLibraries,
  loadLibraries,
  dialogs,
  toast,
  reportError,
}: Options) {
  const [picker, setPicker] = useState<PhotoAsset[]>();
  const [photoPicker, setPhotoPicker] = useState<PhotoAlbum>();
  const [busy, setBusy] = useState<{ label: string; done: number; total: number }>();
  const byId = useMemo(() => new Map(viewAssets.map((asset) => [asset.id, asset])), [viewAssets]);
  const chosen = useMemo(
    () => [...selection.selected].map((id) => byId.get(id)).filter((asset): asset is PhotoAsset => Boolean(asset)),
    [selection.selected, byId],
  );
  const hide = (ids: string[]) => setHidden((current) => new Set([...current, ...ids]));
  const unhide = (ids: string[]) => setHidden((current) => new Set([...current].filter((id) => !ids.includes(id))));

  const batch = async <R>(label: string, assets: PhotoAsset[], action: (asset: PhotoAsset) => Promise<R>) => {
    setBusy({ label, done: 0, total: assets.length });
    const result = await runEach(assets, action, (done) => setBusy({ label, done, total: assets.length }));
    setBusy(undefined);
    return { ...result, details: result.failed.map((failure) => `${failure.item.name}：${messageOf(failure.error)}`) };
  };
  const report = (text: string, failed: string[], extra?: { action?: { label: string; run: () => void } }) => {
    if (failed.length) toast({ text: `${text}；${failed.length} 张未成功`, tone: "error", details: failed, ...extra });
    else toast({ text, ...extra });
  };

  const restore = async (assets: PhotoAsset[], fromTrash = false) => {
    const result = await batch("正在恢复", assets, (asset) => restorePhoto(asset.id));
    const ids = result.succeeded.map((entry) => entry.item.id);
    if (fromTrash) hide(ids);
    else unhide(ids);
    report(`已恢复 ${assetsLabel(result.succeeded.map((entry) => entry.item))}`, result.details);
  };
  const trash = async (assets: PhotoAsset[]) => {
    const result = await batch("正在移到回收站", assets, (asset) => trashPhoto(asset.id));
    const moved = result.succeeded.map((entry) => entry.item);
    hide(moved.map((asset) => asset.id));
    selection.clear();
    setAlbumsKey((value) => value + 1);
    report(
      `已移到回收站：${moved.length} 张`,
      result.details,
      moved.length ? { action: { label: "撤销", run: () => void restore(moved) } } : undefined,
    );
  };
  const addTo = async (album: PhotoAlbum, assets: PhotoAsset[]) => {
    setPicker(undefined);
    const result = await batch(`正在加入“${album.name}”`, assets, (asset) => addToAlbum(album.id, asset.id));
    const copied = result.succeeded.filter((entry) => entry.item.libraryId !== album.libraryId).length;
    selection.clear();
    setAlbumsKey((value) => value + 1);
    if (place?.kind === "album" && place.album.id === album.id) setRefreshKey((value) => value + 1);
    report(
      `已加入相册“${album.name}”${copied ? `，其中 ${copied} 张已复制到${libraryName(caller, libraryOf(caller, album.libraryId))}` : ""}`,
      result.details,
      place?.kind === "album" && place.album.id === album.id
        ? undefined
        : { action: { label: "查看", run: () => setPlace({ kind: "album", libraryId: album.libraryId, album }) } },
    );
  };
  const createAndAdd = async (library: PhotoLibrary, name: string, assets: PhotoAsset[]) => {
    try {
      await addTo(await createAlbum(library.id, name), assets);
    } catch (caught) {
      setPicker(undefined);
      reportError(messageOf(caught));
    }
  };
  const copyTo = async (library: PhotoLibrary, assets: PhotoAsset[]) => {
    const result = await batch(`正在复制到${libraryName(caller, library)}`, assets, (asset) =>
      copyPhoto(asset.id, library.id),
    );
    selection.clear();
    report(`已复制 ${result.succeeded.length} 张到${libraryName(caller, library)}`, result.details, {
      action: { label: "查看", run: () => setPlace({ kind: "timeline", libraryId: library.id }) },
    });
  };
  const removeFromCurrentAlbum = async (assets: PhotoAsset[]) => {
    if (place?.kind !== "album") return;
    const album = place.album;
    const result = await batch("正在移出相册", assets, (asset) => removeFromAlbum(album.id, asset.id));
    const removed = result.succeeded.map((entry) => entry.item);
    hide(removed.map((asset) => asset.id));
    selection.clear();
    setAlbumsKey((value) => value + 1);
    report(`已从“${album.name}”移出 ${removed.length} 张，照片仍在图库中`, result.details, {
      action: {
        label: "撤销",
        run: () =>
          void runEach(removed, (asset) => addToAlbum(album.id, asset.id)).then(() => {
            unhide(removed.map((asset) => asset.id));
            setAlbumsKey((value) => value + 1);
          }),
      },
    });
  };
  // A wrong photo leaves the AI cluster for good; it stays in the library.
  const takeOutOfCluster = async (assets: PhotoAsset[]) => {
    if (place?.kind !== "cluster") return;
    const label = place.label;
    const result = await batch("正在移出", assets, (asset) => hideAILabel(asset.id, label.id));
    hide(result.succeeded.map((entry) => entry.item.id));
    selection.clear();
    report(`已从“${label.name}”移出 ${result.succeeded.length} 张，照片仍在图库中`, result.details);
  };
  // A photo taken out of a person stays out: local AI never puts it back.
  const takeOutOfPerson = async (assets: PhotoAsset[]) => {
    if (place?.kind !== "person") return;
    const person = place.person;
    const result = await batch("正在移出", assets, async (asset) => {
      await removeFromPerson(person.id, asset.id);
      return asset;
    });
    hide(result.succeeded.map((entry) => entry.item.id));
    selection.clear();
    report(`已从“${personName(person)}”移出 ${result.succeeded.length} 张，照片仍在图库中`, result.details);
  };
  const [merging, setMerging] = useState<PhotoPerson>();
  // Changing a person shows the change at once and keeps the place.
  const changePerson = async (change: { name?: string; hidden?: boolean }) => {
    if (place?.kind !== "person") return;
    try {
      setPlaceState({ ...place, person: await updatePerson(place.person.id, change) });
    } catch (caught) {
      reportError(messageOf(caught));
    }
  };
  const namePerson = async () => {
    if (place?.kind !== "person") return;
    const person = place.person;
    const name = await dialogs.prompt({
      title: person.name ? `给“${person.name}”改名` : person.kind === "person" ? "这是谁？" : "它叫什么？",
      label: "名字",
      value: person.name,
      confirm: "保存",
      maxLength: 40,
    });
    if (name === undefined || name === person.name) return;
    await changePerson({ name });
    if (name) toast({ text: `搜索“${name}”就能找到 TA 的照片` });
  };
  const mergeInto = async (into: PhotoPerson) => {
    if (place?.kind !== "person") return;
    const from = place.person;
    setMerging(undefined);
    try {
      const merged = await mergePeople(from.id, into.id);
      setPlace({ kind: "person", viewing: place.viewing, person: merged });
      toast({ text: `已把${from.name ? `“${from.name}”` : "这些照片"}合并到“${personName(merged)}”` });
    } catch (caught) {
      reportError(messageOf(caught));
    }
  };
  const [peopleSettings, setPeopleSettings] = useState(false);
  // Off clears what was recognised, so it asks first; on starts at once.
  const setPeople = async (library: PhotoLibrary, on: boolean) => {
    const name = libraryName(caller, library);
    if (
      !on &&
      !(await dialogs.confirm({
        title: `关闭${name}的人物与宠物？`,
        message:
          "这个图库里识别出的人脸与宠物会被删除，本地 AI 也不再识别新照片。起的名字和做过的更正会保留，重新开启后随识别结果回来。",
        confirm: "关闭并删除",
        danger: true,
      }))
    )
      return;
    try {
      const changed = await setLibraryPeople(library.id, on);
      setLibraries((items) =>
        items?.map((item) =>
          item.id === changed.id ? { ...item, people: changed.people, canSetPeople: changed.canSetPeople } : item,
        ),
      );
      setRefreshKey((key) => key + 1);
      toast({ text: on ? `已开启${name}的人物与宠物，本地 AI 会在空闲时重新识别` : `已关闭${name}的人物与宠物` });
    } catch (caught) {
      reportError(messageOf(caught));
    }
  };
  // Taking a tag off leaves the photos in the library and in their albums.
  const untag = async (assets: PhotoAsset[]) => {
    if (place?.kind !== "tag") return;
    const name = place.tag.name;
    const result = await batch("正在去掉标签", assets, (asset) => removePhotoTag(asset.id, name));
    const untagged = result.succeeded.map((entry) => entry.item);
    hide(untagged.map((asset) => asset.id));
    selection.clear();
    report(`已从 ${untagged.length} 张照片上去掉“${name}”`, result.details, {
      action: {
        label: "撤销",
        run: () =>
          void runEach(untagged, (asset) => addPhotoTag(asset.id, name)).then(() =>
            unhide(untagged.map((asset) => asset.id)),
          ),
      },
    });
  };
  const purge = async (assets: PhotoAsset[]) => {
    if (
      !(await dialogs.confirm({
        title: `永久删除 ${assetsLabel(assets)}？`,
        message: "永久删除后无法恢复，照片会从所有相册中移除。",
        confirm: "永久删除",
        danger: true,
      }))
    )
      return;
    const result = await batch("正在永久删除", assets, (asset) => purgePhoto(asset.id));
    hide(result.succeeded.map((entry) => entry.item.id));
    selection.clear();
    report(`已永久删除 ${result.succeeded.length} 张`, result.details);
  };
  const download = async (assets: PhotoAsset[]) => {
    if (
      assets.length > 10 &&
      !(await dialogs.confirm({
        title: `下载 ${assets.length} 张原图？`,
        message: "浏览器会逐张下载，并可能询问是否允许下载多个文件。",
        confirm: "下载",
      }))
    )
      return;
    for (const [position, asset] of assets.entries()) {
      const link = document.createElement("a");
      link.href = downloadURL(asset.id);
      link.download = asset.motion?.kind === "live-photo" ? asset.name.replace(/\.[^.]+$/, "") + ".zip" : asset.name;
      document.body.appendChild(link);
      link.click();
      link.remove();
      if (position < assets.length - 1) await sleep(350);
    }
  };
  const newAlbum = async (library = sideLibrary) => {
    if (!library || library.viewing) return;
    const name = await dialogs.prompt({
      title: `在${libraryName(caller, library)}新建相册`,
      label: "相册名称",
      confirm: "创建",
      maxLength: 100,
    });
    if (!name) return;
    try {
      const album = await createAlbum(library.id, name);
      setAlbumsKey((value) => value + 1);
      setPlace({ kind: "album", libraryId: library.id, album });
    } catch (caught) {
      reportError(messageOf(caught));
    }
  };
  const renameCurrentAlbum = async () => {
    if (place?.kind !== "album") return;
    const name = await dialogs.prompt({
      title: "重命名相册",
      label: "相册名称",
      value: place.album.name,
      confirm: "重命名",
      maxLength: 100,
    });
    if (!name || name === place.album.name) return;
    try {
      const album = await renameAlbum(place.album.id, name);
      setPlaceState({ ...place, album });
      setAlbumsKey((value) => value + 1);
    } catch (caught) {
      reportError(messageOf(caught));
    }
  };
  const deleteCurrentAlbum = async () => {
    if (place?.kind !== "album") return;
    if (
      !(await dialogs.confirm({
        title: `删除相册“${place.album.name}”？`,
        message: "只删除这个分组，其中的照片会留在图库和其他相册里。",
        confirm: "删除相册",
        danger: true,
      }))
    )
      return;
    try {
      await deleteAlbum(place.album.id);
      setAlbumsKey((value) => value + 1);
      setPlace({ kind: "albums", libraryId: place.libraryId });
      toast({ text: `已删除相册“${place.album.name}”` });
    } catch (caught) {
      reportError(messageOf(caught));
    }
  };
  const emptyTrash = async () => {
    if (place?.kind !== "trash") return;
    if (
      !(await dialogs.confirm({
        title: "清空回收站？",
        message: "回收站中你可以清除的照片都会被永久删除，无法恢复。",
        confirm: "清空回收站",
        danger: true,
      }))
    )
      return;
    try {
      const purged = await emptyPhotoTrash(place.libraryId);
      setRefreshKey((value) => value + 1);
      toast({ text: `已永久删除 ${purged} 张照片` });
    } catch (caught) {
      reportError(messageOf(caught));
    }
  };
  const stopViewing = async () => {
    if (!viewingLibrary?.viewing) return;
    try {
      await endViewing(viewingLibrary.viewing.grantId);
      await loadLibraries();
      if (mine) setPlace({ kind: "timeline", libraryId: mine.id });
    } catch (caught) {
      reportError(messageOf(caught));
    }
  };

  // Photos dragged onto the sidebar.
  const assetsFor = (ids: string[]) =>
    ids.map((id) => byId.get(id)).filter((asset): asset is PhotoAsset => Boolean(asset));
  const dropOnAlbum = (album: PhotoAlbum, ids: string[]) => {
    const assets = assetsFor(ids);
    if (assets.length) void addTo(album, assets);
  };
  const dropOnLibrary = (library: PhotoLibrary, ids: string[]) => {
    const assets = assetsFor(ids).filter((asset) => asset.libraryId !== library.id);
    if (assets.length) void copyTo(library, assets);
  };

  return {
    chosen,
    busy,
    picker,
    setPicker,
    photoPicker,
    setPhotoPicker,
    merging,
    setMerging,
    peopleSettings,
    setPeopleSettings,
    restore,
    trash,
    addTo,
    createAndAdd,
    copyTo,
    removeFromCurrentAlbum,
    takeOutOfCluster,
    takeOutOfPerson,
    changePerson,
    namePerson,
    mergeInto,
    setPeople,
    untag,
    purge,
    download,
    newAlbum,
    renameCurrentAlbum,
    deleteCurrentAlbum,
    emptyTrash,
    stopViewing,
    dropOnAlbum,
    dropOnLibrary,
  };
}

export type PhotoActions = ReturnType<typeof usePhotoActions>;
