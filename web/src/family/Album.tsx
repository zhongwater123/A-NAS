import { ImagePlus, Play } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

import type { FamilyAlbumPhoto } from "../api.generated";
import { type FamilyBaby, familyError, getAlbum } from "./api";
import { useFamily } from "./context";
import styles from "./family.module.css";
import { monthLabel } from "./format";
import { MonthTitle, Thumb } from "./parts";

// Album is 云相册: the channel's photos and videos, newest first, grouped by
// the month of life they were taken in. The photos live in the shared
// library; a click opens one in 相册.
export function Album({ baby, version, onAdd }: { baby: FamilyBaby; version: number; onAdd: () => void }) {
  const family = useFamily();
  const [photos, setPhotos] = useState<FamilyAlbumPhoto[]>();
  const [next, setNext] = useState("");
  const [loading, setLoading] = useState(false);
  const [failure, setFailure] = useState("");
  const end = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let live = true;
    getAlbum(baby.id).then(
      (page) => {
        if (!live) return;
        setPhotos(page.photos);
        setNext(page.next ?? "");
      },
      (caught: unknown) => {
        if (live) setFailure(familyError(caught));
      },
    );
    return () => {
      live = false;
    };
  }, [baby.id, version]);

  // Reaching the end of the grid reads the next page.
  useEffect(() => {
    const target = end.current;
    if (!target || !next) return;
    const observer = new IntersectionObserver((entries) => {
      if (!entries.some((entry) => entry.isIntersecting) || loading) return;
      setLoading(true);
      getAlbum(baby.id, next).then(
        (page) => {
          setPhotos((current) => [...(current ?? []), ...page.photos]);
          setNext(page.next ?? "");
          setLoading(false);
        },
        (caught: unknown) => {
          family.toast({ text: familyError(caught), tone: "error" });
          setLoading(false);
        },
      );
    });
    observer.observe(target);
    return () => observer.disconnect();
  }, [baby.id, next, loading, family]);

  const groups = useMemo(() => {
    const result: Array<{ month: number; photos: FamilyAlbumPhoto[] }> = [];
    for (const photo of photos ?? []) {
      const last = result.at(-1);
      if (last && last.month === photo.monthOfLife) last.photos.push(photo);
      else result.push({ month: photo.monthOfLife, photos: [photo] });
    }
    return result;
  }, [photos]);

  if (failure) return <p className={styles.fcFailure}>{failure}</p>;
  if (!photos) return <span className="loader" aria-label="正在读取云相册" />;
  if (photos.length === 0)
    return (
      <div className={styles.fcBlank}>
        <h2>相册还是空的</h2>
        <p>在频道里上传的照片视频都会放在这里，按{baby.nickname || baby.name}的月龄排好。</p>
        <button type="button" className={styles.fcPrimary} onClick={onAdd}>
          <ImagePlus size={15} /> 上传照片视频
        </button>
      </div>
    );
  return (
    <div className={styles.fcAlbum}>
      {groups.map((group) => (
        <section key={group.month} className={styles.fcMonth} aria-label={monthLabel(group.month)}>
          <header className={styles.fcMonthHead}>
            <h2>
              <MonthTitle month={group.month} />
            </h2>
            <small>{group.photos.length} 项</small>
          </header>
          <div className={styles.fcAlbumGrid}>
            {group.photos.map((photo) => (
              <button
                key={photo.assetId}
                type="button"
                className={styles.fcAlbumTile}
                onClick={() => family.onShowPhoto(photo.assetId)}
                aria-label={`在相册中打开这${photo.video ? "段视频" : "张照片"}`}
              >
                <Thumb assetId={photo.assetId} />
                {photo.video && (
                  <span className={styles.fcVideoMark} aria-hidden="true">
                    <Play size={12} fill="currentColor" />
                  </span>
                )}
              </button>
            ))}
          </div>
        </section>
      ))}
      <div ref={end} className={styles.fcAlbumEnd}>
        {loading && <span className="loader small" aria-label="正在读取更多照片" />}
      </div>
    </div>
  );
}
