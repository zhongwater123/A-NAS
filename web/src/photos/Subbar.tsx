import { Hourglass, Search } from "lucide-react";

import type { PhotoAIStatus, PhotoAsset, PhotoObjectsFound } from "./api";
import { type ImageResult, ImageSearchButton, ImageSource } from "./imageSearch";
import type { Caller, Place } from "./model";
import styles from "./photos.module.css";
import { ObjectsStatus, SimilarSource } from "./results";
import { SearchScope } from "./scope";
import type { SearchBox } from "./Toolbar";

interface Props extends SearchBox {
  place: Place;
  caller: Caller;
  setPlace: (place: Place) => void;
  scope: string;
  setSearchLibrary: (library: string) => void;
  found?: PhotoObjectsFound;
  viewAssets: PhotoAsset[];
  imageResult: ImageResult;
  ai?: PhotoAIStatus;
}

// Subbar is the bar under the toolbar on the places that find photos. On AI
// search the search box sits under the toolbar; similar photos and a search
// by image show there what they were asked for.
export function Subbar({
  place,
  caller,
  setPlace,
  scope,
  setSearchLibrary,
  found,
  viewAssets,
  imageResult,
  ai,
  query,
  setQuery,
  submitSearch,
  searchInput,
  dragging,
  imageOver,
  imageDrop,
  imageDragOver,
  pickSearchImage,
}: Props) {
  return place.kind === "search" && place.query ? (
    <div className={`${styles.phSubbar} ${styles.phSearchBar}`}>
      <form
        className={`${styles.phSearch} ${styles.wide}${imageOver === "results" ? ` ${styles.imageDrop}` : ""}`}
        role="search"
        onSubmit={submitSearch}
        {...{ ...imageDrop, onDragOver: imageDragOver("results") }}
      >
        <Search />
        <input
          ref={searchInput}
          type="search"
          aria-label="搜索照片"
          placeholder={dragging ? "拖到这里，以图搜图" : "例如：海边的猫"}
          maxLength={200}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Escape") {
              event.preventDefault();
              setPlace({ ...place, query: "" });
            }
          }}
        />
        <ImageSearchButton onPick={pickSearchImage} />
      </form>
      <SearchScope caller={caller} viewing={place.viewing} value={scope} onChange={setSearchLibrary} />
      {found && <ObjectsStatus objects={found} results={viewAssets.length > 0} />}
    </div>
  ) : place.kind === "similar" ? (
    <div className={`${styles.phSubbar} ${styles.phSimilarBar}`}>
      <SimilarSource asset={place.asset} />
    </div>
  ) : place.kind === "image" ? (
    <div className={`${styles.phSubbar} ${styles.phSimilarBar}`}>
      <ImageSource
        query={place.query}
        result={imageResult}
        over={imageOver === "source"}
        drop={{ ...imageDrop, onDragOver: imageDragOver("source") }}
        onReplace={pickSearchImage}
      />
      {ai && ai.pending > 0 && imageResult && !imageResult.error && (
        <span
          className={`${styles.phFoundNote} ${styles.pending}`}
          title="本地 AI 会在设备空闲时继续整理，结果可能还会增加"
        >
          <Hourglass />
          还有 {ai.pending} 张照片尚未整理完
        </span>
      )}
    </div>
  ) : null;
}
