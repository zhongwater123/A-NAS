import { ChevronDown, ChevronUp, FileText, History, ScanSearch, Search, ShieldCheck, Sunset, X } from "lucide-react";
import { FormEvent, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

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
  onOpenCluster: (label: PhotoLabelCount) => void;
  onError: (message: string) => void;
}

// Cluster cards are at least this wide; a collapsed group shows one row.
const cardWidth = 124;
const cardGap = 14;

// Until local AI has grouped any photos, the kinds of sentence that search
// well fill the page (docs/architecture/photo-ai.md#语义搜索).
const ways = [
  { icon: ScanSearch, title: "描述画面", text: "照片里有谁，在做什么", examples: ["草地上奔跑的孩子", "桌上的生日蛋糕", "抱着猫的人"] },
  { icon: Sunset, title: "场景与氛围", text: "地点、天气、时间和光线", examples: ["傍晚的海边", "下雪的街道", "夜晚的城市灯光"] },
  { icon: FileText, title: "截图与文档", text: "拍下的纸张和屏幕", examples: ["手机截图", "手写的笔记", "餐厅菜单"] },
];

// AIHome is AI 聚合: a sentence finds photos by meaning, and below, the photos
// local AI has grouped by what they show. A group holds only the photos the
// model is sure of; it opens on its own and never filters a search.
export function AIHome({ viewing, refreshKey, topInset, ai, recent, onSearch, onForget, onOpenCluster, onError }: Props) {
  const [query, setQuery] = useState("");
  const [clusters, setClusters] = useState<{ items: PhotoLabelCount[]; ready: boolean }>();
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const groups = useMemo(() => {
    const byCategory = new Map<string, PhotoLabelCount[]>();
    for (const label of clusters?.items ?? []) byCategory.set(label.category, [...byCategory.get(label.category) ?? [], label]);
    const order = Object.keys(categoryNames);
    return [...byCategory].sort(([a], [b]) => (order.indexOf(a) + 1 || 99) - (order.indexOf(b) + 1 || 99));
  }, [clusters]);
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
  }, [groups.length > 0]);
  useEffect(() => {
    let live = true;
    listPhotoLabels(viewing).then((value) => { if (live) setClusters(value); }, (caught) => { if (live) { setClusters({ items: [], ready: false }); onError(messageOf(caught)); } });
    return () => { live = false; };
  }, [viewing, refreshKey]);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (query.trim()) onSearch(query.trim());
  };
  const status = !ai || ai.state === "unavailable" ? "" : ai.pending ? `已整理 ${ai.ready} / ${ai.ready + ai.pending + ai.failed} 张` : `已整理全部 ${ai.ready} 张`;

  return (
    <div className="ph-cards-scroll ph-discover" style={{ paddingTop: topInset }}>
      <section className="ph-hero" aria-label="用一句话找照片">
        <h2>用一句话找照片</h2>
        <form className="ph-hero-search" role="search" onSubmit={submit}>
          <Search />
          <input autoFocus type="search" aria-label="用一句话搜索照片" placeholder="例如：傍晚海边散步的一家人" maxLength={200} value={query} onChange={(event) => setQuery(event.target.value)} />
          <button type="submit" className="ph-button primary" disabled={!query.trim()}>搜索</button>
        </form>
        {recent.length > 0 && (
          <div className="ph-hero-recent" aria-label="最近搜索">
            <History />
            {recent.map((item) => <button type="button" key={item} onClick={() => onSearch(item)}>{item}</button>)}
            <button type="button" className="ph-hero-forget" aria-label="清除最近搜索" title="清除最近搜索" onClick={onForget}><X /></button>
          </div>
        )}
        <p className="ph-hero-note"><ShieldCheck />照片在这台 NAS 上由本地 AI 识别，不会离开设备{status ? ` · ${status}` : ""}</p>
      </section>

      {clusters === undefined ? <div className="ph-loading"><span className="ph-spinner" /></div> : groups.length ? (
        <section ref={browse} className="ph-browse" aria-label="按内容聚合">
          <header><h3>按内容聚合</h3><small>只收本地 AI 有把握的照片，宁可少放，不放错</small></header>
          {groups.map(([category, items]) => {
            const expanded = open.has(category);
            return (
              <div className="ph-browse-group" key={category}>
                <h4>{categoryNames[category] ?? category}<small>{items.length} 类</small></h4>
                <div className="ph-cards things">
                  {(expanded ? items : items.slice(0, perRow)).map((label) => (
                    <button type="button" key={label.id} className="ph-card ph-thing" aria-label={`打开 ${label.name}`} onClick={() => onOpenCluster(label)}>
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
      ) : (
        <section className="ph-ways" aria-label="可以这样搜">
          <header><h3>可以这样搜</h3><small>照片整理好后，本地 AI 有把握的照片会按动物、风景、物品等聚合在这里</small></header>
          <div className="ph-ways-grid">
            {ways.map(({ icon: Icon, title, text, examples }) => (
              <article className="ph-way" key={title}>
                <span className="ph-way-icon"><Icon /></span>
                <h4>{title}</h4>
                <p>{text}</p>
                <div className="ph-way-examples">
                  {examples.map((example) => <button type="button" key={example} aria-label={`搜索 ${example}`} onClick={() => onSearch(example)}>{example}</button>)}
                </div>
              </article>
            ))}
          </div>
        </section>
      )}
    </div>
  );
}
