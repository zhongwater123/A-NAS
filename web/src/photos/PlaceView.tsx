import {
  Camera,
  CircleAlert,
  Hash,
  Hourglass,
  ImagePlus,
  Images,
  RefreshCw,
  ScanSearch,
  Search,
  SearchX,
  Shapes,
  Trash2,
  UserRound,
} from "lucide-react";
import type { ReactNode } from "react";

import { AIHome } from "./AIHome";
import { AssetGrid } from "./AssetGrid";
import {
  listAlbumPhotos,
  listLabelPhotos,
  listPersonPhotos,
  listPhotoTrash,
  listSimilarPhotos,
  listTagPhotos,
  type PhotoAIStatus,
  type PhotoAlbum,
  type PhotoAsset,
  type PhotoSearchPage,
  type PhotoSimilarPage,
  searchPhotos,
} from "./api";
import { AlbumsView, Empty } from "./Collections";
import type { GridProps } from "./grid";
import { ImageReading, imageSearchPage } from "./imageSearch";
import { type Caller, canEditAlbum, daysLeft, type Place } from "./model";
import styles from "./photos.module.css";
import { joinWords } from "./results";
import type { useSelection } from "./selection";
import { Timeline } from "./Timeline";
import type { SearchBox } from "./Toolbar";
import type { UploadItem, UploadTarget, useUploads } from "./uploads";
import type { PhotoActions } from "./usePhotoActions";
import type { PlaceState } from "./usePlace";

interface Props extends Pick<SearchBox, "dragging" | "imageOver" | "imageDrop" | "imageDragOver" | "pickSearchImage"> {
  place: Place;
  // The panel's usePlace: what the view reports goes back there.
  view: PlaceState;
  grid: GridProps;
  caller: Caller;
  userName: string;
  readOnlyPlace: boolean;
  uploadTarget?: UploadTarget;
  pickFiles: () => void;
  pending: UploadItem[];
  uploads: ReturnType<typeof useUploads>;
  selection: ReturnType<typeof useSelection>;
  refreshKey: number;
  albumsKey: number;
  topInset: number;
  ai?: PhotoAIStatus;
  recent: string[];
  setRecent: (recent: string[]) => void;
  startSearch: (text: string) => void;
  scope: string;
  sourceBadge: (asset: PhotoAsset) => ReactNode;
  newAlbum: PhotoActions["newAlbum"];
  setPeopleSettings: (open: boolean) => void;
  setPhotoPicker: (album: PhotoAlbum) => void;
  reportError: (message: string) => void;
}

// PlaceView fills the window under the toolbar with the place: a grid of its
// photos, AI 聚合's start page or a library's albums, each with what to say
// when there is nothing to show.
export function PlaceView({
  place,
  view,
  grid,
  caller,
  userName,
  readOnlyPlace,
  uploadTarget,
  pickFiles,
  pending,
  uploads,
  selection,
  refreshKey,
  albumsKey,
  topInset,
  ai,
  recent,
  setRecent,
  startSearch,
  scope,
  sourceBadge,
  newAlbum,
  setPeopleSettings,
  setPhotoPicker,
  reportError,
  dragging,
  imageOver,
  imageDrop,
  imageDragOver,
  pickSearchImage,
}: Props) {
  const {
    setPlace,
    setCount,
    setVideoCount,
    aiHomeReturn,
    setAIHomeReturn,
    closest,
    setClosest,
    found,
    setFound,
    setSearchPeople,
    similarReady,
    setSimilarReady,
    imageResult,
    setImageResult,
    imageAttempt,
    setImageAttempt,
  } = view;
  const things = found ? joinWords(found.words) : "";
  const emptyTimeline = readOnlyPlace ? (
    <Empty icon={<Images />} title="还没有照片" text="这个图库里还没有照片。" />
  ) : (
    <Empty
      icon={<ImagePlus />}
      title="把照片拖到这里"
      text="支持 JPEG、PNG、HEIC、安卓动态照片，以及 MP4、MOV 等常见视频。iPhone Live Photo 请选择未修改的照片与同名 MOV。本地 AI 在空闲时分析照片，不分析视频。"
      action={
        uploadTarget && (
          <button type="button" className={`${styles.phButton} primary`} onClick={pickFiles}>
            <ImagePlus />
            选择照片
          </button>
        )
      }
    />
  );
  switch (place.kind) {
    case "timeline":
      return (
        <Timeline
          key={place.libraryId}
          {...grid}
          libraryId={place.libraryId}
          pending={pending}
          onRetryUpload={uploads.retry}
          onReloaded={uploads.prune}
          onCount={(total, videos) => {
            setCount(total);
            setVideoCount(videos ?? 0);
          }}
          onError={reportError}
          empty={emptyTimeline}
        />
      );
    case "album":
      return (
        <AssetGrid
          key={place.album.id}
          {...grid}
          listKey={place.album.id}
          load={(cursor) => listAlbumPhotos(place.album.id, cursor)}
          onCount={setCount}
          onError={reportError}
          empty={
            <Empty
              icon={<Images />}
              title="相册是空的"
              text="把照片从时间线拖到侧栏的这个相册，或者点“添加照片”。"
              action={
                canEditAlbum(caller, place.album) && (
                  <button
                    type="button"
                    className={`${styles.phButton} primary`}
                    onClick={() => setPhotoPicker(place.album)}
                  >
                    <ImagePlus />
                    添加照片
                  </button>
                )
              }
            />
          }
        />
      );
    case "trash":
      return (
        <AssetGrid
          key={`trash:${place.libraryId}`}
          {...grid}
          onOpen={(asset) => selection.toggle(asset, false)}
          onDragIds={undefined}
          listKey={`trash:${place.libraryId}`}
          load={async () => ({ items: await listPhotoTrash(place.libraryId) })}
          onCount={setCount}
          onError={reportError}
          badge={(asset) =>
            asset.trash && <span className={styles.phTileDays}>{daysLeft(asset.trash.purgeAfter)} 天后删除</span>
          }
          empty={<Empty icon={<Trash2 />} title="回收站是空的" text="删除的照片会在这里保留 15 天，期间可以恢复。" />}
        />
      );
    case "search":
      if (!place.query) {
        return (
          <AIHome
            viewing={place.viewing}
            userName={userName}
            snapshot={aiHomeReturn}
            refreshKey={refreshKey}
            topInset={topInset}
            ai={ai}
            recent={recent}
            onSearch={startSearch}
            onForget={() => setRecent([])}
            image={{
              pick: pickSearchImage,
              over: imageOver === "hero",
              dragging,
              drop: { ...imageDrop, onDragOver: imageDragOver("hero") },
            }}
            onOpenCluster={(label, snapshot) => {
              setPlace({ kind: "cluster", viewing: place.viewing, label });
              setAIHomeReturn(snapshot);
            }}
            onOpenTag={(tag, snapshot) => {
              setPlace({ kind: "tag", viewing: place.viewing, tag });
              setAIHomeReturn(snapshot);
            }}
            onOpenPerson={(person, snapshot) => {
              setPlace({ kind: "person", viewing: place.viewing, person });
              setAIHomeReturn(snapshot);
            }}
            onPeopleSettings={() => setPeopleSettings(true)}
            onError={reportError}
          />
        );
      }
      return (
        <AssetGrid
          key={`search:${place.query}:${scope}`}
          {...grid}
          listKey={`search:${place.viewing}:${place.query}:${scope}`}
          load={(cursor) => searchPhotos(place.query, place.viewing, cursor, scope)}
          onPage={(page, first) => {
            if (first && "closest" in page) {
              setClosest((page as PhotoSearchPage).closest);
              setFound((page as PhotoSearchPage).objects);
              setSearchPeople((page as PhotoSearchPage).people);
            }
          }}
          onCount={setCount}
          onError={reportError}
          badge={scope ? undefined : sourceBadge}
          split={
            closest === undefined
              ? undefined
              : found && found.detected > 0
                ? {
                    at: closest,
                    title: `没有检测到${things}的照片`,
                    text:
                      found.undetected > 0
                        ? `按与搜索内容的相似程度排列；其中有些照片还没识别，也可能有${things}`
                        : `按与搜索内容的相似程度排列；很小或被遮住的${things}可能没有识别出来`,
                  }
                : { at: closest, title: "相关度较低", text: "以下照片与搜索的内容差距较大，可能并不相关" }
          }
          empty={
            found ? (
              <Empty
                icon={<SearchX />}
                title={`没有检测到${things}`}
                text={
                  found.undetected > 0
                    ? `还有 ${found.undetected} 张照片尚未识别完，稍后再搜一次试试。`
                    : "图库里的照片都已识别过。换个说法试试，比如“海边的日落”。"
                }
              />
            ) : (
              <Empty icon={<Search />} title="没有找到相关照片" text="换个说法试试，比如“海边的日落”“桌上的蛋糕”。" />
            )
          }
        />
      );
    case "similar":
      return (
        <AssetGrid
          key={`similar:${place.asset.id}`}
          {...grid}
          listKey={`similar:${place.viewing}:${place.asset.id}`}
          load={(cursor) => listSimilarPhotos(place.asset.id, place.viewing, cursor)}
          onPage={(page, first) => {
            if (first) setSimilarReady((page as PhotoSimilarPage).ready);
          }}
          onCount={setCount}
          onError={reportError}
          badge={sourceBadge}
          empty={
            similarReady === false ? (
              <Empty
                icon={<Hourglass />}
                title="这张照片还在整理中"
                text="本地 AI 会在设备空闲时整理照片，整理好后就能找到和它相似的照片。"
              />
            ) : (
              <Empty icon={<ScanSearch />} title="没有找到相似的照片" text="图库里暂时没有和它画面相近的照片。" />
            )
          }
        />
      );
    case "image":
      return (
        <AssetGrid
          key={`image:${place.query.key}:${imageAttempt}`}
          {...grid}
          listKey={`image:${place.viewing}:${place.query.key}`}
          load={(cursor) => imageSearchPage(place.query, place.viewing, cursor)}
          errorDomain="imageSearch"
          onPage={(_, first) => {
            if (first) setImageResult({});
          }}
          onCount={setCount}
          badge={sourceBadge}
          loading={<ImageReading query={place.query} topInset={topInset} />}
          onError={(message) => {
            if (imageResult === undefined) setImageResult({ error: message });
            else reportError(message);
          }}
          empty={
            imageResult?.error ? (
              <Empty
                icon={<CircleAlert />}
                title="没能识别这张图片"
                text={imageResult.error}
                action={
                  <div className={styles.phEmptyActions}>
                    <button
                      type="button"
                      className={styles.phButton}
                      onClick={() => {
                        setImageResult(undefined);
                        setImageAttempt((value) => value + 1);
                      }}
                    >
                      <RefreshCw />
                      重试
                    </button>
                    <button type="button" className={`${styles.phButton} primary`} onClick={pickSearchImage}>
                      <Camera />
                      换一张
                    </button>
                  </div>
                }
              />
            ) : ai && ai.ready === 0 && ai.pending > 0 ? (
              <Empty
                icon={<Hourglass />}
                title="照片还在整理中"
                text="本地 AI 会在设备空闲时整理照片，整理好的照片才能用图片找到。"
              />
            ) : (
              <Empty icon={<ScanSearch />} title="没有找到相似的照片" text="图库里暂时没有和这张图片画面相近的照片。" />
            )
          }
        />
      );
    case "cluster":
      return (
        <AssetGrid
          key={`cluster:${place.label.id}`}
          {...grid}
          listKey={`cluster:${place.viewing}:${place.label.id}`}
          load={(cursor) => listLabelPhotos(place.label.id, place.viewing, cursor)}
          onCount={setCount}
          onError={reportError}
          badge={sourceBadge}
          empty={
            <Empty icon={<Shapes />} title="这一类里没有照片了" text="被移出的照片仍在图库中，只是不再归在这一类。" />
          }
        />
      );
    case "person":
      return (
        <AssetGrid
          key={`person:${place.person.id}`}
          {...grid}
          listKey={`person:${place.person.id}`}
          load={(cursor) => listPersonPhotos(place.person.id, cursor)}
          onCount={setCount}
          onError={reportError}
          empty={
            <Empty
              icon={<UserRound />}
              title="这里没有照片了"
              text="被移出的照片仍在图库中，只是不再归在这个人名下。"
            />
          }
        />
      );
    case "tag":
      return (
        <AssetGrid
          key={`tag:${place.tag.name}`}
          {...grid}
          listKey={`tag:${place.viewing}:${place.tag.name}`}
          load={(cursor) => listTagPhotos(place.tag.name, place.viewing, cursor)}
          onCount={setCount}
          onError={reportError}
          badge={sourceBadge}
          empty={
            <Empty icon={<Hash />} title="没有带这个标签的照片了" text="在照片信息里给照片加上标签，就会出现在这里。" />
          }
        />
      );
    case "albums":
      return (
        <AlbumsView
          libraryId={place.libraryId}
          readOnly={readOnlyPlace}
          refreshKey={albumsKey}
          topInset={topInset}
          onOpen={(album) => setPlace({ kind: "album", libraryId: place.libraryId, album })}
          onCreate={() => void newAlbum()}
          onError={reportError}
        />
      );
  }
}
