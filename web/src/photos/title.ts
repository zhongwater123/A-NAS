import type { PhotoLibrary, PhotoObjectsFound } from "./api";
import type { ImageResult } from "./imageSearch";
import { type Caller, countLabel, libraryName, libraryOf, type Place } from "./model";
import { personName } from "./People";
import { joinWords } from "./results";
import { scopeText } from "./scope";

interface Reports {
  caller: Caller;
  sideLibrary?: PhotoLibrary;
  count?: number;
  videoCount: number;
  closest?: number;
  found?: PhotoObjectsFound;
  searchPeople?: string[];
  scope: string;
  imageResult: ImageResult;
}

// placeTitle names the place shown, says what its view found and where the
// back button goes, for the toolbar and the window's main landmark.
export function placeTitle(
  place: Place,
  { caller, sideLibrary, count, videoCount, closest, found, searchPeople, scope, imageResult }: Reports,
) {
  // Search results name their closest group while less related ones follow;
  // when the detector found the query's things, the status under the search
  // box names that group instead.
  const searchDetail =
    (searchPeople?.length ? `${joinWords(searchPeople)}的照片 · ` : "") +
    (closest === undefined
      ? ""
      : found && found.detected > 0
        ? count === undefined
          ? ""
          : `共 ${count} 张`
        : count !== undefined && closest >= count
          ? `找到 ${count} 张`
          : `最接近 ${closest} 张${count === undefined ? "" : ` · 共 ${count} 张`}`);
  switch (place.kind) {
    case "timeline":
      return {
        text: libraryName(caller, sideLibrary),
        detail: count === undefined ? "" : countLabel(count, videoCount, " · "),
      };
    case "albums":
      return { text: "相册", detail: libraryName(caller, sideLibrary) };
    case "album":
      return {
        text: place.album.name,
        detail: `${count ?? place.album.photos} 张 · ${libraryName(caller, sideLibrary)}`,
        back: { kind: "albums", libraryId: place.libraryId } as Place,
      };
    case "trash":
      return { text: "回收站", detail: "照片保留 15 天后自动永久删除" };
    case "search":
      return place.query
        ? {
            text: "AI 聚合",
            detail: [searchDetail, scopeText(caller, place.viewing, scope)].filter(Boolean).join(" · "),
            back: { ...place, query: "" } as Place,
          }
        : { text: "AI 聚合", detail: scopeText(caller, place.viewing, "") };
    case "cluster":
      return {
        text: place.label.name,
        detail: `${count ?? place.label.photos} 张 · 本地 AI 聚合`,
        back: { kind: "search", viewing: place.viewing, query: "" } as Place,
      };
    case "tag": {
      const photos = count ?? (place.tag.photos || undefined);
      return {
        text: place.tag.name,
        detail: photos === undefined ? "你的标签" : `${photos} 张 · 你的标签`,
        back: { kind: "search", viewing: place.viewing, query: "" } as Place,
      };
    }
    case "person":
      return {
        text: personName(place.person),
        detail: `${count ?? place.person.photos} 张${place.person.hidden ? " · 已隐藏" : ""} · ${libraryName(caller, libraryOf(caller, place.person.libraryId))}`,
        back: { kind: "search", viewing: place.viewing, query: "" } as Place,
      };
    case "similar":
      return { text: "相似的照片", detail: count === undefined ? "" : `${count} 张`, back: place.from };
    case "image":
      return {
        text: "以图搜图",
        detail: count === undefined || imageResult?.error ? "" : `${count} 张`,
        back: place.from,
      };
  }
}

export type PlaceTitle = ReturnType<typeof placeTitle>;
