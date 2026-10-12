import { Clapperboard, Film, FolderOpen, Heart, History, House, Layers, LayoutGrid, Library, Tv, Video } from "lucide-react";
import { ReactNode } from "react";

import type { Category, MediaLibrary } from "./mediaApi";

export type Section = "home" | "history" | "favorites" | "folders" | "collections" | "libraries" | `category:${Category}`;

const primary: { section: Section; label: string; icon: ReactNode }[] = [
  { section: "home", label: "首页", icon: <House /> },
  { section: "history", label: "最近播放", icon: <History /> },
  { section: "favorites", label: "我的收藏", icon: <Heart /> },
  { section: "folders", label: "文件夹", icon: <FolderOpen /> },
  { section: "collections", label: "合集", icon: <Layers /> },
  { section: "libraries", label: "媒体库", icon: <Library /> },
];

const categories: { section: Section; label: string; icon: ReactNode }[] = [
  { section: "category:all", label: "全部", icon: <LayoutGrid /> },
  { section: "category:movie", label: "电影", icon: <Film /> },
  { section: "category:show", label: "电视剧", icon: <Tv /> },
  { section: "category:other", label: "其他", icon: <Video /> },
];

function Item({ section, label, icon, active, count, onSelect }: { section: Section; label: string; icon: ReactNode; active: boolean; count?: number; onSelect: (section: Section) => void }) {
  return (
    <button type="button" className={active ? "active" : ""} aria-current={active ? "page" : undefined} onClick={() => onSelect(section)}>
      {icon}<span>{label}</span>{count ? <small>{count}</small> : null}
    </button>
  );
}

export function Sidebar({ active, libraries, processing, onSelect }: { active?: Section; libraries?: MediaLibrary[]; processing: boolean; onSelect: (section: Section) => void }) {
  const totals = (libraries ?? []).reduce((sum, library) => ({
    movie: sum.movie + library.counts.movies, show: sum.show + library.counts.shows, other: sum.other + library.counts.others,
  }), { movie: 0, show: 0, other: 0 });
  const counts: Partial<Record<Section, number>> = {
    "category:all": totals.movie + totals.show + totals.other, "category:movie": totals.movie, "category:show": totals.show, "category:other": totals.other,
    libraries: libraries?.length,
  };
  const scanning = (libraries ?? []).filter((library) => library.scan.state === "scanning");
  const pending = (libraries ?? []).reduce((sum, library) => sum + (library.scan.state === "processing" ? library.scan.pending : 0), 0);
  return (
    <nav className="mc-sidebar" aria-label="影视中心导航">
      <div className="mc-brand"><span className="mc-brand-mark"><Clapperboard /></span><strong>影视中心</strong></div>
      <div className="mc-nav">
        {primary.map((item) => <Item key={item.section} {...item} active={active === item.section} count={counts[item.section]} onSelect={onSelect} />)}
      </div>
      <p className="mc-nav-heading">分类</p>
      <div className="mc-nav">
        {categories.map((item) => <Item key={item.section} {...item} active={active === item.section} count={counts[item.section]} onSelect={onSelect} />)}
      </div>
      <div className="mc-sidebar-fill" />
      {(scanning.length > 0 || (processing && pending > 0)) && (
        <div className="mc-scan-card" role="status">
          <span className="mc-spinner tiny" />
          <div>
            <strong>{scanning.length ? `正在扫描 ${scanning.map((library) => library.name).join("、")}` : "正在整理视频"}</strong>
            <small>{pending ? `还有 ${pending} 个视频待读取时长与画面` : "新找到的视频会陆续出现"}</small>
          </div>
        </div>
      )}
    </nav>
  );
}
