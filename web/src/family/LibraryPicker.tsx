import { CircleAlert } from "lucide-react";
import { Tabs } from "radix-ui";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { listPhotoLibraries, type PhotoAsset, type PhotoLibrary } from "../photos/api";
import { Empty } from "../photos/Collections";
import { clampRowHeight } from "../photos/layout";
import type { ScrollerHandle } from "../photos/PhotoScroller";
import { useSelection } from "../photos/selection";
import { Timeline } from "../photos/Timeline";
import { familyError } from "./api";
import { useFamily } from "./context";
import styles from "./family.module.css";
import { inAlbum, needsCopy, sharedIdOf } from "./fromLibrary";
import { Modal } from "./parts";

const noHidden: ReadonlySet<string> = new Set();
const noPending: never[] = [];
const nothing = () => undefined;

// LibraryPicker chooses photos for a record from the photo library: the
// shared library or the member's own, by month and day as in 相册. Photos the
// record cannot take say why and stay unchosen: those the baby's album has
// already, when babyId is set, and videos, for images only.
export function LibraryPicker({
  babyId,
  imagesOnly,
  room,
  onPick,
  onClose,
}: {
  babyId?: string;
  imagesOnly: boolean;
  // room is how many more photos the record takes.
  room: number;
  onPick: (assets: PhotoAsset[]) => void;
  onClose: () => void;
}) {
  const { userId } = useFamily();
  const [libraries, setLibraries] = useState<{ shared?: PhotoLibrary; own?: PhotoLibrary }>();
  const [tab, setTab] = useState("");
  const [failure, setFailure] = useState("");
  const [assets, setAssets] = useState<PhotoAsset[]>([]);
  // Every photo seen in either library, so a choice survives switching tabs.
  const [known, setKnown] = useState<ReadonlyMap<string, PhotoAsset>>(new Map());
  const [inChannel, setInChannel] = useState<ReadonlySet<string>>(new Set());
  const asked = useRef(new Set<string>());
  const [rowHeight, setRowHeight] = useState(120);
  const scroller = useRef<ScrollerHandle>(null);
  const selection = useSelection(assets);

  useEffect(() => {
    let live = true;
    listPhotoLibraries().then(
      (items) => {
        if (!live) return;
        const shared = items.find((item) => item.kind === "shared");
        const own = items.find((item) => item.kind === "private" && item.ownerUserId === userId && !item.viewing);
        setLibraries({ shared, own });
        setTab(shared?.id ?? own?.id ?? "");
      },
      (caught: unknown) => {
        if (live) setFailure(familyError(caught));
      },
    );
    return () => {
      live = false;
    };
  }, [userId]);

  const sharedId = libraries?.shared?.id ?? "";
  // Photos newly in view are checked against the album once.
  useEffect(() => {
    if (!babyId || !sharedId) return;
    const ids = assets
      .map((asset) => sharedIdOf(asset, sharedId))
      .filter((id): id is string => Boolean(id) && !asked.current.has(id!));
    if (ids.length === 0) return;
    for (const id of ids) asked.current.add(id);
    inAlbum(babyId, ids).then(
      (found) => {
        if (found.size) setInChannel((current) => new Set([...current, ...found]));
      },
      // Saving leaves them out anyway; the marks are a courtesy.
      nothing,
    );
  }, [assets, babyId, sharedId]);

  const reason = useCallback(
    (asset: PhotoAsset | undefined): string | undefined => {
      if (!asset) return undefined;
      if (imagesOnly && asset.video) return "视频";
      const id = sharedIdOf(asset, sharedId);
      return id && inChannel.has(id) ? "已在频道中" : undefined;
    },
    [imagesOnly, inChannel, sharedId],
  );
  const badge = useCallback(
    (asset: PhotoAsset) => {
      const why = reason(asset);
      return why ? (
        <span className={styles.fcLibraryBlocked}>
          <span>{why}</span>
        </span>
      ) : null;
    },
    [reason],
  );
  const allowed = (ids: string[]) => ids.filter((id) => !reason(known.get(id)));
  const chosen = useMemo(
    () =>
      [...selection.selected]
        .map((id) => known.get(id))
        .filter((asset): asset is PhotoAsset => Boolean(asset) && !reason(asset)),
    [selection.selected, known, reason],
  );
  const shown = useMemo(() => new Set(chosen.map((asset) => asset.id)), [chosen]);
  const toggle = (asset: PhotoAsset, range: boolean) => {
    if (!reason(asset)) selection.toggle(asset, range);
  };
  const copied = needsCopy(chosen, sharedId);
  const over = chosen.length > room;

  const name = (library: PhotoLibrary) => (library.kind === "shared" ? "共享图库" : "我的图库");
  const tabs = [libraries?.shared, libraries?.own].filter((library): library is PhotoLibrary => Boolean(library));

  return (
    <Modal
      title="从相册选择"
      large
      onClose={onClose}
      footer={
        <>
          <span className={styles.fcLibraryNote}>
            {over
              ? `这条记录最多还能放 ${room} 张`
              : chosen.length
                ? `已选 ${chosen.length} 张${copied ? `，其中 ${copied} 张会复制到共享图库，家人都能看到` : ""}`
                : "点选照片，可以按住勾选圈拖动或框选"}
          </span>
          <span className={styles.fcGrow} />
          <button type="button" className={styles.fcSecondary} onClick={onClose}>
            取消
          </button>
          <button
            type="button"
            className={styles.fcPrimary}
            disabled={!chosen.length || over}
            onClick={() => onPick(chosen)}
          >
            {chosen.length ? `添加 ${chosen.length} 张` : "添加"}
          </button>
        </>
      }
    >
      {failure ? (
        <p className={styles.fcProblem}>{failure}</p>
      ) : !libraries ? (
        <div className={styles.fcCentered}>
          <span className="loader" aria-label="正在打开相册" />
        </div>
      ) : (
        <Tabs.Root value={tab} onValueChange={setTab} className={styles.fcLibrary}>
          <Tabs.List className={styles.fcLibraryTabs} aria-label="图库">
            {tabs.map((library) => (
              <Tabs.Trigger key={library.id} value={library.id} className={styles.fcLibraryTab}>
                {name(library)}
              </Tabs.Trigger>
            ))}
          </Tabs.List>
          {tabs.map((library) => (
            <Tabs.Content key={library.id} value={library.id} className={styles.fcLibraryGrid}>
              <Timeline
                libraryId={library.id}
                pending={noPending}
                onRetryUpload={nothing}
                onReloaded={nothing}
                onCount={nothing}
                onError={setFailure}
                empty={<Empty icon={<CircleAlert />} title="没有照片" text={`${name(library)}还没有照片。`} />}
                badge={badge}
                rowHeight={rowHeight}
                onZoom={(factor) => setRowHeight((value) => clampRowHeight(value * factor))}
                selected={shown}
                onOpen={(asset) => toggle(asset, false)}
                onToggle={toggle}
                onBeginSwipe={selection.beginSwipe}
                onMarquee={(ids, additive) => selection.marquee(allowed(ids), additive)}
                onGestureEnd={selection.endGesture}
                onBackgroundClick={nothing}
                onSelectGroup={(ids) => selection.setGroup(allowed(ids))}
                onAssets={(loaded) => {
                  setAssets(loaded);
                  setKnown((current) => {
                    const next = new Map(current);
                    for (const asset of loaded) next.set(asset.id, asset);
                    return next;
                  });
                }}
                hidden={noHidden}
                refreshKey={0}
                scroller={scroller}
                topInset={8}
              />
            </Tabs.Content>
          ))}
        </Tabs.Root>
      )}
    </Modal>
  );
}
