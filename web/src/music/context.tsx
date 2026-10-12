import { Disc3, ListMusic, Mic2, Music2 } from "lucide-react";
import { createContext, useContext, useState } from "react";

import { hue, type LibraryIndex } from "./library";
import { coverURL, type Playlist } from "./musicApi";
import type { Player } from "./player";

// Place is what the main area shows; detail places sit on a back stack.
export type Place =
  | { kind: "songs" }
  | { kind: "folders"; key?: string }
  | { kind: "albums" }
  | { kind: "album"; id: string }
  | { kind: "genres" }
  | { kind: "genre"; key: string }
  | { kind: "artists" }
  | { kind: "artist"; key: string }
  | { kind: "recent" }
  | { kind: "favorites" }
  | { kind: "playlist"; id: string }
  | { kind: "search" };

// MenuTarget is what a context menu acts on. Positions name the rows of a
// playlist, where one track can appear twice.
export interface MenuTarget { ids: string[]; playlist?: { id: string; positions: number[] } }

export interface Music {
  index: LibraryIndex;
  player: Player;
  favorites: ReadonlySet<string>;
  playlists: Playlist[];
  open: (place: Place) => void;
  toggleFavorite: (trackId: string) => void;
  showMenu: (point: { clientX: number; clientY: number }, target: MenuTarget) => void;
  // A missing playlist ID asks for a new playlist's name first.
  addToPlaylist: (playlistId: string | undefined, ids: string[]) => void;
}

export const MusicContext = createContext<Music | null>(null);

export function useMusic(): Music {
  const music = useContext(MusicContext);
  if (!music) throw new Error("useMusic outside the music window");
  return music;
}

// dragType carries track IDs from a list to a playlist, the queue or another
// row of the same playlist.
export const dragType = "application/x-anas-music-tracks";
export interface DragPayload { ids: string[]; playlist?: { id: string; positions: number[] } }

export function readDrag(transfer: DataTransfer): DragPayload | undefined {
  try {
    const value = JSON.parse(transfer.getData(dragType)) as DragPayload;
    return Array.isArray(value.ids) ? value : undefined;
  } catch { return undefined; }
}

export const carriesTracks = (transfer: DataTransfer) => Array.from(transfer.types).includes(dragType);

type CoverKind = "album" | "artist" | "playlist" | "track";

const fallbackIcons = { album: Disc3, artist: Mic2, playlist: ListMusic, track: Music2 };

// Cover shows album art, or a coloured tile named after the album or artist
// while there is none or it fails to load.
export function Cover({ id, name, kind = "album", size = "small", className = "" }: { id?: string; name: string; kind?: CoverKind; size?: "small" | "large"; className?: string }) {
  const [failed, setFailed] = useState<string>();
  const Icon = fallbackIcons[kind];
  const shade = hue(name || kind);
  if (id && failed !== id) {
    return <span className={`mu-cover ${kind} ${className}`}><img src={coverURL(id, size)} alt="" loading="lazy" draggable={false} onError={() => setFailed(id)} /></span>;
  }
  return (
    <span className={`mu-cover ${kind} fallback ${className}`} style={{ background: `linear-gradient(140deg, hsl(${shade} 62% 72%), hsl(${(shade + 40) % 360} 58% 52%))` }}>
      <Icon aria-hidden="true" />
    </span>
  );
}

// Mosaic shows up to four distinct covers of a playlist's albums.
export function Mosaic({ coverIds, name }: { coverIds: string[]; name: string }) {
  if (coverIds.length < 4) return <Cover id={coverIds[0]} name={name} kind="playlist" size="large" />;
  return <span className="mu-cover mu-mosaic">{coverIds.slice(0, 4).map((id) => <img key={id} src={coverURL(id)} alt="" loading="lazy" draggable={false} />)}</span>;
}

// Equalizer marks the track that is playing.
export function Equalizer({ paused }: { paused: boolean }) {
  return <span className={`mu-eq${paused ? " paused" : ""}`} aria-hidden="true"><i /><i /><i /></span>;
}
