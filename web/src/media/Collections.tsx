import { ArrowLeft, Layers, Pencil, Plus, Sparkles, Trash2, X } from "lucide-react";
import { useEffect, useState } from "react";

import { artworkURL, type Collection, type CollectionDetail, getCollection, listCollections, type Title } from "./api";
import { Empty, Loading, messageOf, PageHeader, TitleGrid } from "./Browse";
import { Menu } from "./Cards";
import styles from "./media.module.css";

function Collage({ covers }: { covers: string[] }) {
  const slots = covers.slice(0, 4);
  if (!slots.length)
    return (
      <div className={`${styles.mcCollage} ${styles.empty}`}>
        <Layers />
      </div>
    );
  return (
    <div className={`${styles.mcCollage} ${styles[`n${slots.length}`] ?? ""}`}>
      {slots.map((id) => (
        <img
          key={id}
          src={artworkURL(id, "poster")}
          alt=""
          loading="lazy"
          onError={(event) => {
            const image = event.currentTarget;
            if (!image.dataset.fallback) {
              image.dataset.fallback = "1";
              image.src = artworkURL(id, "thumb");
            } else image.style.visibility = "hidden";
          }}
        />
      ))}
    </div>
  );
}

export function CollectionsPage({
  refreshKey,
  onOpen,
  onCreate,
  onError,
}: {
  refreshKey: number;
  onOpen: (collection: Collection) => void;
  onCreate: () => void;
  onError: (message: string) => void;
}) {
  const [items, setItems] = useState<Collection[]>();
  useEffect(() => {
    listCollections().then(setItems, (caught) => {
      setItems([]);
      onError(messageOf(caught));
    });
  }, [refreshKey, onError]);
  const own = items?.filter((item) => !item.automatic) ?? [];
  const automatic = items?.filter((item) => item.automatic) ?? [];
  return (
    <div className={styles.mcPage}>
      <PageHeader title="合集" count={items?.length}>
        <button type="button" className={`${styles.mcGlass} ${styles.small}`} onClick={onCreate}>
          <Plus />
          新建合集
        </button>
      </PageHeader>
      {!items ? (
        <Loading />
      ) : !items.length ? (
        <Empty
          icon={<Layers />}
          title="还没有合集"
          text="把系列电影或想一起看的片子放进合集。带合集信息的 NFO 文件会自动生成合集。"
        >
          <button type="button" className={styles.mcPrimary} onClick={onCreate}>
            <Plus />
            新建合集
          </button>
        </Empty>
      ) : (
        <>
          {own.length > 0 && <CollectionGrid items={own} onOpen={onOpen} />}
          {automatic.length > 0 && (
            <section className={styles.mcSection}>
              <h2>
                <Sparkles />
                自动合集<small>来自 NFO 文件</small>
              </h2>
              <CollectionGrid items={automatic} onOpen={onOpen} />
            </section>
          )}
        </>
      )}
    </div>
  );
}

function CollectionGrid({ items, onOpen }: { items: Collection[]; onOpen: (collection: Collection) => void }) {
  return (
    <div className={styles.mcCollectionGrid}>
      {items.map((item) => (
        <button key={item.id} type="button" className={styles.mcCollection} onClick={() => onOpen(item)}>
          <Collage covers={item.covers} />
          <span className={styles.mcCollectionText}>
            <strong>{item.name}</strong>
            <small>
              {item.count} 部{item.automatic ? " · 自动" : ""}
            </small>
          </span>
        </button>
      ))}
    </div>
  );
}

export function CollectionPage({
  id,
  refreshKey,
  onBack,
  onRename,
  onDelete,
  onRemove,
  onError,
}: {
  id: string;
  refreshKey: number;
  onBack: () => void;
  onRename: (collection: CollectionDetail) => void;
  onDelete: (collection: CollectionDetail) => void;
  onRemove: (collection: CollectionDetail, title: Title) => void;
  onError: (message: string) => void;
}) {
  const [detail, setDetail] = useState<CollectionDetail>();
  const [missing, setMissing] = useState(false);
  useEffect(() => {
    let live = true;
    getCollection(id).then(
      (value) => {
        if (live) {
          setDetail(value);
          setMissing(false);
        }
      },
      (caught) => {
        if (live) {
          setMissing(true);
          onError(messageOf(caught));
        }
      },
    );
    return () => {
      live = false;
    };
  }, [id, refreshKey, onError]);
  if (missing)
    return (
      <Empty icon={<Layers />} title="合集不存在" text="它可能已被删除。">
        <button type="button" className={styles.mcGlass} onClick={onBack}>
          返回合集
        </button>
      </Empty>
    );
  if (!detail) return <Loading />;
  return (
    <div className={styles.mcPage}>
      <PageHeader title={detail.name} count={detail.items.length}>
        <button type="button" className={`${styles.mcGlass} ${styles.small}`} onClick={onBack}>
          <ArrowLeft />
          全部合集
        </button>
        {!detail.automatic && (
          <Menu
            label="合集操作"
            items={[
              { label: "重命名", icon: <Pencil />, run: () => onRename(detail) },
              { label: "删除合集", icon: <Trash2 />, danger: true, run: () => onDelete(detail) },
            ]}
            className={`${styles.mcGlass} ${styles.small} ${styles.icon}`}
          />
        )}
      </PageHeader>
      {detail.automatic && (
        <p className={styles.mcNote}>
          <Sparkles />
          这个合集来自影片 NFO 中的合集信息，会随媒体库自动更新。
        </p>
      )}
      {detail.items.length ? (
        <TitleGrid
          items={detail.items}
          extra={
            detail.automatic
              ? undefined
              : (title) => [{ label: "从合集移除", icon: <X />, danger: true, run: () => onRemove(detail, title) }]
          }
        />
      ) : (
        <Empty icon={<Layers />} title="合集是空的" text="在海报的更多菜单或详情页选择“加入合集”。" />
      )}
    </div>
  );
}
