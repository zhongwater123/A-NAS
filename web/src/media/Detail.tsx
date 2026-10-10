import { Check, FileVideo, Heart, Languages, ListPlus, Play, RotateCcw, Star, Subtitles } from "lucide-react";
import { useEffect, useMemo, useState } from "react";

import { Empty, Loading, messageOf } from "./Browse";
import { Artwork, useMediaActions } from "./Cards";
import { bytes, episodeCode, episodeName, progressShare, remaining, runtime, timecode } from "./format";
import { getShow, getVideo, type Episode, type ShowDetail, type Title, type VideoDetail } from "./mediaApi";

function Meta({ title, extra = [] }: { title: Title; extra?: (string | undefined)[] }) {
  const parts = [
    title.year ? String(title.year) : "",
    ...extra,
    title.resolution,
    title.hdr,
  ].filter(Boolean) as string[];
  return (
    <p className="mc-detail-meta">
      {title.rating ? <span className="mc-rating"><Star />{title.rating.toFixed(1)}</span> : null}
      {parts.map((part, index) => <span key={index}>{part}</span>)}
      {title.genres?.slice(0, 4).map((genre) => <span key={genre} className="mc-genre">{genre}</span>)}
    </p>
  );
}

function Backdrop({ title }: { title: Title }) {
  return (
    <div className="mc-detail-backdrop" aria-hidden="true">
      <Artwork title={title} kind="backdrop" fallback="thumb" label={false} eager />
      <div className="mc-detail-shade" />
    </div>
  );
}

export function VideoPage({ id, refreshKey, onBack, onError }: { id: string; refreshKey: number; onBack: () => void; onError: (message: string) => void }) {
  const actions = useMediaActions();
  const [detail, setDetail] = useState<VideoDetail>();
  const [missing, setMissing] = useState(false);
  useEffect(() => {
    let live = true;
    getVideo(id).then((value) => { if (live) { setDetail(value); setMissing(false); } }, (caught) => { if (live) { setMissing(true); onError(messageOf(caught)); } });
    return () => { live = false; };
  }, [id, refreshKey, onError]);
  if (missing) return <Empty icon={<FileVideo />} title="视频不存在" text="它可能已被移动或删除。"><button type="button" className="mc-glass" onClick={onBack}>返回</button></Empty>;
  if (!detail) return <Loading />;
  const progress = detail.progress;
  const resuming = Boolean(progress?.position);
  const watched = Boolean(progress?.watched);
  return (
    <div className="mc-detail">
      <Backdrop title={detail} />
      <div className={`mc-detail-main ${detail.type === "other" ? "wide" : ""}`}>
        {detail.type !== "other" && <div className="mc-detail-poster"><Artwork title={detail} kind="poster" fallback="thumb" eager /></div>}
        <div className="mc-detail-info">
          {detail.originalTitle && detail.originalTitle !== detail.title && <p className="mc-detail-original">{detail.originalTitle}</p>}
          <h1>{detail.type === "episode" && detail.showTitle ? detail.showTitle : detail.title}</h1>
          {detail.type === "episode" && <p className="mc-detail-episode">{episodeName({ season: detail.season ?? 0, episode: detail.episode ?? 0, title: detail.title })}</p>}
          <Meta title={detail} extra={[runtime(detail.duration)]} />
          {resuming && progress && (
            <div className="mc-detail-progress">
              <span><span style={{ width: `${progressShare(progress) * 100}%` }} /></span>
              <small>看到 {timecode(progress.position)} · {remaining(progress.position, progress.duration)}</small>
            </div>
          )}
          <div className="mc-detail-actions">
            <button type="button" className="mc-primary" onClick={() => actions.play(detail)}><Play />{resuming ? "继续播放" : "播放"}</button>
            {resuming && <button type="button" className="mc-glass" onClick={() => actions.play(detail, true)}><RotateCcw />从头播放</button>}
            <button type="button" className={`mc-round ${detail.favorite ? "on" : ""}`} aria-pressed={detail.favorite} aria-label={detail.favorite ? "取消收藏" : "收藏"} title={detail.favorite ? "取消收藏" : "收藏"}
              onClick={() => { actions.toggleFavorite(detail); setDetail({ ...detail, favorite: !detail.favorite }); }}><Heart /></button>
            <button type="button" className={`mc-round ${watched ? "on" : ""}`} aria-pressed={watched} aria-label={watched ? "标记为未看" : "标记为已看"} title={watched ? "标记为未看" : "标记为已看"}
              onClick={() => { actions.setWatched(detail, !watched); setDetail({ ...detail, progress: { position: 0, duration: detail.duration ?? 0, watched: !watched } }); }}><Check /></button>
            {detail.type !== "episode" && <button type="button" className="mc-round" aria-label="加入合集" title="加入合集" onClick={() => actions.addToCollection(detail)}><ListPlus /></button>}
          </div>
          {detail.plot && <p className="mc-detail-plot">{detail.plot}</p>}
          {(detail.collections.length > 0 || detail.collection) && (
            <p className="mc-detail-collections">合集：{[detail.collection, ...detail.collections.map((item) => item.name)].filter(Boolean).join("、")}</p>
          )}
        </div>
      </div>
      <section className={`mc-detail-more ${detail.type === "other" ? "wide" : ""}`} aria-label="文件信息">
          <dl className="mc-detail-facts">
            <div><dt>文件</dt><dd title={`${detail.file.folder}/${detail.file.name}`}>{detail.file.name}</dd></div>
            <div><dt>位置</dt><dd>{detail.file.folder}</dd></div>
            <div><dt>大小</dt><dd>{bytes(detail.file.sizeBytes)}</dd></div>
            {detail.media && <div><dt>格式</dt><dd>{[detail.media.container, detail.media.videoCodec, detail.media.audioCodec, detail.media.width && detail.media.height ? `${detail.media.width}×${detail.media.height}` : ""].filter(Boolean).join(" · ")}</dd></div>}
            {detail.audio.length > 1 && <div><dt><Languages />音轨</dt><dd>{detail.audio.map((track) => track.label).join("、")}</dd></div>}
            {detail.subtitles.length > 0 && <div><dt><Subtitles />字幕</dt><dd>{detail.subtitles.map((track) => track.label).join("、")}</dd></div>}
            {detail.probeState === "pending" && <div><dt>状态</dt><dd>正在读取视频信息…</dd></div>}
            {detail.probeState === "failed" && <div><dt>状态</dt><dd className="warn">无法读取这个视频的信息，可以尝试直接播放</dd></div>}
          </dl>
      </section>
    </div>
  );
}

function EpisodeRow({ show, episode, current }: { show: ShowDetail; episode: Episode; current: boolean }) {
  const actions = useMediaActions();
  const title: Title = {
    id: episode.id, type: "episode", libraryId: show.libraryId, title: episode.title, addedAt: episode.addedAt, artwork: episode.artwork,
    favorite: false, progress: episode.progress, duration: episode.duration, season: episode.season, episode: episode.episode, showId: show.id, showTitle: show.title,
  };
  const share = progressShare(episode.progress);
  const watched = Boolean(episode.progress?.watched);
  return (
    <li className={`mc-episode ${current ? "current" : ""}`}>
      <button type="button" className="mc-episode-art" onClick={() => actions.play(title)} aria-label={`播放${episodeName(episode)}`}>
        <Artwork title={{ ...title, artwork: episode.artwork.thumb ? episode.artwork : show.artwork }} kind="thumb" fallback="backdrop" label={false} />
        <span className="mc-episode-play"><Play /></span>
        {share > 0 && !watched && <span className="mc-progress"><span style={{ width: `${share * 100}%` }} /></span>}
      </button>
      <div className="mc-episode-text">
        <strong>{episode.title === `第 ${episode.episode} 集` ? episode.title : <><span className="mc-episode-number">{episode.episode || "·"}</span>{episode.title}</>}</strong>
        <small>{[runtime(episode.duration), watched ? "已看完" : episode.progress?.position ? remaining(episode.progress.position, episode.progress.duration) : ""].filter(Boolean).join(" · ")}</small>
        {episode.plot && <p>{episode.plot}</p>}
      </div>
      <button type="button" className={`mc-round small ${watched ? "on" : ""}`} aria-pressed={watched} aria-label={watched ? "标记为未看" : "标记为已看"} title={watched ? "标记为未看" : "标记为已看"}
        onClick={() => actions.setWatched(title, !watched)}><Check /></button>
    </li>
  );
}

export function ShowPage({ id, refreshKey, onBack, onError }: { id: string; refreshKey: number; onBack: () => void; onError: (message: string) => void }) {
  const actions = useMediaActions();
  const [detail, setDetail] = useState<ShowDetail>();
  const [season, setSeason] = useState<number>();
  const [missing, setMissing] = useState(false);
  useEffect(() => {
    let live = true;
    getShow(id).then((value) => {
      if (!live) return;
      setDetail(value);
      setMissing(false);
      setSeason((current) => current !== undefined && value.seasonList.some((item) => item.number === current) ? current : value.next?.season ?? value.seasonList[0]?.number);
    }, (caught) => { if (live) { setMissing(true); onError(messageOf(caught)); } });
    return () => { live = false; };
  }, [id, refreshKey, onError]);
  const episodes = useMemo(() => detail?.seasonList.find((item) => item.number === season)?.episodes ?? [], [detail, season]);
  if (missing) return <Empty icon={<FileVideo />} title="剧集不存在" text="它可能已被移动或删除。"><button type="button" className="mc-glass" onClick={onBack}>返回</button></Empty>;
  if (!detail) return <Loading />;
  const next = detail.next;
  const allWatched = Boolean(detail.episodes && detail.watchedEpisodes === detail.episodes);
  const playNext = () => {
    if (!next) return;
    actions.play({ id: next.id, type: "episode", libraryId: detail.libraryId, title: next.title, addedAt: next.addedAt, artwork: next.artwork, favorite: false,
      progress: next.progress, duration: next.duration, season: next.season, episode: next.episode, showId: detail.id, showTitle: detail.title });
  };
  const nextLabel = next ? (next.progress?.position ? `继续播放 ${episodeCode(next.season, next.episode)}` : `播放 ${episodeCode(next.season, next.episode)}`) : "播放";
  return (
    <div className="mc-detail">
      <Backdrop title={detail} />
      <div className="mc-detail-main">
        <div className="mc-detail-poster"><Artwork title={detail} kind="poster" fallback="thumb" eager /></div>
        <div className="mc-detail-info">
          {detail.originalTitle && detail.originalTitle !== detail.title && <p className="mc-detail-original">{detail.originalTitle}</p>}
          <h1>{detail.title}</h1>
          <Meta title={detail} extra={[`${detail.seasons} 季`, `${detail.episodes} 集`]} />
          <div className="mc-detail-actions">
            <button type="button" className="mc-primary" onClick={playNext} disabled={!next}><Play />{nextLabel}</button>
            <button type="button" className={`mc-round ${detail.favorite ? "on" : ""}`} aria-pressed={detail.favorite} aria-label={detail.favorite ? "取消收藏" : "收藏"} title={detail.favorite ? "取消收藏" : "收藏"}
              onClick={() => { actions.toggleFavorite(detail); setDetail({ ...detail, favorite: !detail.favorite }); }}><Heart /></button>
            <button type="button" className={`mc-round ${allWatched ? "on" : ""}`} aria-pressed={allWatched} aria-label={allWatched ? "全部标记为未看" : "全部标记为已看"} title={allWatched ? "全部标记为未看" : "全部标记为已看"}
              onClick={() => actions.setWatched(detail, !allWatched)}><Check /></button>
            <button type="button" className="mc-round" aria-label="加入合集" title="加入合集" onClick={() => actions.addToCollection(detail)}><ListPlus /></button>
          </div>
          {detail.plot && <p className="mc-detail-plot">{detail.plot}</p>}
          <p className="mc-detail-collections">已看 {detail.watchedEpisodes ?? 0} / {detail.episodes} 集{detail.collections.length ? ` · 合集：${detail.collections.map((item) => item.name).join("、")}` : ""}</p>
        </div>
      </div>
      <section className="mc-seasons" aria-label="剧集">
        {detail.seasonList.length > 1 && (
          <div className="mc-season-tabs" role="tablist" aria-label="季">
            {detail.seasonList.map((item) => (
              <button key={item.number} type="button" role="tab" aria-selected={item.number === season} className={item.number === season ? "active" : ""} onClick={() => setSeason(item.number)}>
                {item.number ? `第 ${item.number} 季` : "特别篇"}<small>{item.episodes.length}</small>
              </button>
            ))}
          </div>
        )}
        <ol className="mc-episodes">
          {episodes.map((episode) => <EpisodeRow key={episode.id} show={detail} episode={episode} current={episode.id === next?.id} />)}
        </ol>
      </section>
    </div>
  );
}
