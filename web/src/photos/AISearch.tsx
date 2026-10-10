import { ChevronDown, ChevronUp, History, ScanSearch, Search, ShieldCheck, X } from "lucide-react";
import { FormEvent, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { Empty } from "./Collections";
import { categoryNames, messageOf } from "./model";
import { listPhotoLabels, thumbnailURL, type PhotoAIStatus, type PhotoLabelCount } from "./photosApi";

interface Props {
  viewing: string;
  refreshKey: number;
  topInset: number;
  ai?: PhotoAIStatus;
  recent: string[];
  onSearch: (query: string) => void;
  onForget: () => void;
  onError: (message: string) => void;
}

// Category cards are at least this wide; a collapsed group shows one row.
const cardWidth = 124;
const cardGap = 14;

// AISearchHome is where AI search starts: a sentence finds photos; below, the
// things local AI recognises reliably, by kind, each a search away.
export function AISearchHome({ viewing, refreshKey, topInset, ai, recent, onSearch, onForget, onError }: Props) {
  const [query, setQuery] = useState("");
  const [labels, setLabels] = useState<{ items: PhotoLabelCount[]; ready: boolean }>();
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const browse = useRef<HTMLElement>(null);
  const [perRow, setPerRow] = useState(6);
  useLayoutEffect(() => {
    const element = browse.current;
    if (!element) return;
    const measure = () => setPerRow(Math.max(2, Math.floor(((element.clientWidth || 900) + cardGap) / (cardWidth + cardGap))));
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    let live = true;
    listPhotoLabels(viewing).then((value) => { if (live) setLabels(value); }, (caught) => { if (live) { setLabels({ items: [], ready: false }); onError(messageOf(caught)); } });
    return () => { live = false; };
  }, [viewing, refreshKey]);
  const groups = useMemo(() => {
    const byCategory = new Map<string, PhotoLabelCount[]>();
    for (const label of labels?.items ?? []) byCategory.set(label.category, [...byCategory.get(label.category) ?? [], label]);
    const order = Object.keys(categoryNames);
    return [...byCategory].sort(([a], [b]) => (order.indexOf(a) + 1 || 99) - (order.indexOf(b) + 1 || 99));
  }, [labels]);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (query.trim()) onSearch(query.trim());
  };
  const status = !ai || ai.state === "unavailable" ? "" : ai.pending ? `已整理 ${ai.ready} / ${ai.ready + ai.pending + ai.failed} 张` : `已整理全部 ${ai.ready} 张`;

  return (
    <div className="ph-cards-scroll ph-discover" style={{ paddingTop: topInset }}>
      <section className="ph-hero" aria-label="AI 搜图">
        <h2>用一句话找照片</h2>
        <form className="ph-hero-search" role="search" onSubmit={submit}>
          <Search />
          <input autoFocus type="search" aria-label="用一句话搜索照片" placeholder="例如：海边的猫、桌上的笔记本电脑" maxLength={200} value={query} onChange={(event) => setQuery(event.target.value)} />
          <button type="submit" className="ph-button primary" disabled={!query.trim()}>搜索</button>
        </form>
        {labels?.items.length ? (
          <div className="ph-hero-chips" aria-label="常见事物">
            {labels.items.slice(0, 8).map((label) => <button type="button" key={label.id} onClick={() => onSearch(label.name)}>{label.name}<small>{label.photos}</small></button>)}
          </div>
        ) : null}
        {recent.length > 0 && (
          <div className="ph-hero-recent" aria-label="最近搜索">
            <History />
            {recent.map((item) => <button type="button" key={item} onClick={() => onSearch(item)}>{item}</button>)}
            <button type="button" className="ph-hero-forget" aria-label="清除最近搜索" title="清除最近搜索" onClick={onForget}><X /></button>
          </div>
        )}
        <p className="ph-hero-note"><ShieldCheck />照片在这台 NAS 上由本地 AI 识别，不会离开设备{status ? ` · ${status}` : ""}</p>
      </section>

      <section ref={browse} className="ph-browse" aria-label="按内容浏览">
        <header><h3>按内容浏览</h3><small>这些是本地 AI 能可靠认出的事物，点一下即可搜索</small></header>
        {labels === undefined ? <div className="ph-loading"><span className="ph-spinner" /></div>
          : !groups.length ? (
            <Empty icon={<ScanSearch />} title="还没有可以浏览的事物" text="照片整理好后，这里会按动物、食物、物品等列出照片里认出的事物。现在可以先按名称和标签搜索。" />
          ) : groups.map(([category, items]) => {
            const expanded = open.has(category);
            return (
              <div className="ph-browse-group" key={category}>
                <h4>{categoryNames[category] ?? category}<small>{items.length} 类</small></h4>
                <div className="ph-cards things">
                  {(expanded ? items : items.slice(0, perRow)).map((label) => (
                    <button type="button" key={label.id} className="ph-card ph-thing" aria-label={`搜索 ${label.name}`} onClick={() => onSearch(label.name)}>
                      <span className="ph-card-cover"><img src={thumbnailURL(label.coverId)} alt="" loading="lazy" /></span>
                      <strong>{label.name}</strong>
                      <small>{label.photos} 张</small>
                    </button>
                  ))}
                </div>
                {items.length > perRow && (
                  <button type="button" className="ph-browse-more" onClick={() => setOpen((value) => { const next = new Set(value); if (expanded) next.delete(category); else next.add(category); return next; })}>
                    {expanded ? <><ChevronUp />收起</> : <><ChevronDown />展开全部 {items.length} 类</>}
                  </button>
                )}
              </div>
            );
          })}
      </section>
    </div>
  );
}
