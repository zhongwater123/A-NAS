import { ChevronLeft, ChevronRight, Clapperboard, FolderPlus, Info, Play, Sparkles } from "lucide-react";
import { ReactNode, useCallback, useEffect, useRef, useState } from "react";

import { Artwork, PosterCard, StillCard, useMediaActions } from "./Cards";
import { episodeCode, heading, remaining, runtime, typeLabel } from "./format";
import type { Home as HomeData, Title } from "./mediaApi";

// Shelf is a row that scrolls sideways with arrow buttons.
export function Shelf({ title, count, onMore, children }: { title: string; count?: number; onMore?: () => void; children: ReactNode }) {
  const track = useRef<HTMLDivElement>(null);
  const [edges, setEdges] = useState({ start: true, end: false });
  const measure = useCallback(() => {
    const element = track.current;
    if (!element) return;
    setEdges({ start: element.scrollLeft < 4, end: element.scrollLeft + element.clientWidth >= element.scrollWidth - 4 });
  }, []);
  useEffect(() => {
    measure();
    const element = track.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [measure, children]);
  const scroll = (direction: number) => track.current?.scrollBy({ left: direction * track.current.clientWidth * 0.85, behavior: "smooth" });
  return (
    <section className="mc-shelf" aria-label={title}>
      <header>
        <h2>{title}{count !== undefined && <small>{count}</small>}</h2>
        {onMore && <button type="button" className="mc-link" onClick={onMore}>查看全部<ChevronRight /></button>}
      </header>
      <div className="mc-shelf-body">
        {!edges.start && <button type="button" className="mc-shelf-arrow left" aria-label={`${title}向左`} onClick={() => scroll(-1)}><ChevronLeft /></button>}
        <div ref={track} className="mc-shelf-track" onScroll={measure}>{children}</div>
        {!edges.end && <button type="button" className="mc-shelf-arrow right" aria-label={`${title}向右`} onClick={() => scroll(1)}><ChevronRight /></button>}
      </div>
    </section>
  );
}

const heroInterval = 8000;

function Hero({ items, continuing }: { items: Title[]; continuing: Set<string> }) {
  const actions = useMediaActions();
  const [index, setIndex] = useState(0);
  const [paused, setPaused] = useState(false);
  const count = items.length;
  useEffect(() => { if (index >= count) setIndex(0); }, [count, index]);
  useEffect(() => {
    if (paused || count < 2) return;
    const timer = window.setTimeout(() => setIndex((current) => (current + 1) % count), heroInterval);
    return () => window.clearTimeout(timer);
  }, [index, paused, count]);
  const current = items[Math.min(index, count - 1)];
  if (!current) return null;
  const resuming = Boolean(current.progress?.position);
  const meta = [
    current.type === "episode" ? episodeCode(current.season, current.episode) : typeLabel[current.type],
    current.year,
    current.type !== "show" ? runtime(current.duration) : current.episodes ? `${current.episodes} 集` : "",
    current.resolution === "4K" ? "4K" : "",
    current.hdr ? "HDR" : "",
  ].filter(Boolean);
  return (
    <section className="mc-hero" aria-roledescription="carousel" aria-label="精选" onPointerEnter={() => setPaused(true)} onPointerLeave={() => setPaused(false)}>
      {items.map((item, position) => (
        <div key={item.id} className={`mc-hero-slide ${position === index ? "active" : ""}`} aria-hidden={position !== index}>
          <Artwork title={item} kind="backdrop" fallback="thumb" label={false} eager={position === 0} />
        </div>
      ))}
      <div className="mc-hero-shade" />
      <div className="mc-hero-content" key={current.id}>
        {continuing.has(current.id) ? <p className="mc-hero-kicker"><Play />继续观看</p> : <p className="mc-hero-kicker"><Sparkles />最近添加</p>}
        <h1>{heading(current)}</h1>
        <p className="mc-hero-meta">{meta.map((part, position) => <span key={position}>{part}</span>)}</p>
        {current.type === "episode" && current.title && !current.title.startsWith("第 ") && <p className="mc-hero-plot">{current.title}</p>}
        <div className="mc-hero-actions">
          <button type="button" className="mc-primary" onClick={() => actions.play(current)}>
            <Play />{resuming ? "继续播放" : "播放"}
          </button>
          <button type="button" className="mc-glass" onClick={() => actions.open(current)}><Info />详情</button>
          {resuming && current.progress && <span className="mc-hero-left">{remaining(current.progress.position, current.progress.duration)}</span>}
        </div>
      </div>
      {count > 1 && (
        <>
          <button type="button" className="mc-hero-arrow left" aria-label="上一个" onClick={() => setIndex((index - 1 + count) % count)}><ChevronLeft /></button>
          <button type="button" className="mc-hero-arrow right" aria-label="下一个" onClick={() => setIndex((index + 1) % count)}><ChevronRight /></button>
          <div className="mc-hero-dots" role="tablist">
            {items.map((item, position) => (
              <button key={item.id} type="button" role="tab" aria-selected={position === index} aria-label={heading(item)} onClick={() => setIndex(position)}>
                {position === index && !paused && <span style={{ animationDuration: `${heroInterval}ms` }} />}
              </button>
            ))}
          </div>
        </>
      )}
    </section>
  );
}

export function HomePage({ home, onCategory, onLibraries, onCreateLibrary, canCreate }: {
  home: HomeData;
  onCategory: (category: "movie" | "show" | "other" | "all" | "favorites" | "history") => void;
  onLibraries: () => void;
  onCreateLibrary: () => void;
  canCreate: boolean;
}) {
  const empty = !home.recent.length;
  if (!home.libraries) {
    return (
      <div className="mc-welcome">
        <div className="mc-welcome-art" aria-hidden="true"><Clapperboard /></div>
        <h1>把你的视频变成私人影院</h1>
        <p>选择存放电影、电视剧或家庭视频的文件夹，影视中心会自动识别片名、季和集，生成海报墙，并记住每个人看到哪里。</p>
        <ol>
          <li><strong>1</strong><span>新建媒体库，选择类型</span></li>
          <li><strong>2</strong><span>选择个人空间或共享空间中的文件夹</span></li>
          <li><strong>3</strong><span>扫描完成后即可播放</span></li>
        </ol>
        {canCreate && <button type="button" className="mc-primary" onClick={onCreateLibrary}><FolderPlus />新建媒体库</button>}
      </div>
    );
  }
  if (empty) {
    return (
      <div className="mc-state">
        {home.scanning ? <span className="mc-spinner" /> : <Clapperboard />}
        <h2>{home.scanning ? "正在扫描媒体库…" : "媒体库里还没有视频"}</h2>
        <p>{home.scanning ? "找到的视频会陆续出现在这里。" : "把视频放进媒体库的文件夹后，重新扫描即可看到。"}</p>
        <button type="button" className="mc-glass" onClick={onLibraries}>管理媒体库</button>
      </div>
    );
  }
  return (
    <div className="mc-home">
      {home.featured.length > 0 && <Hero items={home.featured} continuing={new Set(home.continue.map((title) => title.id))} />}
      <div className="mc-home-rows">
        {home.continue.length > 0 && (
          <Shelf title="继续观看" onMore={() => onCategory("history")}>
            {home.continue.map((title) => <StillCard key={title.id} title={title}
              caption={title.progress?.position ? remaining(title.progress.position, title.progress.duration) : title.type === "episode" ? `下一集 · ${episodeCode(title.season, title.episode)}` : undefined} />)}
          </Shelf>
        )}
        <Shelf title="最近添加" onMore={() => onCategory("all")}>
          {home.recent.map((title) => <PosterCard key={title.id} title={title} />)}
        </Shelf>
        {home.movies.length > 0 && <Shelf title="电影" onMore={() => onCategory("movie")}>{home.movies.map((title) => <PosterCard key={title.id} title={title} />)}</Shelf>}
        {home.shows.length > 0 && <Shelf title="电视剧" onMore={() => onCategory("show")}>{home.shows.map((title) => <PosterCard key={title.id} title={title} />)}</Shelf>}
        {home.others.length > 0 && <Shelf title="其他视频" onMore={() => onCategory("other")}>{home.others.map((title) => <StillCard key={title.id} title={title} />)}</Shelf>}
        {home.favorites.length > 0 && <Shelf title="我的收藏" onMore={() => onCategory("favorites")}>{home.favorites.map((title) => <PosterCard key={title.id} title={title} />)}</Shelf>}
      </div>
    </div>
  );
}
