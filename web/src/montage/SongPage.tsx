import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronLeft, Music2, Pause, Play, Search } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

import { errorText } from "../api/errorMessages";
import { coverURL, listTracks, streamURL } from "../music/api";
import { Spinner } from "../ui/workspace";
import { chooseSong, type MontageSong, songAudioURL, updateProject } from "./api";
import { Failed } from "./Failed";
import styles from "./montage.module.css";
import { clock, songQuery } from "./queries";

// Why a song is not ready yet, or cannot be used (MontageSong.waiting, .reason).
const songWaits: Record<NonNullable<MontageSong["waiting"]>, string> = {
  analyzer_unavailable: "等配乐分析服务就绪，几分钟后自动重试",
  compute_unavailable: "等计算服务就绪，几分钟后自动重试",
  media_unavailable: "等媒体服务就绪，几分钟后自动重试",
};
const songReasons: Record<NonNullable<MontageSong["reason"]>, string> = {
  undecodable: "这首歌解码不了",
  few_bars: "这首歌太短，不到 16 小节",
  no_excerpt: "找不到 30 秒到 150 秒的一段",
  long_slot: "节拍太慢，一个镜头会超过 12 秒",
  analysis_failed: "这首歌分析了三次都没成功，换一首试试",
};
// The prototype's covers: a colour per song, its first letter on it.
const covers = [
  "linear-gradient(135deg, #a3c43c, #5c8f2a)",
  "linear-gradient(135deg, #79d36b, #2f9e55)",
  "linear-gradient(135deg, #d04fc2, #7b2ba8)",
  "linear-gradient(135deg, #c9b04a, #7f7a2a)",
  "linear-gradient(135deg, #4aa8ff, #5b6cff)",
  "linear-gradient(135deg, #ff9a4d, #ff5f7e)",
];
const coverOf = (title: string) => covers[[...title].reduce((sum, c) => sum + c.charCodeAt(0), 0) % covers.length];

// SongPage is the prototype's 配乐 page: the chosen song with the excerpt
// the film is cut to, then the member's music library to choose from.
export function SongPage({ id, onBack }: { id: string; onBack: () => void }) {
  const client = useQueryClient();
  const song = useQuery(songQuery(id));
  const tracks = useQuery({ queryKey: ["music", "tracks"], queryFn: listTracks });
  const [filter, setFilter] = useState("");
  const [playing, setPlaying] = useState<string>();
  const audio = useRef<HTMLAudioElement>(null);
  const choose = useMutation({
    mutationFn: (trackId: string) => chooseSong(id, trackId),
    onSuccess: (chosen) => client.setQueryData(["montage", "song", id], chosen),
  });
  const shown = useMemo(() => {
    const words = filter.trim().toLowerCase();
    return (tracks.data?.items ?? []).filter(
      (track) => !words || `${track.title} ${track.artist ?? ""}`.toLowerCase().includes(words),
    );
  }, [tracks.data, filter]);
  useEffect(() => {
    const player = audio.current;
    if (!player) return;
    if (playing) {
      player.src = streamURL(playing);
      void player.play().catch(() => setPlaying(undefined));
    } else {
      player.pause();
    }
  }, [playing]);
  return (
    <main className={styles.mtSub}>
      <button type="button" className={`${styles.mtBtn} ${styles.small} ${styles.mtBack}`} onClick={onBack}>
        <ChevronLeft aria-hidden="true" />
        返回
      </button>
      <section className={`${styles.mtPanel} ${styles.mtStep} ${styles.tune}`} aria-label="配乐">
        <div className={styles.mtStepH}>
          <span className={styles.mtBadge} aria-hidden="true">
            <Music2 />
          </span>
          <div>
            <div className={styles.mtStepN}>成片的长度和节奏跟着它走</div>
            <h2>配乐</h2>
          </div>
          {tracks.data && <small>曲库 {tracks.data.items.length} 首</small>}
        </div>
        {song.error && <Failed inline error={song.error} onRetry={() => void song.refetch()} />}
        {song.data && <Chosen projectId={id} song={song.data} />}
        <div className={styles.mtSearch}>
          <Search aria-hidden="true" />
          <input
            className={styles.mtText}
            type="search"
            placeholder="搜索曲库：歌名或歌手"
            aria-label="搜索歌曲"
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
          />
        </div>
        <div className={styles.mtLabel}>音乐库</div>
        {tracks.error && <Failed inline error={tracks.error} onRetry={() => void tracks.refetch()} />}
        {choose.error && <p className={styles.mtError}>{errorText(choose.error)}</p>}
        {!tracks.data && !tracks.error && <Spinner small />}
        {tracks.data && shown.length === 0 && (
          <p className={styles.mtFaint}>
            {filter.trim() ? "曲库里没有匹配的歌。" : "曲库里还没有歌：先把歌放进“音乐”文件夹，再回来选。"}
          </p>
        )}
        <ul className={styles.mtSongs} aria-label="音乐库">
          {shown.map((track) => {
            const on = song.data?.title === track.title;
            return (
              <li key={track.id} className={`${styles.mtSong} ${on ? styles.on : ""}`}>
                <span className={styles.mtCover} style={{ background: coverOf(track.title) }} aria-hidden="true">
                  {track.coverId ? <img src={coverURL(track.coverId)} alt="" /> : [...track.title][0]}
                </span>
                <span className={styles.mtSongTx}>
                  <b>{track.title}</b>
                  {track.artist && <span className={styles.mtWho}>{track.artist}</span>}
                  {track.durationMs !== undefined && (
                    <span className={styles.mtMeta}>
                      <span className={styles.mtTag}>{clock(track.durationMs / 1000)}</span>
                      {track.durationMs > 8 * 60_000 && (
                        <span className={`${styles.mtTag} ${styles.warn}`}>超过 8 分钟</span>
                      )}
                    </span>
                  )}
                </span>
                <button
                  type="button"
                  className={`${styles.mtPlay} ${playing === track.id ? styles.on : ""}`}
                  aria-label={playing === track.id ? `暂停 ${track.title}` : `试听 ${track.title}`}
                  onClick={() => setPlaying(playing === track.id ? undefined : track.id)}
                >
                  {playing === track.id ? <Pause /> : <Play />}
                </button>
                <button
                  type="button"
                  className={`${styles.mtCheck} ${on ? styles.on : ""}`}
                  aria-label={`用《${track.title}》配乐`}
                  aria-pressed={on}
                  disabled={choose.isPending}
                  onClick={() => choose.mutate(track.id)}
                >
                  <Check />
                </button>
              </li>
            );
          })}
        </ul>
        {/* biome-ignore lint/a11y/useMediaCaption: a song preview has no captions */}
        <audio ref={audio} hidden onEnded={() => setPlaying(undefined)} />
      </section>
      <div className={styles.mtFoot}>
        <button type="button" className={`${styles.mtBtn} ${styles.primary}`} onClick={onBack}>
          <Check aria-hidden="true" />
          好了，回去开始剪辑
        </button>
      </div>
    </main>
  );
}

// Chosen is the song the project uses: how far its analysis got, and the
// excerpt the film is cut to, which the member may move and listen to.
function Chosen({ projectId, song }: { projectId: string; song: MontageSong }) {
  const client = useQueryClient();
  const excerpt = song.selection?.excerpt;
  const [start, setStart] = useState<number>();
  const at = start ?? excerpt?.[0] ?? 0;
  const span = excerpt ? excerpt[1] - excerpt[0] : 0;
  const move = useMutation({
    mutationFn: (seconds: number) => updateProject(projectId, { songStart: seconds }),
    onSuccess: () => {
      setStart(undefined);
      void client.invalidateQueries({ queryKey: ["montage", "song", projectId] });
    },
  });
  const status =
    song.state === "analyzing"
      ? song.waiting
        ? songWaits[song.waiting]
        : "正在分析节拍与段落…"
      : song.state === "unusable"
        ? `${song.reason ? songReasons[song.reason] : "这首歌用不了"}，换一首吧`
        : undefined;
  return (
    <div className={styles.mtChosen}>
      <div className={`${styles.mtSong} ${styles.on}`}>
        <span className={styles.mtCover} style={{ background: coverOf(song.title) }} aria-hidden="true">
          {[...song.title][0]}
        </span>
        <span className={styles.mtSongTx}>
          <b>{song.title}</b>
          <span className={styles.mtWhy}>{status ?? "当前配乐"}</span>
          <span className={styles.mtMeta}>
            <span className={styles.mtTag}>{clock(song.duration)}</span>
            {song.tempo !== undefined && <span className={styles.mtTag}>{Math.round(song.tempo)} BPM</span>}
            {excerpt && song.selection?.chorus && <span className={styles.mtTag}>{clock(excerpt[0])} 进副歌</span>}
            {song.selection && <span className={styles.mtTag}>约 {Math.round(song.selection.seconds)} 秒</span>}
          </span>
        </span>
      </div>
      {excerpt && (
        <>
          <div className={styles.mtSongbar} role="img" aria-label={`选段 ${clock(at)}–${clock(at + span)}`}>
            <span style={{ left: `${(at / song.duration) * 100}%`, width: `${(span / song.duration) * 100}%` }}>
              选段
            </span>
          </div>
          <div className={styles.mtScale}>
            <span>0:00</span>
            <span>
              选段 {clock(at)}–{clock(at + span)}
            </span>
            <span>{clock(song.duration)}</span>
          </div>
          {song.selection?.chorus === false && <p className={styles.mtNote}>没找到副歌，用了最响的一段：请试听确认</p>}
          <input
            className={styles.mtRange}
            type="range"
            aria-label="选段起点"
            min={0}
            max={Math.max(0, song.duration - span)}
            step={0.5}
            value={at}
            onChange={(event) => setStart(Number(event.target.value))}
            onPointerUp={() => start !== undefined && move.mutate(start)}
            onKeyUp={() => start !== undefined && move.mutate(start)}
          />
          {/* biome-ignore lint/a11y/useMediaCaption: a song excerpt has no captions */}
          <audio controls preload="none" src={songAudioURL(projectId, song.transcode ? excerpt[0] : undefined)} />
          {move.error && <p className={styles.mtError}>{errorText(move.error)}</p>}
        </>
      )}
    </div>
  );
}
