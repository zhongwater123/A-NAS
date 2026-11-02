import { ChevronLeft, ChevronRight, Images, X } from "lucide-react";
import { AnimatePresence, motion, type PanInfo } from "motion/react";
import { Dialog } from "radix-ui";
import { useEffect, useEffectEvent, useLayoutEffect, useRef, useState } from "react";

import { displayURL, downloadURL, getPhoto, type PhotoAsset, thumbnailURL } from "../photos/api";
import { VideoPlayer } from "../photos/VideoPlayer";
import { useFamily } from "./context";
import styles from "./family.module.css";

export interface LightboxPhoto {
  assetId: string;
  video: boolean;
}

// photoLayout names a photo for motion's shared layout: the tile it was
// opened from and the viewer's photo carry the same id, so one grows out of
// the other and shrinks back on close.
export const photoLayout = (scope: string, assetId: string) => `fc-photo-${scope}-${assetId}`;

// Lightbox shows a record's or the album's photos over the window: the one
// opened grows out of its tile, arrow keys, the buttons or a swipe move
// through the rest, and videos play in place. 相册 opens the photo where it
// lives, in the shared library.
export function Lightbox({
  scope,
  photos,
  opened,
  aspect,
  onClose,
}: {
  scope: string;
  photos: LightboxPhoto[];
  // opened is the index of the photo to show; undefined closes the viewer.
  opened?: number;
  // aspect is the opened photo's width over height, read from its tile.
  aspect?: number;
  onClose: () => void;
}) {
  const { container } = useFamily();
  return (
    <Dialog.Root
      open={opened !== undefined}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <AnimatePresence>
        {opened !== undefined && (
          <Dialog.Portal forceMount container={container.current}>
            <Dialog.Overlay asChild forceMount>
              <motion.div
                className={styles.fcLightboxBackdrop}
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.25 }}
              />
            </Dialog.Overlay>
            <Dialog.Content asChild forceMount aria-describedby={undefined}>
              <motion.div className={styles.fcLightbox} exit={{ opacity: 1 }}>
                <Viewer scope={scope} photos={photos} start={opened} aspect={aspect} />
              </motion.div>
            </Dialog.Content>
          </Dialog.Portal>
        )}
      </AnimatePresence>
    </Dialog.Root>
  );
}

function Viewer({
  scope,
  photos,
  start,
  aspect,
}: {
  scope: string;
  photos: LightboxPhoto[];
  start: number;
  aspect?: number;
}) {
  const { onShowPhoto } = useFamily();
  const [index, setIndex] = useState(start);
  const [direction, setDirection] = useState(0);
  const [assets, setAssets] = useState<Record<string, PhotoAsset>>({});
  const stage = useRef<HTMLDivElement>(null);
  const size = useStageSize(stage);
  const photo = photos[index];
  const asset = assets[photo.assetId];

  // Each photo's details come once, for its size and how to show it.
  useEffect(() => {
    let live = true;
    for (const near of [index, index + 1, index - 1]) {
      const assetId = photos[near]?.assetId;
      if (!assetId || assets[assetId]) continue;
      getPhoto(assetId).then(
        (found) => {
          if (live) setAssets((current) => ({ ...current, [assetId]: found }));
        },
        () => undefined,
      );
    }
    return () => {
      live = false;
    };
  }, [index, photos, assets]);

  const go = (step: number) => {
    const next = index + step;
    if (next < 0 || next >= photos.length) return;
    setDirection(step);
    setIndex(next);
  };
  // The arrow keys page through the photos while the viewer is open.
  const keys = useEffectEvent((event: KeyboardEvent) => {
    if (event.key === "ArrowRight") go(1);
    else if (event.key === "ArrowLeft") go(-1);
    else return;
    event.preventDefault();
  });
  useEffect(() => {
    const listen = (event: KeyboardEvent) => keys(event);
    document.addEventListener("keydown", listen);
    return () => document.removeEventListener("keydown", listen);
  }, []);
  const swiped = (_: unknown, info: PanInfo) => {
    if (info.offset.x < -80 || info.velocity.x < -500) go(1);
    else if (info.offset.x > 80 || info.velocity.x > 500) go(-1);
  };

  const ratio = asset?.width && asset.height ? asset.width / asset.height : index === start ? aspect : undefined;
  const box = fit(ratio ?? 4 / 3, size);
  // Only the photo that was opened shares its layout with a tile; the
  // others slide in and out.
  const shared = index === start;
  return (
    <div className={styles.fcLightboxFrame}>
      <header className={styles.fcLightboxBar}>
        <Dialog.Title className={styles.fcLightboxCount}>
          {index + 1} / {photos.length}
        </Dialog.Title>
        <button type="button" className={styles.fcLightboxButton} onClick={() => onShowPhoto(photo.assetId)}>
          <Images size={15} /> 在相册中打开
        </button>
        <Dialog.Close className={styles.fcLightboxIcon} aria-label="关闭">
          <X size={18} />
        </Dialog.Close>
      </header>
      <div className={styles.fcLightboxStage} ref={stage}>
        <AnimatePresence initial={false} custom={direction} mode="popLayout">
          <motion.div
            key={photo.assetId}
            layoutId={shared ? photoLayout(scope, photo.assetId) : undefined}
            className={styles.fcLightboxPhoto}
            style={{ width: box.width, height: box.height }}
            custom={direction}
            initial={shared ? false : { opacity: 0, x: direction * 80 }}
            animate={{ opacity: 1, x: 0 }}
            exit={shared ? { opacity: 0 } : { opacity: 0, x: direction * -80 }}
            transition={{ type: "spring", stiffness: 320, damping: 34 }}
            drag={photos.length > 1 && !photo.video ? "x" : false}
            dragConstraints={{ left: 0, right: 0 }}
            dragElastic={0.5}
            onDragEnd={swiped}
          >
            {photo.video && asset ? (
              <VideoPlayer
                asset={asset}
                stage={stage}
                onDownload={() => window.open(downloadURL(asset.id), "_blank", "noopener")}
              />
            ) : (
              <Picture assetId={photo.assetId} asset={asset} />
            )}
          </motion.div>
        </AnimatePresence>
        {index > 0 && (
          <button
            type="button"
            className={`${styles.fcLightboxStep} ${styles.back}`}
            onClick={() => go(-1)}
            aria-label="上一张"
          >
            <ChevronLeft size={22} />
          </button>
        )}
        {index < photos.length - 1 && (
          <button
            type="button"
            className={`${styles.fcLightboxStep} ${styles.forth}`}
            onClick={() => go(1)}
            aria-label="下一张"
          >
            <ChevronRight size={22} />
          </button>
        )}
      </div>
      {photos.length > 1 && (
        <nav className={styles.fcLightboxStrip} aria-label="这条记录的照片">
          {photos.map((item, at) => (
            <button
              key={item.assetId}
              type="button"
              className={at === index ? styles.active : undefined}
              aria-current={at === index ? "true" : undefined}
              aria-label={`第 ${at + 1} ${item.video ? "段视频" : "张照片"}`}
              onClick={() => go(at - index)}
            >
              <img src={thumbnailURL(item.assetId)} alt="" draggable={false} />
            </button>
          ))}
        </nav>
      )}
    </div>
  );
}

// Picture shows the tile's thumbnail at once, then the full photo over it
// when it has loaded, so the photo sharpens rather than blinks.
function Picture({ assetId, asset }: { assetId: string; asset?: PhotoAsset }) {
  const [sharp, setSharp] = useState(false);
  return (
    <>
      <img className={styles.fcLightboxThumb} src={thumbnailURL(assetId)} alt="" draggable={false} />
      {asset && (
        <motion.img
          className={styles.fcLightboxFull}
          src={displayURL(asset)}
          alt=""
          draggable={false}
          initial={{ opacity: 0 }}
          animate={{ opacity: sharp ? 1 : 0 }}
          transition={{ duration: 0.2 }}
          onLoad={() => setSharp(true)}
        />
      )}
    </>
  );
}

// useStageSize follows the stage's size, which the photo is fitted into.
function useStageSize(stage: React.RefObject<HTMLDivElement | null>) {
  const [size, setSize] = useState({ width: 960, height: 600 });
  useLayoutEffect(() => {
    const element = stage.current;
    if (!element) return;
    const measure = () => setSize({ width: element.clientWidth, height: element.clientHeight });
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [stage]);
  return size;
}

// fit is the largest box of a ratio inside the stage, leaving a margin for
// the arrows.
function fit(ratio: number, size: { width: number; height: number }) {
  const width = Math.max(120, size.width - 136);
  const height = Math.max(120, size.height - 24);
  return width / height > ratio ? { width: height * ratio, height } : { width, height: width / ratio };
}
