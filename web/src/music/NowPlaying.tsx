import { ChevronDown, Disc3, Mic2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

import { APIError } from "../api/client";
import { coverURL, getLyrics } from "./api";
import { Cover, useMusic } from "./context";
import {
  activeLine,
  artistKey,
  artistLine,
  artistsOf,
  formatDuration,
  type Lyrics,
  parseLyrics,
  qualityOf,
  unknownAlbum,
} from "./library";
import styles from "./music.module.css";
import { usePlaybackTime } from "./player";

type LyricsState = { trackId: string; lyrics?: Lyrics; missing?: boolean; failed?: boolean };
// A song without lyrics has none to load; one with lyrics has nothing until they arrive.
const lyricsOf = (track: { id: string; lyrics?: unknown } | undefined): LyricsState | undefined =>
  track && !track.lyrics ? { trackId: track.id, missing: true } : undefined;

// NowPlaying is the immersive view of the playing song: large art on a
// blurred backdrop and lyrics that follow the music.
export function NowPlaying({ onClose }: { onClose: () => void }) {
  const music = useMusic();
  const track = music.player.current;
  const [state, setState] = useState(() => lyricsOf(track));
  const [trackBefore, setTrackBefore] = useState(track);
  if (trackBefore !== track) {
    setTrackBefore(track);
    if (track) setState(lyricsOf(track));
  }
  useEffect(() => {
    if (!track?.lyrics) return;
    let live = true;
    getLyrics(track.id).then(
      (text) => {
        if (live) setState({ trackId: track.id, lyrics: parseLyrics(text) });
      },
      (error: unknown) => {
        if (live)
          setState({
            trackId: track.id,
            missing: error instanceof APIError && error.status === 404,
            failed: !(error instanceof APIError && error.status === 404),
          });
      },
    );
    return () => {
      live = false;
    };
  }, [track]);
  useEffect(() => {
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", escape);
    return () => window.removeEventListener("keydown", escape);
  }, [onClose]);

  if (!track) return null;
  const current = state?.trackId === track.id ? state : undefined;
  return (
    <section className={styles.muStage} aria-label="正在播放">
      {track.coverId && (
        <span
          className={styles.muStageBackdrop}
          style={{ backgroundImage: `url("${coverURL(track.coverId)}")` }}
          aria-hidden="true"
        />
      )}
      <button type="button" className={styles.muStageClose} aria-label="收起正在播放" onClick={onClose}>
        <ChevronDown />
      </button>
      <div className={styles.muStageBody}>
        <div className={styles.muStageArt}>
          <Cover id={track.coverId} name={track.album || track.title} size="large" />
          <h2>{track.title}</h2>
          <p>
            {artistsOf(track).map((name, at) => (
              <span key={name}>
                {at > 0 && " / "}
                <button
                  type="button"
                  onClick={() => {
                    music.open({ kind: "artist", key: artistKey(name) });
                    onClose();
                  }}
                >
                  {name}
                </button>
              </span>
            ))}
          </p>
          <p className={styles.muStageAlbum}>
            <button
              type="button"
              onClick={() => {
                music.open({ kind: "album", id: track.albumId });
                onClose();
              }}
            >
              <Disc3 />
              {track.album || unknownAlbum}
            </button>
            <em>{qualityOf(track)}</em>
          </p>
        </div>
        <div className={styles.muStageLyrics}>
          {current?.lyrics?.lines.length ? (
            <LyricsView lyrics={current.lyrics} />
          ) : (
            <div className={styles.muStageNolyrics}>
              <Mic2 />
              <p>{!current ? "正在载入歌词…" : current.failed ? "歌词暂时无法读取" : "暂无歌词"}</p>
              {current && !current.failed && (
                <small>
                  把同名的 .lrc 歌词文件放在歌曲旁边，例如“{track.name.replace(/\.[^.]+$/, "")}
                  .lrc”，就能在这里看到随音乐滚动的歌词。
                </small>
              )}
              <dl>
                <dt>歌手</dt>
                <dd>{artistLine(track)}</dd>
                {track.year ? (
                  <>
                    <dt>年份</dt>
                    <dd>{track.year}</dd>
                  </>
                ) : null}
                {track.genres?.length ? (
                  <>
                    <dt>类型</dt>
                    <dd>{track.genres.join(" / ")}</dd>
                  </>
                ) : null}
                <dt>时长</dt>
                <dd>{formatDuration(track.durationMs)}</dd>
              </dl>
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

function LyricsView({ lyrics }: { lyrics: Lyrics }) {
  const { player } = useMusic();
  const time = usePlaybackTime(player);
  const active = activeLine(lyrics, time.position);
  const container = useRef<HTMLDivElement>(null);
  // Reading back through the lyrics pauses following for a few seconds.
  const manual = useRef(0);
  useEffect(() => {
    const element = container.current;
    if (!element || active < 0 || Date.now() - manual.current < 4000) return;
    const line = element.children[active] as HTMLElement | undefined;
    if (!line) return;
    element.scrollTo({ top: line.offsetTop - element.clientHeight / 2 + line.clientHeight / 2, behavior: "smooth" });
  }, [active]);
  const lines = useMemo(() => lyrics.lines, [lyrics]);
  return (
    <div
      ref={container}
      className={`${styles.muLyrics}${lyrics.synced ? ` ${styles.synced}` : ""}`}
      onWheel={() => {
        manual.current = Date.now();
      }}
      onTouchMove={() => {
        manual.current = Date.now();
      }}
    >
      {lines.map((line, at) => (
        <p key={at} className={at === active ? styles.active : at < active ? styles.past : ""}>
          {lyrics.synced ? (
            <button
              type="button"
              onClick={() => {
                manual.current = 0;
                player.seek(line.time);
              }}
            >
              {line.text || "♪"}
            </button>
          ) : (
            line.text
          )}
        </p>
      ))}
    </div>
  );
}
