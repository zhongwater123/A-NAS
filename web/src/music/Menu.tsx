import {
  ChevronRight,
  Disc3,
  Heart,
  HeartOff,
  Info,
  ListEnd,
  ListPlus,
  ListStart,
  Mic2,
  Play,
  Plus,
  Trash2,
} from "lucide-react";
import { ReactNode, useEffect, useLayoutEffect, useRef, useState } from "react";

import { type MenuTarget, useMusic } from "./context";
import { artistKey, artistsOf } from "./library";
import styles from "./music.module.css";

export interface MenuState {
  x: number;
  y: number;
  target: MenuTarget;
}

interface Props {
  menu: MenuState;
  onClose: () => void;
  onRemoveFromPlaylist: (playlistId: string, positions: number[]) => void;
  onInfo: (trackId: string) => void;
}

// TrackMenu is the context menu of songs: play, queue, playlists, favorites
// and where a song lives.
export function TrackMenu({ menu, onClose, onRemoveFromPlaylist, onInfo }: Props) {
  const music = useMusic();
  const ref = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState({ left: menu.x, top: menu.y, flip: false });
  const [submenu, setSubmenu] = useState(false);
  // Leaving closes the submenu after a moment, so the pointer can cross
  // over to it without it vanishing.
  const closing = useRef<number>(undefined);
  const openSubmenu = () => {
    window.clearTimeout(closing.current);
    setSubmenu(true);
  };
  const leaveSubmenu = () => {
    closing.current = window.setTimeout(() => setSubmenu(false), 280);
  };
  useEffect(() => () => window.clearTimeout(closing.current), []);
  useLayoutEffect(() => {
    const element = ref.current;
    const parent = element?.offsetParent as HTMLElement | null;
    if (!element || !parent) return;
    const edge = 8;
    const left = Math.max(edge, Math.min(menu.x, parent.clientWidth - element.offsetWidth - edge));
    const top = Math.max(edge, Math.min(menu.y, parent.clientHeight - element.offsetHeight - edge));
    setPosition({ left, top, flip: left + element.offsetWidth + 220 > parent.clientWidth });
  }, [menu]);
  useEffect(() => {
    const close = (event: Event) => {
      if (!ref.current?.contains(event.target as Node)) onClose();
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("pointerdown", close, true);
    window.addEventListener("keydown", escape);
    window.addEventListener("blur", onClose);
    return () => {
      window.removeEventListener("pointerdown", close, true);
      window.removeEventListener("keydown", escape);
      window.removeEventListener("blur", onClose);
    };
  }, [onClose]);

  const { ids, playlist } = menu.target;
  const single = ids.length === 1 ? music.index.byId.get(ids[0]) : undefined;
  const allFavorite = ids.every((id) => music.favorites.has(id));
  const act = (run: () => void) => () => {
    run();
    onClose();
  };
  const item = (icon: ReactNode, label: string, run: () => void, danger = false) => (
    <button type="button" role="menuitem" className={danger ? styles.danger : ""} onClick={act(run)}>
      {icon}
      <span>{label}</span>
    </button>
  );

  return (
    <div
      ref={ref}
      className={styles.muMenu}
      role="menu"
      style={{ left: position.left, top: position.top }}
      onContextMenu={(event) => event.preventDefault()}
    >
      {ids.length > 1 && <p className={styles.muMenuTitle}>已选 {ids.length} 首歌曲</p>}
      {item(<Play />, "播放", () => music.player.play(ids, 0))}
      {item(<ListStart />, "下一首播放", () => music.player.playNext(ids))}
      {item(<ListEnd />, "添加到播放队列", () => music.player.enqueue(ids))}
      <div className={styles.muMenuSub} onPointerEnter={openSubmenu} onPointerLeave={leaveSubmenu}>
        <button type="button" role="menuitem" aria-haspopup="menu" aria-expanded={submenu} onClick={openSubmenu}>
          <ListPlus />
          <span>添加到歌单</span>
          <ChevronRight className={styles.muMenuChevron} />
        </button>
        {submenu && (
          <div className={`${styles.muMenu} ${styles.muSubmenu}${position.flip ? ` ${styles.flip}` : ""}`} role="menu">
            {item(<Plus />, "新建歌单…", () => music.addToPlaylist(undefined, ids))}
            {music.playlists.length > 0 && <hr />}
            <div className={styles.muSubmenuList}>
              {music.playlists.map((list) => (
                <button
                  key={list.id}
                  type="button"
                  role="menuitem"
                  onClick={act(() => music.addToPlaylist(list.id, ids))}
                >
                  <span>{list.name}</span>
                  <small>{list.trackIds.length}</small>
                </button>
              ))}
            </div>
          </div>
        )}
      </div>
      <hr />
      {item(
        allFavorite ? <HeartOff /> : <Heart />,
        allFavorite ? "取消收藏" : ids.length > 1 ? `收藏这 ${ids.length} 首` : "收藏",
        () => {
          for (const id of ids) if (music.favorites.has(id) === allFavorite) music.toggleFavorite(id);
        },
      )}
      {single && <hr />}
      {single && item(<Disc3 />, "查看专辑", () => music.open({ kind: "album", id: single.albumId }))}
      {single && item(<Mic2 />, "查看歌手", () => music.open({ kind: "artist", key: artistKey(artistsOf(single)[0]) }))}
      {single && item(<Info />, "歌曲信息", () => onInfo(single.id))}
      {playlist && <hr />}
      {playlist && item(<Trash2 />, "从歌单移除", () => onRemoveFromPlaylist(playlist.id, playlist.positions), true)}
    </div>
  );
}
