import { ChevronDown, ChevronUp, Hash, History, Search, ShieldCheck, X } from "lucide-react";
import { FormEvent, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { type ImageDropProps, ImageSearchButton } from "./imageSearch";
import { categoryNames, messageOf } from "./model";
import { PeopleRow, personGap, personWidth } from "./People";
import { listPeople, listPhotoLabels, listTagGroups, thumbnailURL, type PhotoAIStatus, type PhotoLabelCount, type PhotoPerson, type PhotoTagCount } from "./photosApi";

// AIHomeSnapshot is how AI 聚合 looked when one of its groups opened, so that
// going back lands on the same spot instead of the top.
export interface AIHomeSnapshot {
  viewing: string;
  scrollTop: number;
  clusters?: { items: PhotoLabelCount[]; ready: boolean };
  people?: PhotoPerson[];
  peopleExpanded: boolean;
  showHidden: boolean;
  tags: PhotoTagCount[];
  open: string[];
  allTags: boolean;
  perRow: number;
}

interface Props {
  viewing: string;
  userName: string;
  // Where to come back to; only one taken for the same scope is used.
  snapshot?: AIHomeSnapshot;
  refreshKey: number;
  topInset: number;
  ai?: PhotoAIStatus;
  recent: string[];
  onSearch: (query: string) => void;
  // The search box also takes an image to find photos like it.
  image: { pick: () => void; over: boolean; dragging: boolean; drop: ImageDropProps };
  onForget: () => void;
  onOpenCluster: (label: PhotoLabelCount, snapshot: AIHomeSnapshot) => void;
  onOpenTag: (tag: PhotoTagCount, snapshot: AIHomeSnapshot) => void;
  onOpenPerson: (person: PhotoPerson, snapshot: AIHomeSnapshot) => void;
  onError: (message: string) => void;
}

// Cluster cards are at least this wide; a collapsed group shows one row.
const cardWidth = 124;
const cardGap = 14;

// AIHome is AI 聚合: a sentence finds photos by meaning; below, the people
// and pets local AI keeps finding, the photos it has grouped by what they
// show, then the groups people made with their own tags. An AI group holds
// only the photos the model is sure of; it opens on its own and never filters
// a search. People are named by members, so their names do find photos.
export function AIHome({ viewing, userName, snapshot, refreshKey, topInset, ai, recent, onSearch, image, onForget, onOpenCluster, onOpenTag, onOpenPerson, onError }: Props) {
  const back = snapshot?.viewing === viewing ? snapshot : undefined;
  const [query, setQuery] = useState("");
  const [clusters, setClusters] = useState(back?.clusters);
  const [tags, setTags] = useState<PhotoTagCount[]>(back?.tags ?? []);
  const [people, setPeople] = useState<PhotoPerson[] | undefined>(back?.people);
  const [peopleExpanded, setPeopleExpanded] = useState(back?.peopleExpanded ?? false);
  const [showHidden, setShowHidden] = useState(back?.showHidden ?? false);
  const [allTags, setAllTags] = useState(back?.allTags ?? false);
  const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set(back?.open));
  // Local AI has settled once the groups are ready and no photo waits for
  // it; then the clusters load again if they changed since they were asked
  // for. Only the status is polled before that: any other request would
  // hold the remaining AI work back for another five minutes.
  const settled = ai?.clusters === "ready" && ai.pending === 0;
  const wasSettled = useRef<boolean | undefined>(undefined);
  const [reload, setReload] = useState(0);
  useEffect(() => {
    const was = wasSettled.current;
    if (ai) wasSettled.current = settled;
    if (settled && (was === false || (clusters && !clusters.ready))) setReload((value) => value + 1);
  }, [settled, clusters]);
  const groups = useMemo(() => {
    const byCategory = new Map<string, PhotoLabelCount[]>();
    for (const label of clusters?.items ?? []) byCategory.set(label.category, [...byCategory.get(label.category) ?? [], label]);
    const order = Object.keys(categoryNames);
    return [...byCategory].sort(([a], [b]) => (order.indexOf(a) + 1 || 99) - (order.indexOf(b) + 1 || 99));
  }, [clusters]);
  // Until it settles, an empty page means the groups are still coming.
  const waiting = !!clusters && !groups.length && !!ai?.clusters && ai.state !== "unavailable" && !(settled && clusters.ready);
  const scroller = useRef<HTMLDivElement>(null);
  const [perRow, setPerRow] = useState(back?.perRow ?? 6);
  const [peoplePerRow, setPeoplePerRow] = useState(7);
  const shown = (clusters === undefined ? "" : groups.length ? "groups" : waiting ? "waiting" : "") + (tags.length ? ":tags" : "") + (people?.length ? ":people" : "");
  // A row holds as many cards as the first group section has room for.
  useLayoutEffect(() => {
    const element = scroller.current;
    if (!element) return;
    const measure = () => {
      const section = element.querySelector<HTMLElement>(".ph-browse");
      if (!section) return;
      setPerRow(Math.max(2, Math.floor(((section.clientWidth || 900) + cardGap) / (cardWidth + cardGap))));
      setPeoplePerRow(Math.max(2, Math.floor(((section.clientWidth || 900) + personGap) / (personWidth + personGap))));
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [shown]);
  // Coming back from a group: the remembered page is already drawn, so its
  // scroll position is restored before the first paint.
  useLayoutEffect(() => {
    if (back && scroller.current) scroller.current.scrollTop = back.scrollTop;
  }, []);
  const remember = (): AIHomeSnapshot => ({
    viewing, scrollTop: scroller.current?.scrollTop ?? 0, clusters, people, peopleExpanded, showHidden, tags, open: [...open], allTags, perRow,
  });
  useEffect(() => {
    let live = true;
    listPhotoLabels(viewing).then((value) => { if (live) setClusters(value); }, (caught) => { if (live) { setClusters({ items: [], ready: false }); onError(messageOf(caught)); } });
    return () => { live = false; };
  }, [viewing, refreshKey, reload]);
  useEffect(() => {
    let live = true;
    listTagGroups(viewing).then((value) => { if (live) setTags(value); }, (caught) => { if (live) onError(messageOf(caught)); });
    return () => { live = false; };
  }, [viewing, refreshKey]);
  // People come with the photos local AI has organised, so they load again
  // when it settles, as the groups do.
  useEffect(() => {
    let live = true;
    listPeople(viewing, true).then((value) => { if (live) setPeople(value); }, (caught) => { if (live) { setPeople([]); onError(messageOf(caught)); } });
    return () => { live = false; };
  }, [viewing, refreshKey, reload]);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (query.trim()) onSearch(query.trim());
  };
  const status = !ai || ai.state === "unavailable" ? "" : ai.pending ? `已整理 ${ai.ready} / ${ai.ready + ai.pending + ai.failed} 张` : `已整理全部 ${ai.ready} 张`;

  return (
    <div ref={scroller} className="ph-cards-scroll ph-discover" style={{ paddingTop: topInset }}>
      <section className="ph-hero" aria-label="用一句话找照片">
        <h2>{userName}，您可以用一句话来找照片</h2>
        <form className={`ph-hero-search${image.over ? " image-drop" : ""}`} role="search" onSubmit={submit} {...image.drop}>
          <Search />
          <input autoFocus={!back} type="search" aria-label="用一句话搜索照片" placeholder={image.dragging ? "拖到这里，找出和这张图片相似的照片" : "例如：傍晚海边散步的一家人"} maxLength={200} value={query} onChange={(event) => setQuery(event.target.value)} />
          <ImageSearchButton onPick={image.pick} className="ph-hero-image" />
          <button type="submit" className="ph-button primary" disabled={!query.trim()}>搜索</button>
        </form>
        {recent.length > 0 && (
          <div className="ph-hero-recent" aria-label="最近搜索">
            <History />
            {recent.map((item) => <button type="button" key={item} onClick={() => onSearch(item)}>{item}</button>)}
            <button type="button" className="ph-hero-forget" aria-label="清除最近搜索" title="清除最近搜索" onClick={onForget}><X /></button>
          </div>
        )}
        <p className="ph-hero-note"><ShieldCheck />照片和人脸都在这台 NAS 上由本地 AI 识别，不会离开设备{status ? ` · ${status}` : ""}</p>
      </section>

      {people?.length ? (
        <PeopleRow people={people ?? []} perRow={peoplePerRow} expanded={peopleExpanded} showHidden={showHidden}
          onExpand={setPeopleExpanded} onShowHidden={setShowHidden} onOpen={(person) => onOpenPerson(person, remember())} />
      ) : null}

      {clusters === undefined ? <div className="ph-loading"><span className="ph-spinner" /></div> : groups.length ? (
        <section className="ph-browse" aria-label="按内容聚合">
          <header><h3>按内容聚合</h3></header>
          {groups.map(([category, items]) => {
            const expanded = open.has(category);
            return (
              <div className="ph-browse-group" key={category}>
                <h4>{categoryNames[category] ?? category}<small>{items.length} 类</small></h4>
                <div className="ph-cards things">
                  {(expanded ? items : items.slice(0, perRow)).map((label) => (
                    <button type="button" key={label.id} className="ph-card ph-thing" aria-label={`打开 ${label.name}`} onClick={() => onOpenCluster(label, remember())}>
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
      ) : waiting ? (
        <section className="ph-browse" aria-label="按内容聚合" aria-busy="true">
          <header><h3>按内容聚合</h3></header>
          <div className="ph-cards things ph-ghosts" aria-hidden="true">
            {Array.from({ length: perRow }, (_, index) => <span className="ph-card ph-ghost" key={index}><span className="ph-card-cover" /><strong /><small /></span>)}
          </div>
        </section>
      ) : null}

      {tags.length > 0 && (
        <section className="ph-browse ph-tag-groups" aria-label="你的标签">
          <header><h3>你的标签</h3></header>
          <div className="ph-cards things">
            {(allTags ? tags : tags.slice(0, perRow * 2)).map((tag) => (
              <button type="button" key={tag.name} className="ph-card ph-thing" aria-label={`打开标签 ${tag.name}`} onClick={() => onOpenTag(tag, remember())}>
                <span className="ph-card-cover"><img src={thumbnailURL(tag.coverId)} alt="" loading="lazy" /><span className="ph-tag-mark"><Hash /></span></span>
                <strong>{tag.name}</strong>
                <small>{tag.photos} 张</small>
              </button>
            ))}
          </div>
          {tags.length > perRow * 2 && (
            <button type="button" className="ph-browse-more" onClick={() => setAllTags(!allTags)}>
              {allTags ? <><ChevronUp />收起</> : <><ChevronDown />展开全部 {tags.length} 个标签</>}
            </button>
          )}
        </section>
      )}
    </div>
  );
}
