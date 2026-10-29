import { Check, FileVideo, Heart, Languages, ListPlus, Play, RotateCcw, Star, Subtitles } from "lucide-react";
import { MouseEvent, ReactNode, useEffect, useMemo, useRef, useState } from "react";

import { formatBytes } from "../lib/bytes";
import {
  type Episode,
  getShow,
  getVideo,
  renameShow,
  renameVideoFile,
  type ShowDetail,
  type Title,
  type VideoDetail,
} from "./api";
import { Empty, Loading, messageOf } from "./Browse";
import { Artwork, type MediaActions, useMediaActions } from "./Cards";
import { EditableName, splitName } from "./EditableName";
import { episodeCode, episodeName, progressShare, remaining, runtime, timecode } from "./format";
import styles from "./media.module.css";

const nfoNote = "片名来自 NFO 文件，改名不会改变显示的片名";

// FileName is a video's name that a double-click renames: what shows is the
// title or the file name, what is edited is the file name, its 片源.
function FileName({
  as,
  text,
  videoId,
  file,
  namedByNfo,
  actions,
  onRenamed,
}: {
  as: "h1" | "p" | "strong" | "span";
  text: ReactNode;
  videoId: string;
  file: string;
  namedByNfo?: boolean;
  actions: MediaActions;
  onRenamed?: (detail: VideoDetail) => void;
}) {
  const { stem, extension } = splitName(file);
  return (
    <EditableName
      as={as}
      text={text}
      value={stem}
      suffix={extension}
      label="重命名片源"
      hint={namedByNfo ? `改的是片源文件名；${nfoNote}` : "改的是片源文件名，片名会按新名字重新识别"}
      onSave={async (name) => {
        const detail = await renameVideoFile(videoId, name + extension);
        onRenamed?.(detail);
        actions.renamed(`已重命名为“${detail.file.name}”，正在重新识别片名`);
      }}
    />
  );
}

function Meta({ title, extra = [] }: { title: Title; extra?: (string | undefined)[] }) {
  const parts = [title.year ? String(title.year) : "", ...extra, title.resolution, title.hdr].filter(
    Boolean,
  ) as string[];
  return (
    <p className={styles.mcDetailMeta}>
      {title.rating ? (
        <span className={styles.mcRating}>
          <Star />
          {title.rating.toFixed(1)}
        </span>
      ) : null}
      {parts.map((part, index) => (
        <span key={index}>{part}</span>
      ))}
      {title.genres?.slice(0, 4).map((genre) => (
        <span key={genre} className={styles.mcGenre}>
          {genre}
        </span>
      ))}
    </p>
  );
}

function Backdrop({ title }: { title: Title }) {
  return (
    <div className={styles.mcDetailBackdrop} aria-hidden="true">
      <Artwork title={title} kind="backdrop" fallback="thumb" label={false} eager />
      <div className={styles.mcDetailShade} />
    </div>
  );
}

export function VideoPage({
  id,
  refreshKey,
  onBack,
  onError,
}: {
  id: string;
  refreshKey: number;
  onBack: () => void;
  onError: (message: string) => void;
}) {
  const actions = useMediaActions();
  const [detail, setDetail] = useState<VideoDetail>();
  const [missing, setMissing] = useState(false);
  useEffect(() => {
    let live = true;
    getVideo(id).then(
      (value) => {
        if (live) {
          setDetail(value);
          setMissing(false);
        }
      },
      (caught) => {
        if (live) {
          setMissing(true);
          onError(messageOf(caught));
        }
      },
    );
    return () => {
      live = false;
    };
  }, [id, refreshKey, onError]);
  if (missing)
    return (
      <Empty icon={<FileVideo />} title="视频不存在" text="它可能已被移动或删除。">
        <button type="button" className={styles.mcGlass} onClick={onBack}>
          返回
        </button>
      </Empty>
    );
  if (!detail) return <Loading />;
  const progress = detail.progress;
  const resuming = Boolean(progress?.position);
  const watched = Boolean(progress?.watched);
  return (
    <div className={styles.mcDetail}>
      <Backdrop title={detail} />
      <div className={`${styles.mcDetailMain} ${detail.type === "other" ? styles.wide : ""}`}>
        {detail.type !== "other" && (
          <div className={styles.mcDetailPoster}>
            <Artwork title={detail} kind="poster" fallback="thumb" eager />
          </div>
        )}
        <div className={styles.mcDetailInfo}>
          {detail.originalTitle && detail.originalTitle !== detail.title && (
            <p className={styles.mcDetailOriginal}>{detail.originalTitle}</p>
          )}
          {detail.type === "episode" ? (
            <h1>{detail.showTitle || detail.title}</h1>
          ) : (
            <FileName
              as="h1"
              text={detail.title}
              videoId={detail.id}
              file={detail.file.name}
              namedByNfo={detail.namedByNfo}
              actions={actions}
              onRenamed={setDetail}
            />
          )}
          {detail.type === "episode" && (
            <FileName
              as="p"
              text={episodeName({ season: detail.season ?? 0, episode: detail.episode ?? 0, title: detail.title })}
              videoId={detail.id}
              file={detail.file.name}
              actions={actions}
              onRenamed={setDetail}
            />
          )}
          <Meta title={detail} extra={[runtime(detail.duration)]} />
          {resuming && progress && (
            <div className={styles.mcDetailProgress}>
              <span>
                <span style={{ width: `${progressShare(progress) * 100}%` }} />
              </span>
              <small>
                看到 {timecode(progress.position)} · {remaining(progress.position, progress.duration)}
              </small>
            </div>
          )}
          <div className={styles.mcDetailActions}>
            <button type="button" className={styles.mcPrimary} onClick={() => actions.play(detail)}>
              <Play />
              {resuming ? "继续播放" : "播放"}
            </button>
            {resuming && (
              <button type="button" className={styles.mcGlass} onClick={() => actions.play(detail, true)}>
                <RotateCcw />
                从头播放
              </button>
            )}
            <button
              type="button"
              className={`${styles.mcRound} ${detail.favorite ? styles.on : ""}`}
              aria-pressed={detail.favorite}
              aria-label={detail.favorite ? "取消收藏" : "收藏"}
              data-tip={detail.favorite ? "取消收藏" : "收藏"}
              onClick={() => {
                actions.toggleFavorite(detail);
                setDetail({ ...detail, favorite: !detail.favorite });
              }}
            >
              <Heart />
            </button>
            <button
              type="button"
              className={`${styles.mcRound} ${watched ? styles.on : ""}`}
              aria-pressed={watched}
              aria-label={watched ? "标记为未看" : "标记为已看"}
              data-tip={watched ? "标记为未看" : "标记为已看"}
              onClick={() => {
                actions.setWatched(detail, !watched);
                setDetail({ ...detail, progress: { position: 0, duration: detail.duration ?? 0, watched: !watched } });
              }}
            >
              <Check />
            </button>
            {detail.type !== "episode" && (
              <button
                type="button"
                className={styles.mcRound}
                aria-label="加入合集"
                data-tip="加入合集"
                onClick={() => actions.addToCollection(detail)}
              >
                <ListPlus />
              </button>
            )}
          </div>
          {detail.plot && <p className={styles.mcDetailPlot}>{detail.plot}</p>}
          {(detail.collections.length > 0 || detail.collection) && (
            <p className={styles.mcDetailCollections}>
              合集：{[detail.collection, ...detail.collections.map((item) => item.name)].filter(Boolean).join("、")}
            </p>
          )}
        </div>
      </div>
      <section className={`${styles.mcDetailMore} ${detail.type === "other" ? styles.wide : ""}`} aria-label="文件信息">
        <dl className={styles.mcDetailFacts}>
          <div className={styles.mcDetailFile}>
            <dt>片源</dt>
            <dd>
              <FileName
                as="span"
                text={<span className={styles.mcEditableText}>{detail.file.name}</span>}
                videoId={detail.id}
                file={detail.file.name}
                namedByNfo={detail.namedByNfo}
                actions={actions}
                onRenamed={setDetail}
              />
            </dd>
          </div>
          <div>
            <dt>位置</dt>
            <dd>{detail.file.folder}</dd>
          </div>
          <div>
            <dt>大小</dt>
            <dd>{formatBytes(detail.file.sizeBytes)}</dd>
          </div>
          {detail.media && (
            <div>
              <dt>格式</dt>
              <dd>
                {[
                  detail.media.container,
                  detail.media.videoCodec,
                  detail.media.audioCodec,
                  detail.media.width && detail.media.height ? `${detail.media.width}×${detail.media.height}` : "",
                ]
                  .filter(Boolean)
                  .join(" · ")}
              </dd>
            </div>
          )}
          {detail.audio.length > 1 && (
            <div>
              <dt>
                <Languages />
                音轨
              </dt>
              <dd>{detail.audio.map((track) => track.label).join("、")}</dd>
            </div>
          )}
          {detail.subtitles.length > 0 && (
            <div>
              <dt>
                <Subtitles />
                字幕
              </dt>
              <dd>{detail.subtitles.map((track) => track.label).join("、")}</dd>
            </div>
          )}
          {detail.probeState === "pending" && (
            <div>
              <dt>状态</dt>
              <dd>正在读取视频信息…</dd>
            </div>
          )}
          {detail.probeState === "failed" && (
            <div>
              <dt>状态</dt>
              <dd className={styles.warn}>无法读取这个视频的信息，可以尝试直接播放</dd>
            </div>
          )}
        </dl>
      </section>
    </div>
  );
}

function EpisodeRow({ show, episode, current }: { show: ShowDetail; episode: Episode; current: boolean }) {
  const actions = useMediaActions();
  const title: Title = {
    id: episode.id,
    type: "episode",
    libraryId: show.libraryId,
    title: episode.title,
    addedAt: episode.addedAt,
    artwork: episode.artwork,
    favorite: false,
    progress: episode.progress,
    duration: episode.duration,
    season: episode.season,
    episode: episode.episode,
    showId: show.id,
    showTitle: show.title,
  };
  const share = progressShare(episode.progress);
  const watched = Boolean(episode.progress?.watched);
  // Pressing anywhere on the row plays the episode. On its title a second
  // press renames it instead, so the title waits a moment before playing.
  const pending = useRef(0);
  useEffect(() => () => window.clearTimeout(pending.current), []);
  const press = (event: MouseEvent<HTMLLIElement>) => {
    const target = event.target as HTMLElement;
    window.clearTimeout(pending.current);
    if (target.closest(`button, input, .${styles.mcEditable}.${styles.editing}`)) return;
    if (!target.closest(`.${styles.mcEditable}`)) {
      actions.play(title);
      return;
    }
    if (event.detail === 1) pending.current = window.setTimeout(() => actions.play(title), 280);
  };
  const name =
    episode.title === `第 ${episode.episode} 集` ? (
      <span className={styles.mcEditableText}>{episode.title}</span>
    ) : (
      <>
        <span className={styles.mcEpisodeNumber}>{episode.episode || "·"}</span>
        <span className={styles.mcEditableText}>{episode.title}</span>
      </>
    );
  return (
    <li className={`${styles.mcEpisode} ${current ? styles.current : ""}`} onClick={press}>
      <button
        type="button"
        className={styles.mcEpisodeArt}
        onClick={() => actions.play(title)}
        aria-label={`播放${episodeName(episode)}`}
      >
        <Artwork
          title={{ ...title, artwork: episode.artwork.thumb ? episode.artwork : show.artwork }}
          kind="thumb"
          fallback="backdrop"
          label={false}
        />
        <span className={styles.mcEpisodePlay}>
          <Play />
        </span>
        {share > 0 && !watched && (
          <span className={styles.mcProgress}>
            <span style={{ width: `${share * 100}%` }} />
          </span>
        )}
      </button>
      <div className={styles.mcEpisodeText}>
        {episode.file ? (
          <FileName as="strong" text={name} videoId={episode.id} file={episode.file} actions={actions} />
        ) : (
          <strong>{name}</strong>
        )}
        <small>
          {[
            runtime(episode.duration),
            watched
              ? "已看完"
              : episode.progress?.position
                ? remaining(episode.progress.position, episode.progress.duration)
                : "",
          ]
            .filter(Boolean)
            .join(" · ")}
        </small>
        {episode.plot && <p>{episode.plot}</p>}
      </div>
      <button
        type="button"
        className={`${styles.mcRound} ${styles.small} ${watched ? styles.on : ""}`}
        aria-pressed={watched}
        aria-label={watched ? "标记为未看" : "标记为已看"}
        data-tip={watched ? "标记为未看" : "标记为已看"}
        data-tip-at="end"
        onClick={() => actions.setWatched(title, !watched)}
      >
        <Check />
      </button>
    </li>
  );
}

export function ShowPage({
  id,
  refreshKey,
  onBack,
  onError,
}: {
  id: string;
  refreshKey: number;
  onBack: () => void;
  onError: (message: string) => void;
}) {
  const actions = useMediaActions();
  const [detail, setDetail] = useState<ShowDetail>();
  const [season, setSeason] = useState<number>();
  const [missing, setMissing] = useState(false);
  useEffect(() => {
    let live = true;
    getShow(id).then(
      (value) => {
        if (!live) return;
        setDetail(value);
        setMissing(false);
        setSeason((current) =>
          current !== undefined && value.seasonList.some((item) => item.number === current)
            ? current
            : (value.next?.season ?? value.seasonList[0]?.number),
        );
      },
      (caught) => {
        if (live) {
          setMissing(true);
          onError(messageOf(caught));
        }
      },
    );
    return () => {
      live = false;
    };
  }, [id, refreshKey, onError]);
  const episodes = useMemo(
    () => detail?.seasonList.find((item) => item.number === season)?.episodes ?? [],
    [detail, season],
  );
  if (missing)
    return (
      <Empty icon={<FileVideo />} title="剧集不存在" text="它可能已被移动或删除。">
        <button type="button" className={styles.mcGlass} onClick={onBack}>
          返回
        </button>
      </Empty>
    );
  if (!detail) return <Loading />;
  const next = detail.next;
  const allWatched = Boolean(detail.episodes && detail.watchedEpisodes === detail.episodes);
  const playNext = () => {
    if (!next) return;
    actions.play({
      id: next.id,
      type: "episode",
      libraryId: detail.libraryId,
      title: next.title,
      addedAt: next.addedAt,
      artwork: next.artwork,
      favorite: false,
      progress: next.progress,
      duration: next.duration,
      season: next.season,
      episode: next.episode,
      showId: detail.id,
      showTitle: detail.title,
    });
  };
  const nextLabel = next
    ? next.progress?.position
      ? `继续播放 ${episodeCode(next.season, next.episode)}`
      : `播放 ${episodeCode(next.season, next.episode)}`
    : "播放";
  return (
    <div className={styles.mcDetail}>
      <Backdrop title={detail} />
      <div className={styles.mcDetailMain}>
        <div className={styles.mcDetailPoster}>
          <Artwork title={detail} kind="poster" fallback="thumb" eager />
        </div>
        <div className={styles.mcDetailInfo}>
          {detail.originalTitle && detail.originalTitle !== detail.title && (
            <p className={styles.mcDetailOriginal}>{detail.originalTitle}</p>
          )}
          <EditableName
            as="h1"
            text={detail.title}
            value={detail.folder || detail.title}
            label="重命名剧集"
            hint={[
              detail.folder ? "剧集文件夹和带剧名的各集文件一起改名" : "这部剧没有自己的文件夹，只改各集文件名里的剧名",
              detail.namedByNfo ? nfoNote : "",
            ]
              .filter(Boolean)
              .join("；")}
            onSave={async (name) => {
              setDetail(await renameShow(detail.id, name));
              actions.renamed(`已重命名为“${name}”，正在重新整理剧集`);
            }}
          />
          <Meta title={detail} extra={[`${detail.seasons} 季`, `${detail.episodes} 集`]} />
          <div className={styles.mcDetailActions}>
            <button type="button" className={styles.mcPrimary} onClick={playNext} disabled={!next}>
              <Play />
              {nextLabel}
            </button>
            <button
              type="button"
              className={`${styles.mcRound} ${detail.favorite ? styles.on : ""}`}
              aria-pressed={detail.favorite}
              aria-label={detail.favorite ? "取消收藏" : "收藏"}
              data-tip={detail.favorite ? "取消收藏" : "收藏"}
              onClick={() => {
                actions.toggleFavorite(detail);
                setDetail({ ...detail, favorite: !detail.favorite });
              }}
            >
              <Heart />
            </button>
            <button
              type="button"
              className={`${styles.mcRound} ${allWatched ? styles.on : ""}`}
              aria-pressed={allWatched}
              aria-label={allWatched ? "全部标记为未看" : "全部标记为已看"}
              data-tip={allWatched ? "全部标记为未看" : "全部标记为已看"}
              onClick={() => actions.setWatched(detail, !allWatched)}
            >
              <Check />
            </button>
            <button
              type="button"
              className={styles.mcRound}
              aria-label="加入合集"
              data-tip="加入合集"
              onClick={() => actions.addToCollection(detail)}
            >
              <ListPlus />
            </button>
          </div>
          {detail.plot && <p className={styles.mcDetailPlot}>{detail.plot}</p>}
          <p className={styles.mcDetailCollections}>
            已看 {detail.watchedEpisodes ?? 0} / {detail.episodes} 集
            {detail.collections.length ? ` · 合集：${detail.collections.map((item) => item.name).join("、")}` : ""}
          </p>
        </div>
      </div>
      <section className={styles.mcSeasons} aria-label="剧集">
        {detail.seasonList.length > 1 && (
          <div className={styles.mcSeasonTabs} role="tablist" aria-label="季">
            {detail.seasonList.map((item) => (
              <button
                key={item.number}
                type="button"
                role="tab"
                aria-selected={item.number === season}
                className={item.number === season ? styles.active : ""}
                onClick={() => setSeason(item.number)}
              >
                {item.number ? `第 ${item.number} 季` : "特别篇"}
                <small>{item.episodes.length}</small>
              </button>
            ))}
          </div>
        )}
        <ol className={styles.mcEpisodes}>
          {episodes.map((episode) => (
            <EpisodeRow key={episode.id} show={detail} episode={episode} current={episode.id === next?.id} />
          ))}
        </ol>
      </section>
    </div>
  );
}
