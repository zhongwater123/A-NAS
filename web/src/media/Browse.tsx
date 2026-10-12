import { ArrowDownWideNarrow, Clapperboard, Heart, History, Search, Trash2, X } from "lucide-react";
import { ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";

import { APIError } from "../api";
import { PosterCard, StillCard, type CardAction } from "./Cards";
import { ago, dayLabel, remaining } from "./format";
import { listFavorites, listHistory, listTitles, type Category, type MediaLibrary, type SortOrder, type Title } from "./mediaApi";

export function messageOf(error: unknown): string {
  if (error instanceof APIError) {
    switch (error.code) {
      case "volume_unavailable": return "媒体文件暂时无法读取：数据卷未就绪。";
      case "media_unavailable": return "影视中心暂时不可用。";
      case "media_busy": return "设备正在为其他人转码，请稍后再试。";
      case "folders_overlap": return "所选文件夹已属于另一个媒体库。";
      case "forbidden": return "你没有权限执行这个操作。";
      case "not_found": return "内容不存在或已被移除。";
    }
    return error.message;
  }
  return "请求失败，请稍后再试";
}

export function PageHeader({ title, count, children }: { title: string; count?: number; children?: ReactNode }) {
  return (
    <header className="mc-page-header">
      <h1>{title}{count !== undefined && <small>{count}</small>}</h1>
      <div className="mc-page-tools">{children}</div>
    </header>
  );
}

export function Empty({ icon, title, text, children }: { icon: ReactNode; title: string; text?: string; children?: ReactNode }) {
  return <div className="mc-state">{icon}<h2>{title}</h2>{text && <p>{text}</p>}{children}</div>;
}

export function Loading() {
  return <div className="mc-state" aria-busy="true"><span className="mc-spinner" /></div>;
}

export function TitleGrid({ items, still, extra }: { items: Title[]; still?: boolean; extra?: (title: Title) => CardAction[] }) {
  return (
    <div className={`mc-grid ${still ? "still" : ""}`}>
      {items.map((title) => (still || title.type === "episode"
        ? <StillCard key={title.id} title={title} extra={extra?.(title)} />
        : <PosterCard key={title.id} title={title} extra={extra?.(title)} />))}
    </div>
  );
}

const sortLabels: Record<SortOrder, string> = { added: "最近添加", name: "名称", year: "年份", rating: "评分" };
const categoryTitles: Record<Category, string> = { all: "全部", movie: "电影", show: "电视剧", other: "其他" };
const pageSize = 90;

// useInfinite loads more when the sentinel scrolls into view.
function useSentinel(onVisible: () => void, enabled: boolean) {
  const sentinel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const element = sentinel.current;
    if (!element || !enabled || typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver((entries) => { if (entries.some((entry) => entry.isIntersecting)) onVisible(); }, { rootMargin: "600px" });
    observer.observe(element);
    return () => observer.disconnect();
  }, [onVisible, enabled]);
  return sentinel;
}

// CategoryPage lists one category, or a search, with sorting and filters.
export function CategoryPage({ category, libraries, library: fixedLibrary, search, refreshKey, onError }: {
  category: Category; libraries: MediaLibrary[]; library?: MediaLibrary; search?: string; refreshKey: number; onError: (message: string) => void;
}) {
  const [sort, setSort] = useState<SortOrder>(search ? "name" : "added");
  const [library, setLibrary] = useState(fixedLibrary?.id ?? "");
  const [genre, setGenre] = useState("");
  const [items, setItems] = useState<Title[]>();
  const [total, setTotal] = useState(0);
  const [genres, setGenres] = useState<string[]>([]);
  const [loadingMore, setLoadingMore] = useState(false);
  const generation = useRef(0);
  useEffect(() => { setLibrary(fixedLibrary?.id ?? ""); setGenre(""); }, [fixedLibrary?.id, category]);
  useEffect(() => {
    const current = ++generation.current;
    listTitles({ category, sort, library, genre, q: search, limit: pageSize }).then((page) => {
      if (current !== generation.current) return;
      setItems(page.items);
      setTotal(page.total);
      setGenres(page.genres);
    }, (caught) => { if (current === generation.current) { setItems([]); onError(messageOf(caught)); } });
  }, [category, sort, library, genre, search, refreshKey, onError]);
  const more = useCallback(() => {
    if (!items || loadingMore || items.length >= total) return;
    const current = generation.current;
    setLoadingMore(true);
    listTitles({ category, sort, library, genre, q: search, offset: items.length, limit: pageSize }).then((page) => {
      if (current === generation.current) setItems((existing) => [...(existing ?? []), ...page.items]);
    }, () => undefined).finally(() => setLoadingMore(false));
  }, [items, loadingMore, total, category, sort, library, genre, search]);
  const sentinel = useSentinel(more, Boolean(items && items.length < total));
  const heading = search ? `“${search}”的搜索结果` : fixedLibrary ? fixedLibrary.name : categoryTitles[category];
  const choices = libraries.filter((candidate) => category === "all" || category === "other" ? true : candidate.kind !== "other");
  return (
    <div className="mc-page">
      <PageHeader title={heading} count={items ? total : undefined}>
        {!fixedLibrary && choices.length > 1 && (
          <label className="mc-select">
            <span>媒体库</span>
            <select value={library} onChange={(event) => setLibrary(event.target.value)}>
              <option value="">全部媒体库</option>
              {choices.map((candidate) => <option key={candidate.id} value={candidate.id}>{candidate.name}</option>)}
            </select>
          </label>
        )}
        <label className="mc-select">
          <ArrowDownWideNarrow />
          <select aria-label="排序" value={sort} onChange={(event) => setSort(event.target.value as SortOrder)}>
            {(Object.keys(sortLabels) as SortOrder[]).map((key) => <option key={key} value={key}>{sortLabels[key]}</option>)}
          </select>
        </label>
      </PageHeader>
      {genres.length > 1 && (
        <div className="mc-chips" role="group" aria-label="类型">
          <button type="button" className={!genre ? "active" : ""} onClick={() => setGenre("")}>全部类型</button>
          {genres.slice(0, 16).map((value) => <button key={value} type="button" className={genre === value ? "active" : ""} onClick={() => setGenre(genre === value ? "" : value)}>{value}</button>)}
        </div>
      )}
      {!items ? <Loading /> : items.length === 0 ? (
        search
          ? <Empty icon={<Search />} title="没有找到匹配的影片" text="换个片名、原名或文件名试试。" />
          : <Empty icon={<Clapperboard />} title={`还没有${categoryTitles[category] === "全部" ? "视频" : categoryTitles[category]}`} text="把视频放进媒体库的文件夹，扫描后会出现在这里。" />
      ) : (
        <>
          <TitleGrid items={items} still={category === "other"} />
          <div ref={sentinel} className="mc-sentinel">{loadingMore && <span className="mc-spinner small" />}</div>
        </>
      )}
    </div>
  );
}

function historyCaption(title: Title): string | undefined {
  const progress = title.progress;
  if (!progress) return undefined;
  const when = ago(progress.playedAt);
  if (progress.watched && !progress.position) return `已看完 · ${when}`;
  if (progress.position) return `${remaining(progress.position, progress.duration)} · ${when}`;
  return `播放过 · ${when}`;
}

export function FavoritesPage({ refreshKey, onError }: { refreshKey: number; onError: (message: string) => void }) {
  const [items, setItems] = useState<Title[]>();
  useEffect(() => { listFavorites().then((page) => setItems(page.items), (caught) => { setItems([]); onError(messageOf(caught)); }); }, [refreshKey, onError]);
  return (
    <div className="mc-page">
      <PageHeader title="我的收藏" count={items?.length} />
      {!items ? <Loading /> : items.length ? <TitleGrid items={items} /> : (
        <Empty icon={<Heart />} title="还没有收藏" text="在海报或详情页点一下心形，喜欢的影片就会收在这里。" />
      )}
    </div>
  );
}

export function HistoryPage({ refreshKey, onRemove, onClear, onError }: {
  refreshKey: number; onRemove: (title: Title) => void; onClear: () => void; onError: (message: string) => void;
}) {
  const [items, setItems] = useState<Title[]>();
  useEffect(() => { listHistory().then((page) => setItems(page.items), (caught) => { setItems([]); onError(messageOf(caught)); }); }, [refreshKey, onError]);
  const groups = useMemo(() => {
    const result: { label: string; items: Title[] }[] = [];
    for (const item of items ?? []) {
      const label = item.progress?.playedAt ? dayLabel(item.progress.playedAt) : "更早";
      if (result.at(-1)?.label !== label) result.push({ label, items: [] });
      result.at(-1)!.items.push(item);
    }
    return result;
  }, [items]);
  const remove = (title: Title): CardAction[] => [{ label: "从记录中移除", icon: <X />, danger: true, run: () => onRemove(title) }];
  return (
    <div className="mc-page">
      <PageHeader title="最近播放" count={items?.length}>
        {items && items.length > 0 && <button type="button" className="mc-glass small" onClick={onClear}><Trash2 />清空记录</button>}
      </PageHeader>
      {!items ? <Loading /> : !items.length ? (
        <Empty icon={<History />} title="还没有播放记录" text="播放过的视频会按时间出现在这里，并记住看到哪里。" />
      ) : groups.map((group) => (
        <section key={group.label} className="mc-history-day" aria-label={group.label}>
          <h2>{group.label}</h2>
          <div className="mc-grid still">
            {group.items.map((title) => (
              <StillCard key={title.id} title={title} extra={remove(title)}
                caption={historyCaption(title)} />
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}
