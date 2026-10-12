import { Check, Clapperboard, Ellipsis, Film, Heart, ListPlus, Play, Tv, Video } from "lucide-react";
import { createContext, CSSProperties, KeyboardEvent, ReactNode, useContext, useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { heading, hue, progressShare, subheading, timecode, typeLabel } from "./format";
import { artworkURL, type ArtworkKind, type Title } from "./mediaApi";

export interface CardAction { label: string; icon?: ReactNode; danger?: boolean; run: () => void }

// MediaActions are what any card can do; the media center provides them.
export interface MediaActions {
  open: (title: Title) => void;
  // restart ignores saved progress.
  play: (title: Title, restart?: boolean) => void;
  toggleFavorite: (title: Title) => void;
  setWatched: (title: Title, watched: boolean) => void;
  addToCollection: (title: Title) => void;
}

export const ActionsContext = createContext<MediaActions | undefined>(undefined);
export function useMediaActions(): MediaActions {
  const actions = useContext(ActionsContext);
  if (!actions) throw new Error("media actions are missing");
  return actions;
}

const typeIcons = { movie: Film, show: Tv, episode: Tv, other: Video };

// Artwork shows a title's image, or a coloured placeholder with its name
// when the library has none.
export function Artwork({ title, kind, fallback, className = "", label = true, eager = false }: {
  title: Title; kind: ArtworkKind; fallback?: ArtworkKind; className?: string; label?: boolean; eager?: boolean;
}) {
  const choose = (candidate?: ArtworkKind) => candidate && title.artwork[candidate] ? candidate : undefined;
  const chosen = choose(kind) ?? choose(fallback);
  const [failed, setFailed] = useState(false);
  useEffect(() => setFailed(false), [title.id, chosen, title.artwork.version]);
  const tint = hue(heading(title));
  const style = { "--mc-hue": tint } as CSSProperties;
  if (!chosen || failed) {
    const Icon = typeIcons[title.type] ?? Clapperboard;
    return (
      <div className={`mc-art mc-art-empty ${className}`} style={style} aria-hidden="true">
        <Icon className="mc-art-icon" />
        {label && <span>{heading(title)}</span>}
      </div>
    );
  }
  return (
    <div className={`mc-art ${className}`} style={style}>
      <img src={artworkURL(title.id, chosen, title.artwork.version)} alt="" loading={eager ? "eager" : "lazy"} draggable={false} onError={() => setFailed(true)} />
    </div>
  );
}

function ProgressBar({ title }: { title: Title }) {
  const share = progressShare(title.progress);
  if (!share || title.progress?.watched) return null;
  return <span className="mc-progress" aria-label={`已看 ${Math.round(share * 100)}%`}><span style={{ width: `${Math.max(4, share * 100)}%` }} /></span>;
}

function Badges({ title }: { title: Title }) {
  const watched = title.type === "show" ? title.episodes && title.watchedEpisodes === title.episodes : title.progress?.watched;
  return (
    <div className="mc-badges">
      {title.resolution === "4K" && <span className="mc-badge">4K</span>}
      {title.hdr && <span className="mc-badge hdr">{title.hdr === "Dolby Vision" ? "DV" : "HDR"}</span>}
      {watched ? <span className="mc-badge watched" aria-label="已看完"><Check /></span> : null}
      {title.type === "show" && !watched && title.episodes ? <span className="mc-badge count">{title.episodes - (title.watchedEpisodes ?? 0)}</span> : null}
    </div>
  );
}

export function cardActions(title: Title, actions: MediaActions, extra: CardAction[] = []): CardAction[] {
  const watched = title.type === "show" ? Boolean(title.episodes && title.watchedEpisodes === title.episodes) : Boolean(title.progress?.watched);
  return [
    { label: title.progress?.position ? "继续播放" : "播放", icon: <Play />, run: () => actions.play(title) },
    ...(title.type !== "episode" ? [{ label: "查看详情", icon: <Clapperboard />, run: () => actions.open(title) }] : []),
    { label: title.favorite ? "取消收藏" : "收藏", icon: <Heart />, run: () => actions.toggleFavorite(title) },
    { label: watched ? "标记为未看" : "标记为已看", icon: <Check />, run: () => actions.setWatched(title, !watched) },
    ...(title.type !== "episode" ? [{ label: "加入合集…", icon: <ListPlus />, run: () => actions.addToCollection(title) }] : []),
    ...extra,
  ];
}

// Menu is a small popover of actions anchored to its trigger. It renders at
// the media center's root, so cards that clip their contents do not scroll
// or cut it off.
export function Menu({ items, label, className = "", children }: { items: CardAction[]; label: string; className?: string; children?: ReactNode }) {
  const [open, setOpen] = useState(false);
  const [place, setPlace] = useState<{ left: number; top: number; up: boolean; root: Element }>();
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (!open || !trigger.current) return;
    const root = trigger.current.closest(".mc-app");
    if (!root) return;
    const app = root.getBoundingClientRect();
    const box = trigger.current.getBoundingClientRect();
    const up = box.bottom + 40 * items.length + 20 > app.bottom;
    setPlace({ left: Math.min(box.right - app.left, app.width - 8), top: up ? box.top - app.top : box.bottom - app.top, up, root });
  }, [open, items.length]);
  useEffect(() => {
    if (!open) return;
    menu.current?.querySelector("button")?.focus({ preventScroll: true });
    const close = (event: PointerEvent) => {
      if (!menu.current?.contains(event.target as Node) && !trigger.current?.contains(event.target as Node)) setOpen(false);
    };
    window.addEventListener("pointerdown", close);
    return () => window.removeEventListener("pointerdown", close);
  }, [open, place]);
  const keys = (event: KeyboardEvent) => {
    if (event.key === "Escape") { event.stopPropagation(); setOpen(false); trigger.current?.focus({ preventScroll: true }); }
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const buttons = Array.from(menu.current?.querySelectorAll("button") ?? []);
      const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
      buttons[(index + (event.key === "ArrowDown" ? 1 : buttons.length - 1)) % buttons.length]?.focus({ preventScroll: true });
    }
  };
  return (
    <>
      <button ref={trigger} type="button" className={`mc-menu-trigger ${className}`} aria-label={label} aria-haspopup="menu" aria-expanded={open}
        onClick={(event) => { event.stopPropagation(); setOpen(!open); }}>
        {children ?? <Ellipsis />}
      </button>
      {open && place && createPortal(
        <div ref={menu} className={`mc-menu ${place.up ? "up" : ""}`} role="menu" aria-label={label} style={{ left: place.left, top: place.top }} onKeyDown={keys}>
          {items.map((item) => (
            <button key={item.label} type="button" role="menuitem" className={item.danger ? "danger" : ""}
              onClick={(event) => { event.stopPropagation(); setOpen(false); item.run(); }}>
              {item.icon}<span>{item.label}</span>
            </button>
          ))}
        </div>,
        place.root,
      )}
    </>
  );
}

// PosterCard is a 2:3 card for films and shows.
export function PosterCard({ title, extra }: { title: Title; extra?: CardAction[] }) {
  const actions = useMediaActions();
  return (
    <article className="mc-card mc-poster-card" aria-label={heading(title)}>
      <div className="mc-card-art" role="button" tabIndex={0} aria-label={`打开${heading(title)}`}
        onClick={() => actions.open(title)} onKeyDown={(event) => { if (event.key === "Enter") actions.open(title); }}>
        <Artwork title={title} kind="poster" fallback="thumb" />
        <Badges title={title} />
        <ProgressBar title={title} />
        <div className="mc-card-hover">
          <button type="button" className="mc-card-play" aria-label={`播放${heading(title)}`} onClick={(event) => { event.stopPropagation(); actions.play(title); }}><Play /></button>
          <button type="button" className={`mc-card-heart ${title.favorite ? "on" : ""}`} aria-label={title.favorite ? "取消收藏" : "收藏"} aria-pressed={title.favorite}
            onClick={(event) => { event.stopPropagation(); actions.toggleFavorite(title); }}><Heart /></button>
          <Menu label={`${heading(title)}的更多操作`} items={cardActions(title, actions, extra)} className="mc-card-more" />
        </div>
      </div>
      <div className="mc-card-text">
        <strong title={heading(title)}>{heading(title)}</strong>
        <small>{subheading(title) || typeLabel[title.type]}</small>
      </div>
    </article>
  );
}

// StillCard is a 16:9 card for episodes and other videos, and for anything
// the user is in the middle of.
export function StillCard({ title, extra, caption }: { title: Title; extra?: CardAction[]; caption?: string }) {
  const actions = useMediaActions();
  const playOnClick = title.type === "episode" || Boolean(title.progress?.position);
  const primary = () => (playOnClick ? actions.play(title) : actions.open(title));
  return (
    <article className="mc-card mc-still-card" aria-label={heading(title)}>
      <div className="mc-card-art" role="button" tabIndex={0} aria-label={playOnClick ? `播放${heading(title)}` : `打开${heading(title)}`}
        onClick={primary} onKeyDown={(event) => { if (event.key === "Enter") primary(); }}>
        <Artwork title={title} kind="thumb" fallback="backdrop" label={false} />
        <Badges title={title} />
        <ProgressBar title={title} />
        <div className="mc-card-hover">
          <button type="button" className="mc-card-play" aria-label={`播放${heading(title)}`} onClick={(event) => { event.stopPropagation(); actions.play(title); }}><Play /></button>
          <Menu label={`${heading(title)}的更多操作`} items={cardActions(title, actions, extra)} className="mc-card-more" />
        </div>
        {title.duration ? <span className="mc-card-duration">{timecode(title.duration)}</span> : null}
      </div>
      <div className="mc-card-text">
        <strong title={heading(title)}>{heading(title)}</strong>
        <small>{caption ?? (subheading(title) || typeLabel[title.type])}</small>
      </div>
    </article>
  );
}
